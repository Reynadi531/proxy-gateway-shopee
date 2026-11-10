package usecase

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"pg-shopee/internal/domain"
)

type WebhookSender interface {
	Send(context.Context, domain.Delivery) error
}

func (s *TransactionService) Poll(ctx context.Context) error {
	end := time.Now().UTC()
	start, err := s.Repository.ScanStart(ctx, s.Scope, end)
	if err != nil {
		return err
	}
	feed, complete, err := s.Gateway.Scan(ctx, start, end)
	if err != nil {
		return err
	}
	return s.Repository.Reconcile(ctx, s.Scope, feed, end, complete)
}
func (s *TransactionService) RunPoller(ctx context.Context, interval time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		scanCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := s.Poll(scanCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Error("payment scan failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *TransactionService) RunWebhooks(ctx context.Context, sender WebhookSender, log *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for i := 0; i < 20; i++ {
			if ctx.Err() != nil {
				return
			}
			delivery, err := s.Repository.ClaimDelivery(ctx)
			if err != nil {
				if !errors.Is(err, domain.ErrNoDelivery) && ctx.Err() == nil {
					log.Error("webhook claim failed", "error", err)
				}
				break
			}
			err = sender.Send(ctx, delivery)
			message := ""
			if err != nil {
				message = err.Error()
			}
			if err = s.Repository.FinishDelivery(ctx, delivery, message); err != nil && ctx.Err() == nil {
				log.Error("webhook result persistence failed", "event_id", delivery.ID, "error", err)
			}
		}
	}
}
