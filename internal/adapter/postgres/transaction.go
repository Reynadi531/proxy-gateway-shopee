package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pg-shopee/internal/domain"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

//go:embed schema.sql
var schema string

type Repository struct {
	DB            *gorm.DB
	sqlDB         *sql.DB
	UniqueCodeMin int64
	UniqueCodeMax int64
}

func Open(ctx context.Context, dsn string) (*Repository, error) {
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(5)
	r := &Repository{DB: db, sqlDB: sqlDB, UniqueCodeMin: 1, UniqueCodeMax: 999}
	if err = sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(7382110)").Error; err != nil {
			return err
		}
		// Keep the existing constraints and partial indexes compatible with deployed databases.
		for _, statement := range strings.Split(schema, ";") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if err := tx.Exec(statement).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	return r, nil
}

func (r *Repository) Close() error { return r.sqlDB.Close() }

func get(db *gorm.DB, id string) (domain.Transaction, error) {
	var row transactionRecord
	err := db.Where("transaction_id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = domain.ErrTransactionNotFound
	}
	return row.transaction(), err
}

func (r *Repository) Get(ctx context.Context, id string) (domain.Transaction, error) {
	return get(r.DB.WithContext(ctx), id)
}

func (r *Repository) Create(ctx context.Context, t domain.Transaction, build func(int64) (string, error)) (domain.Transaction, bool, error) {
	created := false
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The shared lock also serializes reconciliation across API instances.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(7382111)").Error; err != nil {
			return err
		}
		existing, err := get(tx, t.ID)
		if err == nil {
			if existing.Amount != t.Amount || existing.WebhookURL != t.WebhookURL || existing.Scope != t.Scope {
				return domain.ErrTransactionConflict
			}
			t = existing
			return nil
		}
		if !errors.Is(err, domain.ErrTransactionNotFound) {
			return err
		}
		err = tx.Raw(`SELECT code FROM generate_series(?::bigint,?::bigint) AS code WHERE NOT EXISTS (
   SELECT 1 FROM gateway_transactions WHERE scope=? AND total_amount=?+code
   AND (status='pending' OR expires_at >= ?::timestamptz - interval '2 minutes')) ORDER BY code LIMIT 1`, r.UniqueCodeMin, r.UniqueCodeMax, t.Scope, t.Amount, t.CreatedAt).Row().Scan(&t.UniqueCode)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrAmountUnavailable
		}
		if err != nil {
			return err
		}
		t.TotalAmount = t.Amount + t.UniqueCode
		t.QRIS, err = build(t.TotalAmount)
		if err != nil {
			return err
		}
		row := recordFromTransaction(t)
		if err = tx.Create(&row).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	return t, created && err == nil, err
}

func (r *Repository) ScanStart(ctx context.Context, scope string, now time.Time) (time.Time, error) {
	var start time.Time
	err := r.DB.WithContext(ctx).Raw(`SELECT LEAST(
  COALESCE((SELECT scanned_until - interval '2 minutes' FROM gateway_scan WHERE scope=?),?::timestamptz-interval '24 hours'),
  COALESCE((SELECT min(created_at)-interval '1 second' FROM gateway_transactions WHERE scope=? AND status='pending'),?))`, scope, now, scope, now).Row().Scan(&start)
	return start, err
}

func (r *Repository) Reconcile(ctx context.Context, scope string, feed []domain.MerchantTransaction, end time.Time, complete bool) error {
	return r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(7382111)").Error; err != nil {
			return err
		}
		for _, m := range feed {
			if m.ID == "" || m.Time.IsZero() {
				continue
			}
			row := merchantTransactionRecord{Scope: scope, ProviderID: m.ID, Amount: m.Amount, Status: m.Status, OccurredAt: m.Time, Raw: json.RawMessage(m.Raw)}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "scope"}, {Name: "provider_transaction_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"amount", "status", "occurred_at", "raw"}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		var pending []transactionRecord
		if err := tx.Where("scope = ? AND status = ?", scope, "pending").Order("created_at").Find(&pending).Error; err != nil {
			return err
		}
		for _, row := range pending {
			t := row.transaction()
			var match merchantTransactionRecord
			err := tx.Where(`scope = ? AND amount = ? AND status = 'completed'
    AND occurred_at >= date_trunc('second',?::timestamptz) AND occurred_at <= ?
    AND NOT EXISTS (SELECT 1 FROM gateway_transactions t WHERE t.scope=gateway_merchant_transactions.scope AND t.provider_transaction_id=gateway_merchant_transactions.provider_transaction_id)`, scope, t.TotalAmount, t.CreatedAt, t.ExpiresAt).
				Order("occurred_at,provider_transaction_id").Take(&match).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil {
				t.Status = "paid"
				t.ProviderID = match.ProviderID
			} else if complete && !t.ExpiresAt.Add(2*time.Minute).After(end) {
				t.Status = "expired"
			} else {
				continue
			}
			t.UpdatedAt = end
			updated := recordFromTransaction(t)
			if err := tx.Model(&transactionRecord{}).Where("transaction_id = ?", t.ID).Updates(map[string]any{"status": t.Status, "provider_transaction_id": updated.ProviderID, "updated_at": end}).Error; err != nil {
				return err
			}
			event := domain.WebhookEvent{EventID: uuid.NewString(), Type: "transaction." + t.Status, Transaction: t}
			payload, err := json.Marshal(event)
			if err != nil {
				return err
			}
			delivery := webhookRecord{ID: event.EventID, TransactionID: t.ID, URL: t.WebhookURL, Payload: json.RawMessage(payload)}
			if err = tx.Create(&delivery).Error; err != nil {
				return err
			}
		}
		if complete {
			checkpoint := scanRecord{Scope: scope, ScannedUntil: end}
			return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "scope"}}, DoUpdates: clause.Assignments(map[string]any{"scanned_until": gorm.Expr("GREATEST(gateway_scan.scanned_until,excluded.scanned_until)")})}).Create(&checkpoint).Error
		}
		return nil
	})
}

func (r *Repository) ClaimDelivery(ctx context.Context) (domain.Delivery, error) {
	var row webhookRecord
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("delivered_at IS NULL AND next_attempt_at <= now()").Order("next_attempt_at").Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrNoDelivery
		}
		if err != nil {
			return err
		}
		row.Attempts++
		return tx.Model(&row).Updates(map[string]any{"attempts": row.Attempts, "next_attempt_at": gorm.Expr("now() + interval '1 minute'")}).Error
	})
	return domain.Delivery{ID: row.ID, URL: row.URL, Payload: []byte(row.Payload), Attempts: row.Attempts}, err
}

func (r *Repository) FinishDelivery(ctx context.Context, d domain.Delivery, message string) error {
	updates := map[string]any{"delivered_at": gorm.Expr("now()"), "last_error": ""}
	if message != "" {
		delay := time.Second * 5 * time.Duration(1<<min(d.Attempts, 9))
		updates = map[string]any{"last_error": message, "next_attempt_at": gorm.Expr("now()+?::interval", fmt.Sprintf("%f seconds", delay.Seconds()))}
	}
	return r.DB.WithContext(ctx).Model(&webhookRecord{}).Where("event_id = ? AND attempts = ?", d.ID, d.Attempts).Updates(updates).Error
}
