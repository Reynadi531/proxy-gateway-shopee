package shopee

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"pg-shopee/internal/domain"

	"github.com/hirotomasato/paygateme/core"
	sdk "github.com/hirotomasato/paygateme/shopee"
)

const (
	defaultOTPTTL  = 10 * time.Minute
	maxOTPAttempts = 5
	maxMessageLen  = 200
)

// SessionManager writes the session file the Gateway reads. It renews the
// merchant token without an OTP while Shopee keeps the account session alive,
// and runs the OTP login when it does not.
type SessionManager struct {
	SessionFile  string
	MerchantID   string
	StoreID      string
	DeviceReport string
	OTPTTL       time.Duration
	Now          func() time.Time

	// mu serializes every operation that talks to Shopee or writes the file,
	// so a renewal cannot overwrite a session produced by a concurrent login.
	mu      sync.Mutex
	pending *pendingLogin
}

type pendingLogin struct {
	provider  *sdk.Provider
	challenge sdk.OtpChallenge
	expiresAt time.Time
	attempts  int
}

func NewSessionManager(sessionFile, merchantID, storeID string) *SessionManager {
	return &SessionManager{SessionFile: sessionFile, MerchantID: merchantID, StoreID: storeID, DeviceReport: sdk.DeviceRiskBlob, OTPTTL: defaultOTPTTL, Now: time.Now}
}

// Status describes the session file.
func (m *SessionManager) Status() (domain.SessionInfo, error) {
	_, session, err := m.read()
	if err != nil {
		return domain.SessionInfo{}, err
	}
	if err = m.checkScope(session); err != nil {
		return describe(session), err
	}
	return describe(session), nil
}

// Refresh renews the merchant token without an OTP.
func (m *SessionManager) Refresh(ctx context.Context) (domain.SessionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, session, err := m.read()
	if err != nil {
		return domain.SessionInfo{}, err
	}
	if err = m.checkScope(session); err != nil {
		return domain.SessionInfo{}, err
	}
	if session.SwitchCredential == nil {
		return domain.SessionInfo{}, fmt.Errorf("%w: session has no renewal credential", domain.ErrReloginRequired)
	}
	provider := sdk.NewProvider(sdk.ProviderConfig{Session: session, DeviceReport: m.DeviceReport})
	renewed, err := provider.RefreshSession(ctx)
	if err != nil {
		// A failed merchant-token exchange during renewal also needs a fresh login.
		return domain.SessionInfo{}, classify(err, true)
	}
	if err = m.checkScope(renewed); err != nil {
		return domain.SessionInfo{}, err
	}
	if err = m.write(data, renewed); err != nil {
		return domain.SessionInfo{}, err
	}
	return describe(renewed), nil
}

// RequestOTP starts an OTP login, replacing any login already in progress.
func (m *SessionManager) RequestOTP(ctx context.Context, phone, password string) (domain.OTPRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending = nil
	provider := sdk.NewProvider(sdk.ProviderConfig{DeviceReport: m.DeviceReport})
	challenge, err := provider.RequestOtp(ctx, phone, sdk.OtpRequestOptions{Password: password})
	if err != nil {
		return domain.OTPRequest{}, classify(err, false)
	}
	m.pending = &pendingLogin{provider: provider, challenge: *challenge, expiresAt: m.Now().Add(m.OTPTTL)}
	return domain.OTPRequest{Channel: challenge.Channel, ExpiresAt: m.pending.expiresAt}, nil
}

// VerifyOTP completes the pending login and replaces the session file.
func (m *SessionManager) VerifyOTP(ctx context.Context, otp string) (domain.SessionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.pending
	if p == nil || !m.Now().Before(p.expiresAt) {
		m.pending = nil
		return domain.SessionInfo{}, domain.ErrNoPendingLogin
	}
	p.attempts++
	if p.attempts >= maxOTPAttempts {
		m.pending = nil
	}
	outcome, err := p.provider.LoginWithOtp(ctx, sdk.LoginWithOtpInput{Challenge: p.challenge, OTP: otp, MerchantID: m.MerchantID, StoreID: m.StoreID})
	if err != nil {
		return domain.SessionInfo{}, classify(err, false)
	}
	m.pending = nil
	if outcome.Status != sdk.LoginComplete || outcome.Session == nil {
		return domain.SessionInfo{}, errors.New("Shopee login did not complete for the configured merchant")
	}
	if err = m.checkScope(outcome.Session); err != nil {
		return domain.SessionInfo{}, err
	}
	if err = m.write(nil, outcome.Session); err != nil {
		return domain.SessionInfo{}, err
	}
	return describe(outcome.Session), nil
}

