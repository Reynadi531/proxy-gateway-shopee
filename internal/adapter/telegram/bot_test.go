package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"pg-shopee/internal/domain"
)

const admin = int64(77)

type sent struct {
	chatID int64
	text   string
	silent bool
}

type fakeAPI struct {
	mu      sync.Mutex
	sent    []sent
	deleted []int64
}

func (f *fakeAPI) GetUpdates(context.Context, int64, time.Duration) ([]Update, error) {
	return nil, nil
}

func (f *fakeAPI) SendMessage(_ context.Context, chatID int64, text string, silent bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sent{chatID, text, silent})
	return nil
}

func (f *fakeAPI) DeleteMessage(_ context.Context, _ int64, messageID int64) error {
	f.deleted = append(f.deleted, messageID)
	return nil
}

func (f *fakeAPI) last() string {
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1].text
}

type fakeSessions struct {
	info         domain.SessionInfo
	statusErr    error
	renewed      domain.SessionInfo
	refreshErr   error
	refreshCalls int
	otpErr       error
	verifyErr    error
	phone        string
	password     string
	otp          string
	requests     int
	cancels      int
}

func (f *fakeSessions) Status() (domain.SessionInfo, error) { return f.info, f.statusErr }

func (f *fakeSessions) Refresh(context.Context) (domain.SessionInfo, error) {
	f.refreshCalls++
	return f.renewed, f.refreshErr
}

func (f *fakeSessions) RequestOTP(_ context.Context, phone, password string) (domain.OTPRequest, error) {
	f.requests++
	f.phone, f.password = phone, password
	return domain.OTPRequest{Channel: 3, ExpiresAt: time.Unix(1_000_600, 0)}, f.otpErr
}

func (f *fakeSessions) VerifyOTP(_ context.Context, otp string) (domain.SessionInfo, error) {
	f.otp = otp
	return domain.SessionInfo{MerchantID: "100", MerchantName: "Toko", StoreID: "200", TokenPresent: true, ExpiresAt: time.Unix(1_086_400, 0)}, f.verifyErr
}

func (f *fakeSessions) CancelLogin() bool { f.cancels++; return false }

type fakeGateway struct{ state domain.LoginState }

func (f *fakeGateway) LoginState() domain.LoginState { return f.state }

type harness struct {
	bot      *Bot
	api      *fakeAPI
	sessions *fakeSessions
	gateway  *fakeGateway
	now      time.Time
	nextID   int64
}

