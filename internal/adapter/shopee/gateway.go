package shopee

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"pg-shopee/internal/domain"

	"github.com/hirotomasato/paygateme/core"
	"github.com/hirotomasato/paygateme/qris"
	sdk "github.com/hirotomasato/paygateme/shopee"
)

type Gateway struct {
	SessionFile  string
	StaticQRIS   string
	MerchantID   string
	StoreID      string
	MaxPages     int
	PollInterval time.Duration
	mu           sync.Mutex
	fingerprint  [32]byte
	state        domain.LoginState
}

func New(sessionFile, qr, merchantID, storeID string, maxPages int, interval time.Duration) (*Gateway, error) {
	if _, err := qris.StaticToDynamicQris(qr, 1); err != nil {
		return nil, fmt.Errorf("invalid static QRIS: %w", err)
	}
	return &Gateway{SessionFile: sessionFile, StaticQRIS: qr, MerchantID: merchantID, StoreID: storeID, MaxPages: maxPages, PollInterval: interval}, nil
}
func (g *Gateway) QRIS(amount int64) (string, error) {
	return qris.StaticToDynamicQris(g.StaticQRIS, amount)
}

func (g *Gateway) load() (*sdk.Session, *sdk.MerchantCredential, *sdk.HTTPClient, error) {
	data, err := os.ReadFile(g.SessionFile)
	if err != nil {
		g.state.State = "session_missing"
		g.state.Authenticated = false
		return nil, nil, nil, domain.ErrSessionUnavailable
	}
	fingerprint := sha256.Sum256(data)
	if fingerprint != g.fingerprint {
		g.state = domain.LoginState{State: "unverified"}
		g.fingerprint = fingerprint
	}
	var session sdk.Session
	if json.Unmarshal(data, &session) != nil {
		g.state.State = "session_invalid"
		g.state.Authenticated = false
		return nil, nil, nil, domain.ErrSessionUnavailable
	}
	if session.Merchant.ID != g.MerchantID || session.StoreID != g.StoreID {
		g.state.State = "scope_mismatch"
		g.state.Authenticated = false
		return nil, nil, nil, domain.ErrSessionUnavailable
	}
	client := sdk.NewHTTPClient(nil)
	client.Jar().Restore(session.Cookies)
	credential, err := sdk.ReadMerchantCredential(client.Jar())
	if err != nil || credential.AccountID != session.AccountID || (credential.ExpiresAt > 0 && credential.ExpiresAt <= time.Now().UnixMilli()) {
		g.state.State = "session_expired_or_invalid"
		g.state.Authenticated = false
		return nil, nil, nil, domain.ErrSessionUnavailable
	}
	if g.state.State != "auth_required" {
		g.state.Authenticated = true
	}
	return &session, credential, client, nil
}
func (g *Gateway) LoginState() domain.LoginState {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, _, _, _ = g.load()
	state := g.state
	if state.State == "ready" && (state.LastSuccessfulAt == nil || time.Since(*state.LastSuccessfulAt) > max(3*g.PollInterval, time.Minute)) {
		state.State = "stale"
	}
	return state
}
func (g *Gateway) Scan(ctx context.Context, start, end time.Time) ([]domain.MerchantTransaction, bool, error) {
	g.mu.Lock()
	session, credential, client, err := g.load()
	fingerprint := g.fingerprint
	g.mu.Unlock()
	if err != nil {
		return nil, false, err
	}
	feed := sdk.NewTransactionFeed(client, credential.Token, session.Merchant.ID, session.StoreID, sdk.APILocale{}, nil)
	result, err := feed.ListRecent(ctx, core.TransactionFeedQuery{Scope: core.PaymentScope{Provider: sdk.ProviderID, AccountID: g.MerchantID, MerchantID: g.StoreID}, StartTime: start, EndTime: end, MaxPages: g.MaxPages})
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, _, _, loadErr := g.load(); loadErr != nil || fingerprint != g.fingerprint {
		return nil, false, domain.ErrSessionUnavailable
	}
	now := time.Now().UTC()
	g.state.LastCheckedAt = &now
	if err != nil {
		g.state.State = "unreachable"
		if core.IsErrorCode(err, core.CodeAuthRequired) || core.IsErrorCode(err, core.CodeAuthFailed) {
			g.state.State = "auth_required"
			g.state.Authenticated = false
		}
		// Provider responses can contain account data; callers only receive a classified error.
		return nil, false, errors.New("Shopee feed: " + g.state.State)
	}
	g.state.Authenticated = true
	g.state.State = "ready"
	g.state.LastSuccessfulAt = &now
	if result.Truncated {
		g.state.State = "scan_incomplete"
	}
	out := make([]domain.MerchantTransaction, 0, len(result.Transactions))
	for _, t := range result.Transactions {
		raw, err := json.Marshal(t.Raw)
		if err != nil {
			return nil, false, err
		}
		out = append(out, domain.MerchantTransaction{ID: t.ID, Amount: t.Amount, Status: strings.ToLower(t.Status), Time: t.Time, Raw: raw})
	}
	return out, !result.Truncated, nil
}
