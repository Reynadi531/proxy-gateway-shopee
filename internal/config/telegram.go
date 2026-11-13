package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var botTokenPattern = regexp.MustCompile(`^\d+:[A-Za-z0-9_-]{30,}$`)

// TelegramConfig configures the optional session bot. It is enabled when BotToken is set.
type TelegramConfig struct {
	BotToken      string
	AdminIDs      map[int64]bool
	Phone         string
	Password      string
	RefreshBefore time.Duration
	CheckInterval time.Duration
}

func (c TelegramConfig) Enabled() bool { return c.BotToken != "" }

func LoadTelegram() (TelegramConfig, error) {
	c := TelegramConfig{BotToken: strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")), AdminIDs: map[int64]bool{}, Phone: strings.TrimSpace(os.Getenv("SHOPEE_LOGIN_PHONE")), Password: os.Getenv("SHOPEE_LOGIN_PASSWORD"), RefreshBefore: 2 * time.Hour, CheckInterval: time.Minute}
	if !c.Enabled() {
		return c, nil
	}
	if !botTokenPattern.MatchString(c.BotToken) {
		return c, fmt.Errorf("TELEGRAM_BOT_TOKEN must be a bot token from @BotFather")
	}
	for _, raw := range strings.Split(os.Getenv("TELEGRAM_ADMIN_IDS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return c, fmt.Errorf("TELEGRAM_ADMIN_IDS must contain positive Telegram user IDs")
		}
		c.AdminIDs[id] = true
	}
	if len(c.AdminIDs) == 0 {
		return c, fmt.Errorf("TELEGRAM_ADMIN_IDS is required when TELEGRAM_BOT_TOKEN is set")
	}
	for _, setting := range []struct {
		key      string
		target   *time.Duration
		min, max time.Duration
	}{
		{"SESSION_REFRESH_BEFORE", &c.RefreshBefore, time.Minute, 23 * time.Hour},
		{"SESSION_CHECK_INTERVAL", &c.CheckInterval, 10 * time.Second, time.Hour},
	} {
		raw, ok := os.LookupEnv(setting.key)
		if !ok {
			continue
		}
		v, err := time.ParseDuration(raw)
		if err != nil || v < setting.min || v > setting.max {
			return c, fmt.Errorf("%s must be between %s and %s", setting.key, setting.min, setting.max)
		}
		*setting.target = v
	}
	return c, nil
}
