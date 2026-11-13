package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"pg-shopee/internal/domain"
)

const (
	pollTimeout      = 30 * time.Second
	pollErrorBackoff = 5 * time.Second
	refreshRetry     = 10 * time.Minute
	alertReminder    = time.Hour
	conversationTTL  = 10 * time.Minute
	// Messages older than this are ignored, so OTPs or commands queued while
	// the process was down are not replayed.
	maxMessageAge = 5 * time.Minute
)

var otpPattern = regexp.MustCompile(`^\d{4,10}$`)

type API interface {
	GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error)
	SendMessage(ctx context.Context, chatID int64, text string, silent bool) error
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
}

type Sessions interface {
	Status() (domain.SessionInfo, error)
	Refresh(context.Context) (domain.SessionInfo, error)
	RequestOTP(ctx context.Context, phone, password string) (domain.OTPRequest, error)
	VerifyOTP(ctx context.Context, otp string) (domain.SessionInfo, error)
	CancelLogin() bool
}

type LoginStater interface {
	LoginState() domain.LoginState
}

type Config struct {
	// Admins are the Telegram user IDs allowed to use the bot; alerts go to their private chats.
	Admins map[int64]bool
	// Phone and Password skip the matching prompts when set.
	Phone    string
	Password string
	// RefreshBefore renews the token this long before it expires.
	RefreshBefore time.Duration
	CheckInterval time.Duration
}

// Bot alerts admins when the Shopee session needs attention, renews it
// without an OTP when possible, and runs the OTP login over chat.
//
// The update loop owns conv; the watchdog owns the remaining mutable fields.
type Bot struct {
	api      API
	sessions Sessions
	gateway  LoginStater
	cfg      Config
	log      *slog.Logger
	now      func() time.Time
	// retryWait is the pause after a failed poll unless Telegram asks for longer.
	retryWait time.Duration

	conv *conversation

	nextRefresh time.Time
	refreshErr  error
	alertedAt   time.Time
}

type step int

const (
	stepPhone step = iota + 1
	stepPassword
	stepOTP
)

type conversation struct {
	chatID  int64
	step    step
	phone   string
	expires time.Time
}

func New(api API, sessions Sessions, gateway LoginStater, cfg Config, log *slog.Logger) *Bot {
	return &Bot{api: api, sessions: sessions, gateway: gateway, cfg: cfg, log: log, now: time.Now, retryWait: pollErrorBackoff}
}

// Run polls Telegram and watches the session until ctx is done.
func (b *Bot) Run(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.watch(ctx)
	}()
	b.poll(ctx)
	<-done
}

