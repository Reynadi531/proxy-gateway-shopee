package domain

import (
	"errors"
	"time"
)

var (
	ErrInvalidTransaction  = errors.New("transaction_id, positive amount, and an allowed webhook_url are required")
	ErrTransactionNotFound = errors.New("transaction not found")
	ErrTransactionConflict = errors.New("transaction_id already exists with different details")
	ErrAmountUnavailable   = errors.New("no unique amount available; retry later")
	ErrSessionUnavailable  = errors.New("Shopee session is unavailable")
	ErrNoDelivery          = errors.New("no webhook delivery due")
)

type Transaction struct {
	ID          string    `json:"transaction_id"`
	Scope       string    `json:"-"`
	Amount      int64     `json:"amount"`
	UniqueCode  int64     `json:"unique_code"`
	TotalAmount int64     `json:"total_amount"`
	QRIS        string    `json:"qris"`
	WebhookURL  string    `json:"-"`
	Status      string    `json:"status"`
	ProviderID  string    `json:"provider_transaction_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type LoginState struct {
	Authenticated    bool       `json:"authenticated"`
	State            string     `json:"state"`
	LastCheckedAt    *time.Time `json:"last_checked_at"`
	LastSuccessfulAt *time.Time `json:"last_successful_at"`
}

type MerchantTransaction struct {
	ID     string
	Amount int64
	Status string
	Time   time.Time
	Raw    []byte
}

type WebhookEvent struct {
	EventID     string      `json:"event_id"`
	Type        string      `json:"type"`
	Transaction Transaction `json:"transaction"`
}

type Delivery struct {
	ID       string
	URL      string
	Payload  []byte
	Attempts int
}