// CancelLogin discards the pending OTP login, reporting whether one existed.
func (m *SessionManager) CancelLogin() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	had := m.pending != nil
	m.pending = nil
	return had
}

func (m *SessionManager) read() ([]byte, *sdk.Session, error) {
	data, err := os.ReadFile(m.SessionFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("%w: session file is missing", domain.ErrReloginRequired)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read session file: %w", err)
	}
	var session sdk.Session
	if json.Unmarshal(data, &session) != nil {
		return nil, nil, fmt.Errorf("%w: session file is not valid JSON", domain.ErrReloginRequired)
	}
	return data, &session, nil
}

func (m *SessionManager) checkScope(session *sdk.Session) error {
	if session.Merchant.ID != m.MerchantID || session.StoreID != m.StoreID {
		return fmt.Errorf("%w: session belongs to a different merchant or store than SHOPEE_MERCHANT_ID/SHOPEE_STORE_ID", domain.ErrReloginRequired)
	}
	return nil
}

// write atomically replaces the session file. With expected set, the write is
// abandoned if the file no longer holds those bytes, so a session replaced by
// the operator during a renewal is kept.
func (m *SessionManager) write(expected []byte, session *sdk.Session) error {
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	if expected != nil {
		current, err := os.ReadFile(m.SessionFile)
		if err != nil || !bytes.Equal(current, expected) {
			return domain.ErrSessionReplaced
		}
	}
	return writeFileAtomic(m.SessionFile, append(data, '\n'))
}

func writeFileAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write session file: %w", err)
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}
	if err = os.Rename(name, path); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}
	if d, dirErr := os.Open(dir); dirErr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func describe(session *sdk.Session) domain.SessionInfo {
	info := domain.SessionInfo{MerchantID: session.Merchant.ID, MerchantName: session.Merchant.Name, StoreID: session.StoreID, Renewable: session.SwitchCredential != nil}
	// The jar hides expired cookies, so read the JWT with expiry cleared and
	// apply the cookie expiry separately.
	cookies := make([]sdk.Cookie, len(session.Cookies))
	var expiry int64
	for i, c := range session.Cookies {
		if c.Name == sdk.LiveTokenCookie && c.Expires > 0 {
			expiry = minPositive(expiry, c.Expires)
		}
		c.Expires = 0
		cookies[i] = c
	}
	jar := sdk.NewCookieJar()
	jar.Restore(cookies)
	credential, err := sdk.ReadMerchantCredential(jar)
	if err != nil || credential.AccountID != session.AccountID {
		return info
	}
	info.TokenPresent = true
	expiry = minPositive(expiry, credential.ExpiresAt)
	if expiry > 0 {
		info.ExpiresAt = time.UnixMilli(expiry)
	}
	return info
}

func minPositive(a, b int64) int64 {
	if a <= 0 || (b > 0 && b < a) {
		return b
	}
	return a
}

// classify reduces SDK errors to their fixed message and code. Causes are
// dropped: transport errors carry request URLs with login nonces.
func classify(err error, authFailedNeedsLogin bool) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("Shopee request timed out")
	}
	var base *core.BaseError
	if !core.AsBaseError(err, &base) {
		return errors.New("Shopee request failed")
	}
	message := base.Message
	if len(message) > maxMessageLen {
		message = message[:maxMessageLen]
	}
	if base.Code == core.CodeAuthRequired || (authFailedNeedsLogin && base.Code == core.CodeAuthFailed) {
		return fmt.Errorf("%w: %s", domain.ErrReloginRequired, message)
	}
	return fmt.Errorf("%s: %s", base.Code, message)
}