func (b *Bot) watch(ctx context.Context) {
	ticker := time.NewTicker(b.cfg.CheckInterval)
	defer ticker.Stop()
	for {
		b.CheckSession(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *Bot) poll(ctx context.Context) {
	var offset int64
	for ctx.Err() == nil {
		updates, err := b.api.GetUpdates(ctx, offset, pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wait := b.retryWait
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.RetryAfter > wait {
				wait = apiErr.RetryAfter
			}
			b.log.Error("telegram polling failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}
		for _, update := range updates {
			offset = update.UpdateID + 1
			if update.Message != nil {
				b.HandleMessage(ctx, *update.Message)
			}
		}
	}
}

// CheckSession runs one watchdog pass: renew when due, alert when a login is needed.
func (b *Bot) CheckSession(ctx context.Context) {
	now := b.now()
	info, err := b.sessions.Status()
	state := b.gateway.LoginState()
	rejected := state.State == "auth_required"
	known := err == nil && info.TokenPresent && !info.ExpiresAt.IsZero()
	expired := err == nil && (!info.TokenPresent || known && !now.Before(info.ExpiresAt))
	due := expired || known && info.ExpiresAt.Sub(now) < b.cfg.RefreshBefore

	var problem string
	switch {
	case err != nil:
		problem = err.Error()
	case !due && !rejected:
		b.refreshErr = nil
	case !info.Renewable:
		switch {
		case rejected:
			problem = "Shopee rejected the session and it cannot be renewed without an OTP."
		case !info.TokenPresent:
			problem = "The session has no merchant token and cannot be renewed without an OTP."
		case expired:
			problem = "The Shopee token expired and the session cannot be renewed without an OTP."
		default:
			problem = "The Shopee token expires " + formatTime(info.ExpiresAt, now) + " and the session cannot be renewed without an OTP."
		}
	default:
		if !now.Before(b.nextRefresh) {
			b.nextRefresh = now.Add(refreshRetry)
			renewed, refreshErr := b.sessions.Refresh(ctx)
			if ctx.Err() != nil {
				return
			}
			b.refreshErr = refreshErr
			if refreshErr == nil {
				b.log.Info("shopee session renewed", "expires_at", renewed.ExpiresAt)
				b.alertedAt = time.Time{}
				b.broadcast(ctx, "Shopee session renewed without OTP. Token valid until "+formatTime(renewed.ExpiresAt, now)+".", true)
				return
			}
			b.log.Warn("shopee session renewal failed", "error", refreshErr)
		}
		// Keep retrying quietly while the current token still works.
		switch {
		case b.refreshErr != nil && (expired || rejected || errors.Is(b.refreshErr, domain.ErrReloginRequired)):
			problem = "Automatic renewal failed: " + b.refreshErr.Error() + "."
		case b.refreshErr == nil && rejected:
			problem = "Shopee still rejects the session after renewal; retrying every " + shortDuration(refreshRetry) + "."
		}
	}
	if problem == "" {
		b.alertedAt = time.Time{}
		return
	}
	if !b.alertedAt.IsZero() && now.Sub(b.alertedAt) < alertReminder {
		return
	}
	b.alertedAt = now
	b.log.Warn("shopee login required", "problem", problem, "login_state", state.State)
	b.broadcast(ctx, "Shopee login needed.\n"+problem+"\nPayments cannot be created until the session works again. Send /login to log in with an OTP.", false)
}

// HandleMessage processes one incoming message.
func (b *Bot) HandleMessage(ctx context.Context, msg Message) {
	if msg.From == nil || msg.Chat.Type != "private" {
		return
	}
	if !b.cfg.Admins[msg.From.ID] {
		b.log.Warn("telegram message from unauthorized user", "user_id", msg.From.ID)
		b.reply(ctx, msg.Chat.ID, fmt.Sprintf("Not authorized. Your Telegram user ID is %d.", msg.From.ID))
		return
	}
	if b.now().Sub(time.Unix(msg.Date, 0)) > maxMessageAge {
		b.log.Info("ignoring stale telegram message", "message_id", msg.MessageID)
		return
	}
	text := strings.TrimSpace(msg.Text)
	if strings.HasPrefix(text, "/") {
		command, _, _ := strings.Cut(strings.Fields(text)[0], "@")
		b.command(ctx, msg, strings.ToLower(command))
		return
	}
	b.input(ctx, msg, text)
}

func (b *Bot) command(ctx context.Context, msg Message, command string) {
	chatID := msg.Chat.ID
	switch command {
	case "/start", "/help":
		b.reply(ctx, chatID, "Shopee session bot.\n/status - session and gateway health\n/login - log in with an OTP\n/refresh - renew the token without an OTP\n/cancel - abort the login in progress")
	case "/status":
		b.reply(ctx, chatID, b.statusText())
	case "/refresh":
		b.reply(ctx, chatID, "Renewing the Shopee session...")
		info, err := b.sessions.Refresh(ctx)
		if err != nil {
			hint := ""
			if errors.Is(err, domain.ErrReloginRequired) {
				hint = " Send /login to log in with an OTP."
			}
			b.reply(ctx, chatID, "Renewal failed: "+err.Error()+"."+hint)
			return
		}
		b.reply(ctx, chatID, "Session renewed. Token valid until "+formatTime(info.ExpiresAt, b.now())+".")
	case "/login":
		b.conv = &conversation{chatID: chatID, expires: b.now().Add(conversationTTL)}
		b.sessions.CancelLogin()
		if b.cfg.Phone == "" {
			b.conv.step = stepPhone
			b.reply(ctx, chatID, "Send the Shopee account phone number (08... or +62...). /cancel to abort.")
			return
		}
		b.conv.phone = b.cfg.Phone
		b.askPassword(ctx)
	case "/skip":
		if b.conv == nil || b.conv.chatID != chatID || b.conv.step != stepPassword {
			b.reply(ctx, chatID, "Nothing to skip.")
			return
		}
		b.requestOTP(ctx, "")
	case "/cancel":
		active := b.sessions.CancelLogin() || b.conv != nil
		b.conv = nil
		if active {
			b.reply(ctx, chatID, "Login cancelled.")
		} else {
			b.reply(ctx, chatID, "No login in progress.")
		}
	default:
		b.reply(ctx, chatID, "Unknown command. Send /help.")
	}
}

func (b *Bot) input(ctx context.Context, msg Message, text string) {
	chatID := msg.Chat.ID
	if b.conv == nil || b.conv.chatID != chatID {
		b.reply(ctx, chatID, "Send /help for commands.")
		return
	}
	// Phone numbers, passwords and OTPs do not stay in the chat history.
	if err := b.api.DeleteMessage(ctx, chatID, msg.MessageID); err != nil {
		b.log.Warn("telegram message deletion failed", "error", err)
	}
	if !b.now().Before(b.conv.expires) {
		b.conv = nil
		b.sessions.CancelLogin()
		b.reply(ctx, chatID, "Login timed out. Send /login to start again.")
		return
	}
	switch b.conv.step {
	case stepPhone:
		b.conv.phone = text
		b.askPassword(ctx)
	case stepPassword:
		b.requestOTP(ctx, text)
	case stepOTP:
		if !otpPattern.MatchString(text) {
			b.reply(ctx, chatID, "The OTP must be 4 to 10 digits. Send it again or /cancel.")
			return
		}
		b.reply(ctx, chatID, "Verifying the OTP...")
		info, err := b.sessions.VerifyOTP(ctx, text)
		if errors.Is(err, domain.ErrNoPendingLogin) {
			b.conv = nil
			b.reply(ctx, chatID, "The OTP login expired or ran out of attempts. Send /login to start again.")
			return
		}
		if err != nil {
			b.log.Warn("shopee otp login failed", "error", err)
			b.reply(ctx, chatID, "Login failed: "+err.Error()+".\nSend the code again, or /login to request a new OTP.")
			return
		}
		b.conv = nil
		b.log.Info("shopee otp login completed", "merchant_id", info.MerchantID, "store_id", info.StoreID)
		b.reply(ctx, chatID, "Logged in as "+merchantLabel(info)+", store "+info.StoreID+". Token valid until "+formatTime(info.ExpiresAt, b.now())+".\nThe gateway picks up the new session on its next scan.")
	}
}

func (b *Bot) askPassword(ctx context.Context) {
	if b.cfg.Password != "" {
		b.requestOTP(ctx, b.cfg.Password)
		return
	}
	b.conv.step = stepPassword
	b.reply(ctx, b.conv.chatID, "Send the Shopee account password, or /skip if the account has none. The message will be deleted.")
}

func (b *Bot) requestOTP(ctx context.Context, password string) {
	chatID := b.conv.chatID
	b.reply(ctx, chatID, "Requesting an OTP from Shopee...")
	request, err := b.sessions.RequestOTP(ctx, b.conv.phone, password)
	if err != nil {
		b.conv = nil
		b.log.Warn("shopee otp request failed", "error", err)
		b.reply(ctx, chatID, "OTP request failed: "+err.Error()+".\nSend /login to try again.")
		return
	}
	b.conv.step = stepOTP
	b.conv.expires = request.ExpiresAt
	b.reply(ctx, chatID, "OTP sent via "+channelName(request.Channel)+". Send the code here; it will be deleted. Valid until "+formatTime(request.ExpiresAt, b.now())+". /cancel to abort.")
}

func (b *Bot) statusText() string {
	now := b.now()
	state := b.gateway.LoginState()
	lines := []string{"Gateway: " + state.State}
	if state.LastSuccessfulAt != nil {
		lines = append(lines, "Last successful scan: "+formatTime(*state.LastSuccessfulAt, now))
	}
	info, err := b.sessions.Status()
	if err != nil {
		lines = append(lines, "Session: "+err.Error())
		return strings.Join(lines, "\n")
	}
	renewal := "available"
	if !info.Renewable {
		renewal = "unavailable (OTP login needed when the token expires)"
	}
	token := "missing"
	if info.TokenPresent {
		token = "valid until " + formatTime(info.ExpiresAt, now)
	}
	lines = append(lines, "Merchant: "+merchantLabel(info), "Store: "+info.StoreID, "Token: "+token, "Renewal without OTP: "+renewal)
	return strings.Join(lines, "\n")
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	if err := b.api.SendMessage(ctx, chatID, text, false); err != nil {
		b.log.Warn("telegram reply failed", "error", err)
	}
}

func (b *Bot) broadcast(ctx context.Context, text string, silent bool) {
	for id := range b.cfg.Admins {
		if err := b.api.SendMessage(ctx, id, text, silent); err != nil {
			b.log.Warn("telegram notification failed", "user_id", id, "error", err)
		}
	}
}

func merchantLabel(info domain.SessionInfo) string {
	if info.MerchantName == "" {
		return info.MerchantID
	}
	return info.MerchantName + " (" + info.MerchantID + ")"
}

func channelName(channel int) string {
	if channel == 3 {
		return "WhatsApp"
	}
	return fmt.Sprintf("channel %d", channel)
}

func formatTime(t, now time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	text := t.UTC().Format("2006-01-02 15:04 UTC")
	d := t.Sub(now).Round(time.Minute)
	switch {
	case d > 0:
		return text + " (in " + shortDuration(d) + ")"
	case d < 0:
		return text + " (" + shortDuration(-d) + " ago)"
	default:
		return text + " (now)"
	}
}

func shortDuration(d time.Duration) string {
	h, m := int(d.Hours()), int(d.Minutes())%60
	if h == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}
