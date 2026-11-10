package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"pg-shopee/internal/adapter/postgres"
	"pg-shopee/internal/domain"

	"github.com/google/uuid"
)

func repository(t *testing.T) *postgres.Repository {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	r, err := postgres.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}
func input(scope, id string, amount int64) domain.Transaction {
	now := time.Now().UTC()
	return domain.Transaction{ID: id, Scope: scope, Amount: amount, WebhookURL: "https://merchant.example/hook", Status: "pending", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute)}
}
func qr(n int64) (string, error) { return fmt.Sprint("qr:", n), nil }

func cleanupScope(t *testing.T, r *postgres.Repository, scope string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		err := r.DB.WithContext(ctx).Exec(`DELETE FROM gateway_webhooks WHERE transaction_id IN (SELECT transaction_id FROM gateway_transactions WHERE scope=?)`, scope).Error
		if err != nil {
			t.Error(err)
		}
		for _, table := range []string{"gateway_transactions", "gateway_merchant_transactions", "gateway_scan"} {
			if err = r.DB.WithContext(ctx).Exec("DELETE FROM "+table+" WHERE scope=?", scope).Error; err != nil {
				t.Error(err)
			}
		}
	})
}

func TestPostgresAllocationAndIdempotency(t *testing.T) {
	r := repository(t)
	ctx := context.Background()
	scope := uuid.NewString()
	cleanupScope(t, r, scope)
	var wg sync.WaitGroup
	values := make(chan int64, 30)
	errs := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Go(func() {
			p, _, err := r.Create(ctx, input(scope, uuid.NewString(), 10000), qr)
			if err != nil {
				errs <- err
				return
			}
			values <- p.TotalAmount
		})
	}
	wg.Wait()
	close(values)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	seen := map[int64]bool{}
	for v := range values {
		if seen[v] {
			t.Errorf("duplicate payable amount %d", v)
		}
		seen[v] = true
	}
	if len(seen) != 30 {
		t.Fatalf("allocated %d amounts", len(seen))
	}
	original := input(scope, uuid.NewString(), 20000)
	var ids sync.Map
	for i := 0; i < 10; i++ {
		wg.Go(func() {
			p, _, err := r.Create(ctx, original, qr)
			if err != nil {
				t.Error(err)
				return
			}
			ids.Store(p.TotalAmount, true)
		})
	}
	wg.Wait()
	count := 0
	ids.Range(func(_, _ any) bool { count++; return true })
	if count != 1 {
		t.Fatalf("retry allocated %d payments", count)
	}
	// Different base amounts must also avoid an existing payable amount.
	a, _, err := r.Create(ctx, input(scope, uuid.NewString(), 30000), qr)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := r.Create(ctx, input(scope, uuid.NewString(), 29999), qr)
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := r.Create(ctx, input(scope, uuid.NewString(), 29999), qr)
	if err != nil {
		t.Fatal(err)
	}
	if a.TotalAmount == b.TotalAmount || a.TotalAmount == c.TotalAmount {
		t.Fatal("cross-base amount collision")
	}
	original.Amount++
	if _, _, err := r.Create(ctx, original, qr); !errors.Is(err, domain.ErrTransactionConflict) {
		t.Fatalf("conflict: %v", err)
	}
}

func TestPostgresConfiguredUniqueCodeRange(t *testing.T) {
	r := repository(t)
	r.UniqueCodeMin, r.UniqueCodeMax = 100, 101
	ctx := context.Background()
	scope := uuid.NewString()
	cleanupScope(t, r, scope)
	first := input(scope, uuid.NewString(), 10000)
	for i, in := range []domain.Transaction{first, input(scope, uuid.NewString(), 10000)} {
		got, created, err := r.Create(ctx, in, qr)
		if err != nil || !created || got.UniqueCode != int64(100+i) || got.TotalAmount != in.Amount+got.UniqueCode {
			t.Fatalf("allocation %d: %+v created=%v err=%v", i, got, created, err)
		}
	}
	if _, _, err := r.Create(ctx, input(scope, uuid.NewString(), 10000), qr); !errors.Is(err, domain.ErrAmountUnavailable) {
		t.Fatalf("expected range exhaustion, got %v", err)
	}
	r.UniqueCodeMin, r.UniqueCodeMax = 101, 101
	got, created, err := r.Create(ctx, first, qr)
	if err != nil || created || got.UniqueCode != 100 {
		t.Fatalf("retry after range change: %+v created=%v err=%v", got, created, err)
	}
	if _, _, err := r.Create(ctx, input(scope, uuid.NewString(), 9999), qr); !errors.Is(err, domain.ErrAmountUnavailable) {
		t.Fatalf("expected collision with code outside current range, got %v", err)
	}
	got, created, err = r.Create(ctx, input(scope, uuid.NewString(), 20000), qr)
	if err != nil || !created || got.UniqueCode != 101 {
		t.Fatalf("single-code range: %+v created=%v err=%v", got, created, err)
	}
}

