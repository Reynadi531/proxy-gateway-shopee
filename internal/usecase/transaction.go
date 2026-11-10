package usecase

import (
	"context"
	"net/url"
	"strings"
	"time"
	"unicode"

	"pg-shopee/internal/domain"
)

type TransactionRepository interface {
	Create(context.Context, domain.Transaction, func(int64) (string, error)) (domain.Transaction, bool, error)
	Get(context.Context, string) (domain.Transaction, error)
	ScanStart(context.Context, string, time.Time) (time.Time, error)
	Reconcile(context.Context, string, []domain.MerchantTransaction, time.Time, bool) error
	ClaimDelivery(context.Context) (domain.Delivery, error)
	FinishDelivery(context.Context, domain.Delivery, string) error
}

type ShopeeGateway interface {
	LoginState() domain.LoginState
	QRIS(int64) (string, error)
	Scan(context.Context, time.Time, time.Time) ([]domain.MerchantTransaction, bool, error)
}

type TransactionService struct {
	Repository     TransactionRepository
	Gateway        ShopeeGateway
	Scope          string
	Expiry         time.Duration
	AllowedOrigins map[string]bool
}

type CreateTransactionInput struct {
	TransactionID string `json:"transaction_id"`
	Amount        int64  `json:"amount"`
	WebhookURL    string `json:"webhook_url"`
}

func (s *TransactionService) Create(ctx context.Context, in CreateTransactionInput) (domain.Transaction, bool, error) {
	if strings.TrimSpace(in.TransactionID) != in.TransactionID || len(in.TransactionID) == 0 || len(in.TransactionID) > 128 || strings.ContainsFunc(in.TransactionID, unicode.IsControl) || in.Amount < 1 || in.Amount > 999999999 || !s.validWebhook(in.WebhookURL) {
		return domain.Transaction{}, false, domain.ErrInvalidTransaction
	}
	// Resolve retries before checking the session so an outage does not hide existing payments.
	existing, err := s.Repository.Get(ctx, in.TransactionID)
	if err == nil {
		if existing.Amount != in.Amount || existing.WebhookURL != in.WebhookURL || existing.Scope != s.Scope {
			return domain.Transaction{}, false, domain.ErrTransactionConflict
		}
		return existing, false, nil
	}
	if err != domain.ErrTransactionNotFound {
		return domain.Transaction{}, false, err
	}
	state := s.Gateway.LoginState()
	if !state.Authenticated || state.State != "ready" {
		return domain.Transaction{}, false, domain.ErrSessionUnavailable
	}
	now := time.Now().UTC()
	return s.Repository.Create(ctx, domain.Transaction{ID: in.TransactionID, Scope: s.Scope, Amount: in.Amount, WebhookURL: in.WebhookURL, Status: "pending", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(s.Expiry)}, s.Gateway.QRIS)
}

func (s *TransactionService) validWebhook(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil && u.Fragment == "" && (u.Scheme == "https" || u.Scheme == "http") && s.AllowedOrigins[u.Scheme+"://"+u.Host]
}