func newHarness(cfg Config) *harness {
	h := &harness{api: &fakeAPI{}, sessions: &fakeSessions{}, gateway: &fakeGateway{state: domain.LoginState{Authenticated: true, State: "ready"}}, now: time.Unix(1_000_000, 0)}
	if cfg.Admins == nil {
		cfg.Admins = map[int64]bool{admin: true}
	}
	if cfg.RefreshBefore == 0 {
		cfg.RefreshBefore = 2 * time.Hour
	}
	h.bot = New(h.api, h.sessions, h.gateway, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.bot.now = func() time.Time { return h.now }
	h.sessions.info = domain.SessionInfo{MerchantID: "100", StoreID: "200", TokenPresent: true, Renewable: true, ExpiresAt: h.now.Add(10 * time.Hour)}
	return h
}

func (h *harness) send(from int64, chatType, text string) int64 {
	h.nextID++
	h.bot.HandleMessage(context.Background(), Message{MessageID: h.nextID, From: &User{ID: from}, Chat: Chat{ID: from, Type: chatType}, Date: h.now.Unix(), Text: text})
	return h.nextID
}

func TestAccessControl(t *testing.T) {
	h := newHarness(Config{})
	h.send(99, "private", "/login")
	if h.api.last() != "Not authorized. Your Telegram user ID is 99." || h.bot.conv != nil {
		t.Fatalf("stranger: %q", h.api.last())
	}
	h.send(admin, "group", "/login")
	if len(h.api.sent) != 1 || h.bot.conv != nil {
		t.Fatal("group message was handled")
	}
	h.nextID++
	h.bot.HandleMessage(context.Background(), Message{MessageID: h.nextID, From: &User{ID: admin}, Chat: Chat{ID: admin, Type: "private"}, Date: h.now.Add(-10 * time.Minute).Unix(), Text: "/login"})
	if len(h.api.sent) != 1 || h.bot.conv != nil {
		t.Fatal("stale message was handled")
	}
	h.send(admin, "private", "/status@shopee_bot")
	if !strings.Contains(h.api.last(), "Gateway: ready") || !strings.Contains(h.api.last(), "Renewal without OTP: available") {
		t.Fatalf("status: %q", h.api.last())
	}
}

func TestOTPLoginConversation(t *testing.T) {
	h := newHarness(Config{})
	h.send(admin, "private", "/login")
	if !strings.Contains(h.api.last(), "phone number") {
		t.Fatalf("phone prompt: %q", h.api.last())
	}
	phoneMsg := h.send(admin, "private", "081234567890")
	if !strings.Contains(h.api.last(), "password") {
		t.Fatalf("password prompt: %q", h.api.last())
	}
	passwordMsg := h.send(admin, "private", "hunter2")
	if h.sessions.phone != "081234567890" || h.sessions.password != "hunter2" {
		t.Fatalf("otp request: %+v", h.sessions)
	}
	if !strings.Contains(h.api.last(), "WhatsApp") {
		t.Fatalf("otp prompt: %q", h.api.last())
	}
	h.send(admin, "private", "12ab")
	if !strings.Contains(h.api.last(), "4 to 10 digits") || h.sessions.otp != "" {
		t.Fatalf("invalid otp: %q", h.api.last())
	}
	h.sessions.verifyErr = errors.New("API_ERROR: error_otp_invalid")
	h.send(admin, "private", "000000")
	if !strings.Contains(h.api.last(), "error_otp_invalid") || h.bot.conv == nil {
		t.Fatalf("wrong otp: %q", h.api.last())
	}
	h.sessions.verifyErr = nil
	otpMsg := h.send(admin, "private", "123456")
	if h.sessions.otp != "123456" || h.bot.conv != nil || !strings.Contains(h.api.last(), "Logged in as Toko (100), store 200") {
		t.Fatalf("login: %q", h.api.last())
	}
	for _, id := range []int64{phoneMsg, passwordMsg, otpMsg} {
		found := false
		for _, deleted := range h.api.deleted {
			found = found || deleted == id
		}
		if !found {
			t.Errorf("message %d holding credentials was not deleted (deleted %v)", id, h.api.deleted)
		}
	}
	h.send(admin, "private", "123456")
	if h.api.last() != "Send /help for commands." {
		t.Fatalf("input after login: %q", h.api.last())
	}
}

func TestOTPLoginWithConfiguredCredentials(t *testing.T) {
	h := newHarness(Config{Phone: "0812", Password: "secret"})
	h.send(admin, "private", "/login")
	if h.sessions.requests != 1 || h.sessions.phone != "0812" || h.sessions.password != "secret" || h.bot.conv.step != stepOTP {
		t.Fatalf("configured login: %+v", h.sessions)
	}
	h.send(admin, "private", "/cancel")
	if h.bot.conv != nil || h.api.last() != "Login cancelled." {
		t.Fatalf("cancel: %q", h.api.last())
	}
}

func TestOTPLoginSkipPasswordAndFailures(t *testing.T) {
	h := newHarness(Config{Phone: "0812"})
	h.send(admin, "private", "/skip")
	if h.api.last() != "Nothing to skip." {
		t.Fatalf("skip outside login: %q", h.api.last())
	}
	h.send(admin, "private", "/login")
	h.sessions.otpErr = errors.New("CONFIG_INVALID: This Shopee account is password-protected")
	h.send(admin, "private", "/skip")
	if h.sessions.password != "" || h.bot.conv != nil || !strings.Contains(h.api.last(), "password-protected") {
		t.Fatalf("otp request failure: %q", h.api.last())
	}

	h.sessions.otpErr = nil
	h.send(admin, "private", "/login")
	h.send(admin, "private", "/skip")
	h.sessions.verifyErr = domain.ErrNoPendingLogin
	h.send(admin, "private", "123456")
	if h.bot.conv != nil || !strings.Contains(h.api.last(), "expired or ran out of attempts") {
		t.Fatalf("expired otp: %q", h.api.last())
	}

	h.send(admin, "private", "/login")
	h.now = h.now.Add(conversationTTL + time.Second)
	h.send(admin, "private", "pw")
	if h.bot.conv != nil || !strings.Contains(h.api.last(), "timed out") {
		t.Fatalf("timeout: %q", h.api.last())
	}
}

func TestRefreshCommand(t *testing.T) {
	h := newHarness(Config{})
	h.sessions.renewed = domain.SessionInfo{TokenPresent: true, ExpiresAt: h.now.Add(24 * time.Hour)}
	h.send(admin, "private", "/refresh")
	if h.sessions.refreshCalls != 1 || !strings.Contains(h.api.last(), "Session renewed") || !strings.Contains(h.api.last(), "in 24h0m") {
		t.Fatalf("refresh: %q", h.api.last())
	}
	h.sessions.refreshErr = fmt.Errorf("%w: expired", domain.ErrReloginRequired)
	h.send(admin, "private", "/refresh")
	if !strings.Contains(h.api.last(), "Send /login") {
		t.Fatalf("refresh failure: %q", h.api.last())
	}
}

func TestWatchdogRenewsBeforeExpiry(t *testing.T) {
	h := newHarness(Config{})
	ctx := context.Background()
	h.bot.CheckSession(ctx)
	if h.sessions.refreshCalls != 0 || len(h.api.sent) != 0 {
		t.Fatal("healthy session was renewed or alerted")
	}

	h.sessions.info.ExpiresAt = h.now.Add(time.Hour)
	h.sessions.renewed = domain.SessionInfo{TokenPresent: true, ExpiresAt: h.now.Add(24 * time.Hour)}
	h.bot.CheckSession(ctx)
	if h.sessions.refreshCalls != 1 || len(h.api.sent) != 1 || !h.api.sent[0].silent || !strings.Contains(h.api.sent[0].text, "renewed without OTP") {
		t.Fatalf("renewal: %+v", h.api.sent)
	}
	// Renewal attempts are rate limited even if the token still looks due.
	h.now = h.now.Add(time.Minute)
	h.bot.CheckSession(ctx)
	if h.sessions.refreshCalls != 1 {
		t.Fatal("renewal retried immediately")
	}

	// Transient failures stay quiet while the token works, then alert once it expires.
	h.now = h.now.Add(refreshRetry)
	h.sessions.refreshErr = errors.New("Shopee request failed")
	h.bot.CheckSession(ctx)
	if h.sessions.refreshCalls != 2 || len(h.api.sent) != 1 {
		t.Fatalf("transient failure alerted early: %+v", h.api.sent)
	}
	h.now = h.sessions.info.ExpiresAt
	h.bot.CheckSession(ctx)
	if h.sessions.refreshCalls != 3 || len(h.api.sent) != 2 || h.api.sent[1].silent || !strings.Contains(h.api.sent[1].text, "Automatic renewal failed: Shopee request failed") {
		t.Fatalf("expired alert: %+v", h.api.sent)
	}
}

func TestWatchdogAlertsWhenRenewedSessionIsRejected(t *testing.T) {
	h := newHarness(Config{})
	ctx := context.Background()
	h.gateway.state = domain.LoginState{State: "auth_required"}
	h.sessions.renewed = domain.SessionInfo{TokenPresent: true, ExpiresAt: h.now.Add(24 * time.Hour)}
	h.bot.CheckSession(ctx)
	if h.sessions.refreshCalls != 1 || len(h.api.sent) != 1 || !h.api.sent[0].silent {
		t.Fatalf("renewal: %+v", h.api.sent)
	}
	h.now = h.now.Add(time.Minute)
	h.bot.CheckSession(ctx)
	if h.sessions.refreshCalls != 1 || len(h.api.sent) != 2 || !strings.Contains(h.api.sent[1].text, "still rejects the session") {
		t.Fatalf("rejected after renewal: %+v", h.api.sent)
	}
}

func TestWatchdogAlertsWhenLoginNeeded(t *testing.T) {
	h := newHarness(Config{Admins: map[int64]bool{admin: true, 88: true}})
	ctx := context.Background()
	h.gateway.state = domain.LoginState{State: "auth_required"}
	h.sessions.refreshErr = fmt.Errorf("%w: The Shopee account session has expired", domain.ErrReloginRequired)
	h.bot.CheckSession(ctx)
	if h.sessions.refreshCalls != 1 || len(h.api.sent) != 2 || !strings.Contains(h.api.sent[0].text, "Send /login") {
		t.Fatalf("rejected session: %+v", h.api.sent)
	}
	h.now = h.now.Add(30 * time.Minute)
	h.bot.CheckSession(ctx)
	if len(h.api.sent) != 2 {
		t.Fatal("alert repeated before the reminder interval")
	}
	h.now = h.now.Add(31 * time.Minute)
	h.bot.CheckSession(ctx)
	if len(h.api.sent) != 4 {
		t.Fatalf("no reminder: %d messages", len(h.api.sent))
	}

	// Recovery resets the alert so the next problem is reported at once.
	h.gateway.state = domain.LoginState{Authenticated: true, State: "ready"}
	h.bot.CheckSession(ctx)
	h.sessions.statusErr = fmt.Errorf("%w: session file is missing", domain.ErrReloginRequired)
	h.bot.CheckSession(ctx)
	if len(h.api.sent) != 6 || !strings.Contains(h.api.sent[5].text, "session file is missing") {
		t.Fatalf("missing session: %+v", h.api.sent)
	}

	h = newHarness(Config{})
	h.sessions.info = domain.SessionInfo{MerchantID: "100", StoreID: "200", TokenPresent: true, ExpiresAt: h.now.Add(-time.Minute)}
	h.bot.CheckSession(ctx)
	if h.sessions.refreshCalls != 0 || len(h.api.sent) != 1 || !strings.Contains(h.api.sent[0].text, "cannot be renewed without an OTP") {
		t.Fatalf("non-renewable expired session: %+v", h.api.sent)
	}
}

type scriptedAPI struct {
	fakeAPI
	offsets []int64
	batches [][]Update
	cancel  context.CancelFunc
}

func (s *scriptedAPI) GetUpdates(ctx context.Context, offset int64, _ time.Duration) ([]Update, error) {
	s.offsets = append(s.offsets, offset)
	if len(s.batches) == 0 {
		s.cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	batch := s.batches[0]
	s.batches = s.batches[1:]
	if batch == nil {
		return nil, &APIError{Method: "getUpdates", Code: 502, Description: "Bad Gateway"}
	}
	return batch, nil
}

func TestRunPollsUpdatesAndWatchesSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now()
	message := func(id int64, text string) *Message {
		return &Message{MessageID: id, From: &User{ID: admin}, Chat: Chat{ID: admin, Type: "private"}, Date: now.Unix(), Text: text}
	}
	api := &scriptedAPI{cancel: cancel, batches: [][]Update{{{UpdateID: 10, Message: message(1, "/help")}, {UpdateID: 11}}, nil, {{UpdateID: 12, Message: message(2, "/status")}}}}
	sessions := &fakeSessions{info: domain.SessionInfo{TokenPresent: true, Renewable: true, ExpiresAt: now.Add(time.Minute)}}
	bot := New(api, sessions, &fakeGateway{state: domain.LoginState{Authenticated: true, State: "ready"}}, Config{Admins: map[int64]bool{admin: true}, RefreshBefore: time.Hour, CheckInterval: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	bot.retryWait = 10 * time.Millisecond
	bot.Run(ctx)
	if fmt.Sprint(api.offsets) != "[0 12 12 13]" {
		t.Fatalf("offsets: %v", api.offsets)
	}
	if sessions.refreshCalls != 1 {
		t.Fatalf("watchdog did not run at startup: %d", sessions.refreshCalls)
	}
	var replies []string
	for _, m := range api.sent {
		if m.chatID == admin && !m.silent {
			replies = append(replies, strings.SplitN(m.text, "\n", 2)[0])
		}
	}
	if len(replies) != 2 || replies[0] != "Shopee session bot." || !strings.HasPrefix(replies[1], "Gateway: ready") {
		t.Fatalf("replies: %q", replies)
	}
}