func TestReconcileRestartOutboxAndReplay(t *testing.T) {
	r := repository(t)
	ctx := context.Background()
	scope := uuid.NewString()
	cleanupScope(t, r, scope)
	in := input(scope, uuid.NewString(), 5000)
	in.CreatedAt = in.CreatedAt.Add(-4 * time.Minute)
	in.ExpiresAt = time.Now().Add(-3 * time.Minute)
	p, _, err := r.Create(ctx, in, qr)
	if err != nil {
		t.Fatal(err)
	}
	// A new repository instance recovers the pending payment.
	r2 := repository(t)
	end := time.Now().UTC()
	if err = r2.Reconcile(ctx, scope, nil, end, false); err != nil {
		t.Fatal(err)
	}
	got, err := r2.Get(ctx, p.ID)
	if err != nil || got.Status != "pending" {
		t.Fatalf("incomplete scan expired payment: %+v %v", got, err)
	}
	feed := []domain.MerchantTransaction{
		{ID: uuid.NewString(), Amount: p.TotalAmount, Status: "shopee:1", Time: in.CreatedAt.Add(time.Second), Raw: []byte(`{"status":1}`)},
		{ID: uuid.NewString(), Amount: p.TotalAmount, Status: "completed", Time: in.CreatedAt.Add(2 * time.Second), Raw: []byte(`{"status":3}`)},
	}
	if err = r2.Reconcile(ctx, scope, feed, end, true); err != nil {
		t.Fatal(err)
	}
	got, err = r2.Get(ctx, p.ID)
	if err != nil || got.Status != "paid" || got.ProviderID != feed[1].ID {
		t.Fatalf("settlement: %+v %v", got, err)
	}
	if err = r2.Reconcile(ctx, scope, feed, end, true); err != nil {
		t.Fatal(err)
	}
	var events int
	if err = r.DB.WithContext(ctx).Raw("SELECT count(*) FROM gateway_webhooks WHERE transaction_id=?", p.ID).Row().Scan(&events); err != nil || events != 1 {
		t.Fatalf("outbox count %d: %v", events, err)
	}
	// Claim only this scope's event: previous tests do not produce events.
	d, err := r2.ClaimDelivery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var event domain.WebhookEvent
	if err = json.Unmarshal(d.Payload, &event); err != nil || event.Transaction.Status != "paid" || event.EventID != d.ID {
		t.Fatalf("payload %s %v", d.Payload, err)
	}
	if _, err = r.ClaimDelivery(ctx); !errors.Is(err, domain.ErrNoDelivery) {
		t.Fatalf("lease did not exclude duplicate: %v", err)
	}
	if err = r.FinishDelivery(ctx, d, "HTTP 500"); err != nil {
		t.Fatal(err)
	}
	if err = r.DB.WithContext(ctx).Exec("UPDATE gateway_webhooks SET next_attempt_at=now() WHERE event_id=?", d.ID).Error; err != nil {
		t.Fatal(err)
	}
	retry, err := r.ClaimDelivery(ctx)
	if err != nil || retry.ID != d.ID || retry.Attempts != 2 {
		t.Fatalf("retry %+v %v", retry, err)
	}
	if err = r.FinishDelivery(ctx, d, ""); err != nil {
		t.Fatal(err)
	}
	var stillPending bool
	if err = r.DB.WithContext(ctx).Raw("SELECT delivered_at IS NULL FROM gateway_webhooks WHERE event_id=?", d.ID).Row().Scan(&stillPending); err != nil || !stillPending {
		t.Fatalf("stale worker marked a newer attempt delivered: %v", err)
	}
	if err = r.FinishDelivery(ctx, retry, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ClaimDelivery(ctx); !errors.Is(err, domain.ErrNoDelivery) {
		t.Fatalf("delivered event retried: %v", err)
	}
	// Unknown/failed status must never count as paid; a complete scan expires it.
	in = input(scope, uuid.NewString(), 9000)
	in.CreatedAt = in.CreatedAt.Add(-4 * time.Minute)
	in.ExpiresAt = end.Add(-3 * time.Minute)
	p, _, err = r.Create(ctx, in, qr)
	if err != nil {
		t.Fatal(err)
	}
	feed = []domain.MerchantTransaction{{ID: uuid.NewString(), Amount: p.TotalAmount, Status: "", Time: in.CreatedAt, Raw: []byte(`{}`)}}
	if err = r.Reconcile(ctx, scope, feed, end, true); err != nil {
		t.Fatal(err)
	}
	got, err = r.Get(ctx, p.ID)
	if err != nil || got.Status != "expired" {
		t.Fatalf("unknown status settled: %+v %v", got, err)
	}
}

func TestGORMReconciliationRollback(t *testing.T) {
	r := repository(t)
	ctx := context.Background()
	scope := uuid.NewString()
	cleanupScope(t, r, scope)
	in := input(scope, uuid.NewString(), 7000)
	p, _, err := r.Create(ctx, in, qr)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := r.Get(ctx, p.ID)
	if err != nil || stored.CreatedAt.Sub(in.CreatedAt).Abs() >= time.Microsecond || stored.UpdatedAt.Sub(in.UpdatedAt).Abs() >= time.Microsecond {
		t.Fatalf("GORM changed payment timestamps: %+v %v", stored, err)
	}
	// Force the final event insert to fail after the payment update.
	err = r.DB.WithContext(ctx).Exec(`INSERT INTO gateway_webhooks(event_id,transaction_id,url,payload) VALUES (?,?,?,?::jsonb)`, uuid.NewString(), p.ID, p.WebhookURL, `{}`).Error
	if err != nil {
		t.Fatal(err)
	}
	providerID := uuid.NewString()
	feed := []domain.MerchantTransaction{{ID: providerID, Amount: p.TotalAmount, Status: "completed", Time: p.CreatedAt.Add(time.Second), Raw: []byte(`{"status":3}`)}}
	end := time.Now().Add(2 * time.Second)
	if err = r.Reconcile(ctx, scope, feed, end, true); err == nil {
		t.Fatal("expected conflicting outbox event to roll back reconciliation")
	}
	stored, err = r.Get(ctx, p.ID)
	if err != nil || stored.Status != "pending" || stored.ProviderID != "" {
		t.Fatalf("payment escaped rollback: %+v %v", stored, err)
	}
	var imported int
	err = r.DB.WithContext(ctx).Raw("SELECT count(*) FROM gateway_merchant_transactions WHERE scope=?", scope).Row().Scan(&imported)
	if err != nil || imported != 0 {
		t.Fatalf("feed escaped rollback: count=%d %v", imported, err)
	}
	if err = r.DB.Exec("DELETE FROM gateway_webhooks WHERE transaction_id=?", p.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err = r.Reconcile(ctx, scope, feed, end, true); err != nil {
		t.Fatal(err)
	}
	start, err := r.ScanStart(ctx, scope, end.Add(time.Hour))
	if err != nil || start.Sub(end.Add(-2*time.Minute)).Abs() >= time.Microsecond {
		t.Fatalf("persisted checkpoint: %v %v", start, err)
	}
}

func TestConcurrentWebhookClaims(t *testing.T) {
	r := repository(t)
	ctx := context.Background()
	scope := uuid.NewString()
	cleanupScope(t, r, scope)
	var feed []domain.MerchantTransaction
	for i := 0; i < 6; i++ {
		p, _, err := r.Create(ctx, input(scope, uuid.NewString(), 8000), qr)
		if err != nil {
			t.Fatal(err)
		}
		feed = append(feed, domain.MerchantTransaction{ID: uuid.NewString(), Amount: p.TotalAmount, Status: "completed", Time: p.CreatedAt.Add(time.Second), Raw: []byte(`{}`)})
	}
	if err := r.Reconcile(ctx, scope, feed, time.Now().Add(2*time.Second), true); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claimed := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			d, err := r.ClaimDelivery(ctx)
			if errors.Is(err, domain.ErrNoDelivery) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			claimed <- d.ID
		})
	}
	wg.Wait()
	close(claimed)
	seen := map[string]bool{}
	for id := range claimed {
		if seen[id] {
			t.Errorf("event %s leased to multiple workers", id)
		}
		seen[id] = true
	}
	if len(seen) != 6 {
		t.Fatalf("leased %d events, want 6", len(seen))
	}
}
