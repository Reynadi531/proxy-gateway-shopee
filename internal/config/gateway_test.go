package config_test

import (
	"pg-shopee/internal/config"
	"testing"
)

func TestGatewayConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example/gateway")
	t.Setenv("SHOPEE_MERCHANT_ID", "merchant")
	t.Setenv("SHOPEE_STORE_ID", "store")
	t.Setenv("API_KEY", "01234567890123456789012345678901")
	t.Setenv("WEBHOOK_SECRET", "abcdefghijklmnopqrstuvwxyz012345")
	t.Setenv("WEBHOOK_ALLOWED_ORIGINS", "https://merchant.example,http://localhost:9090")
	c, err := config.LoadGateway()
	if err != nil {
		t.Fatal(err)
	}
	if !c.AllowedOrigins["https://merchant.example"] || !c.AllowedOrigins["http://localhost:9090"] {
		t.Fatal("missing allowed origins")
	}
	for _, tc := range []struct{ key, value string }{
		{"API_KEY", "short"}, {"WEBHOOK_SECRET", "short"}, {"DATABASE_URL", ""}, {"SHOPEE_MERCHANT_ID", ""},
		{"WEBHOOK_ALLOWED_ORIGINS", ""}, {"WEBHOOK_ALLOWED_ORIGINS", "https://merchant.example/path"},
		{"WEBHOOK_ALLOWED_ORIGINS", "https://user:pass@merchant.example"}, {"WEBHOOK_ALLOWED_ORIGINS", "file:///etc/passwd"},
		{"POLL_INTERVAL", "0s"}, {"PAYMENT_EXPIRY", "invalid"}, {"SHOPEE_MAX_PAGES", "0"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := config.LoadGateway(); err == nil {
				t.Fatalf("accepted %s=%q", tc.key, tc.value)
			}
		})
	}
}

func TestGatewayConfigReportsOnlyMissingSettings(t *testing.T) {
	t.Setenv("HTTP_ADDRESS", ":8080")
	t.Setenv("SHUTDOWN_TIMEOUT", "10s")
	t.Setenv("DATABASE_URL", "postgres://example/gateway")
	t.Setenv("SHOPEE_MERCHANT_ID", "")
	t.Setenv("SHOPEE_STORE_ID", "")
	_, err := config.LoadGateway()
	if err == nil || err.Error() != "missing required environment variables: SHOPEE_MERCHANT_ID, SHOPEE_STORE_ID" {
		t.Fatalf("expected only the missing settings, got %v", err)
	}
}
