package config_test

import (
	"testing"
	"time"

	"pg-shopee/internal/config"
)

func TestTelegramConfig(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	c, err := config.LoadTelegram()
	if err != nil || c.Enabled() {
		t.Fatalf("disabled bot: %+v %v", c, err)
	}

	t.Setenv("TELEGRAM_BOT_TOKEN", "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi")
	t.Setenv("TELEGRAM_ADMIN_IDS", "11, 22")
	t.Setenv("SHOPEE_LOGIN_PHONE", " 0812 ")
	c, err = config.LoadTelegram()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Enabled() || !c.AdminIDs[11] || !c.AdminIDs[22] || len(c.AdminIDs) != 2 || c.Phone != "0812" || c.RefreshBefore != 2*time.Hour || c.CheckInterval != time.Minute {
		t.Fatalf("defaults: %+v", c)
	}

	t.Setenv("SESSION_REFRESH_BEFORE", "6h")
	t.Setenv("SESSION_CHECK_INTERVAL", "30s")
	if c, err = config.LoadTelegram(); err != nil || c.RefreshBefore != 6*time.Hour || c.CheckInterval != 30*time.Second {
		t.Fatalf("durations: %+v %v", c, err)
	}

	for _, tc := range []struct{ key, value string }{
		{"TELEGRAM_BOT_TOKEN", "not-a-token"}, {"TELEGRAM_BOT_TOKEN", "123:short"}, {"TELEGRAM_BOT_TOKEN", "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi/../x"},
		{"TELEGRAM_ADMIN_IDS", ""}, {"TELEGRAM_ADMIN_IDS", "abc"}, {"TELEGRAM_ADMIN_IDS", "-5"}, {"TELEGRAM_ADMIN_IDS", "0"},
		{"SESSION_REFRESH_BEFORE", "30s"}, {"SESSION_REFRESH_BEFORE", "24h"}, {"SESSION_REFRESH_BEFORE", "soon"},
		{"SESSION_CHECK_INTERVAL", "1s"}, {"SESSION_CHECK_INTERVAL", "2h"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := config.LoadTelegram(); err == nil {
				t.Fatalf("accepted %s=%q", tc.key, tc.value)
			}
		})
	}
}
