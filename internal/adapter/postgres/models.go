package postgres

import (
	"encoding/json"
	"time"

	"pg-shopee/internal/domain"
)

type transactionRecord struct {
	ID          string `gorm:"column:transaction_id;primaryKey"`
	Scope       string
	Amount      int64
	UniqueCode  int64
	TotalAmount int64
	QRIS        string `gorm:"column:qris"`
	WebhookURL  string `gorm:"column:webhook_url"`
	Status      string
	ProviderID  *string   `gorm:"column:provider_transaction_id"`
	CreatedAt   time.Time `gorm:"autoCreateTime:false"`
	ExpiresAt   time.Time
	UpdatedAt   time.Time `gorm:"autoUpdateTime:false"`
}

func (transactionRecord) TableName() string { return "gateway_transactions" }

func recordFromTransaction(t domain.Transaction) transactionRecord {
	r := transactionRecord{ID: t.ID, Scope: t.Scope, Amount: t.Amount, UniqueCode: t.UniqueCode, TotalAmount: t.TotalAmount, QRIS: t.QRIS, WebhookURL: t.WebhookURL, Status: t.Status, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, UpdatedAt: t.UpdatedAt}
	if t.ProviderID != "" {
		r.ProviderID = &t.ProviderID
	}
	return r
}

func (r transactionRecord) transaction() domain.Transaction {
	t := domain.Transaction{ID: r.ID, Scope: r.Scope, Amount: r.Amount, UniqueCode: r.UniqueCode, TotalAmount: r.TotalAmount, QRIS: r.QRIS, WebhookURL: r.WebhookURL, Status: r.Status, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, UpdatedAt: r.UpdatedAt}
	if r.ProviderID != nil {
		t.ProviderID = *r.ProviderID
	}
	return t
}

type merchantTransactionRecord struct {
	Scope      string `gorm:"primaryKey"`
	ProviderID string `gorm:"column:provider_transaction_id;primaryKey"`
	Amount     int64
	Status     string
	OccurredAt time.Time
	Raw        json.RawMessage `gorm:"type:jsonb"`
}

func (merchantTransactionRecord) TableName() string { return "gateway_merchant_transactions" }

type scanRecord struct {
	Scope        string `gorm:"primaryKey"`
	ScannedUntil time.Time
}

func (scanRecord) TableName() string { return "gateway_scan" }

type webhookRecord struct {
	ID            string `gorm:"column:event_id;primaryKey"`
	TransactionID string
	URL           string
	Payload       json.RawMessage `gorm:"type:jsonb"`
	Attempts      int
	NextAttemptAt time.Time `gorm:"default:now()"`
	DeliveredAt   *time.Time
	LastError     string
}

func (webhookRecord) TableName() string { return "gateway_webhooks" }
