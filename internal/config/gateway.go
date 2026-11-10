package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type GatewayConfig struct {
	Config
	DatabaseURL    string
	APIKey         string
	WebhookSecret  string
	SessionFile    string
	QRISFile       string
	MerchantID     string
	StoreID        string
	Expiry         time.Duration
	PollInterval   time.Duration
	MaxPages       int
	UniqueCodeMin  int64
	UniqueCodeMax  int64
	AllowedOrigins map[string]bool
}

func LoadGateway() (GatewayConfig, error) {
	base, err := Load()
	if err != nil {
		return GatewayConfig{}, err
	}
	c := GatewayConfig{Config: base, DatabaseURL: os.Getenv("DATABASE_URL"), APIKey: os.Getenv("API_KEY"), WebhookSecret: os.Getenv("WEBHOOK_SECRET"), MerchantID: os.Getenv("SHOPEE_MERCHANT_ID"), StoreID: os.Getenv("SHOPEE_STORE_ID"), SessionFile: "session.json", QRISFile: "qris.txt", Expiry: 15 * time.Minute, PollInterval: 5 * time.Second, MaxPages: 100, UniqueCodeMin: 1, UniqueCodeMax: 999, AllowedOrigins: map[string]bool{}}
	var missing []string
	for _, setting := range []struct{ name, value string }{
		{"DATABASE_URL", c.DatabaseURL},
		{"SHOPEE_MERCHANT_ID", c.MerchantID},
		{"SHOPEE_STORE_ID", c.StoreID},
	} {
		if strings.TrimSpace(setting.value) == "" {
			missing = append(missing, setting.name)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	if len(c.APIKey) < 32 || len(c.WebhookSecret) < 32 {
		return c, fmt.Errorf("API_KEY and WEBHOOK_SECRET must each contain at least 32 characters")
	}
	if c.APIKey == c.WebhookSecret {
		return c, fmt.Errorf("API_KEY and WEBHOOK_SECRET must be different")
	}
	if v := os.Getenv("SHOPEE_SESSION_FILE"); v != "" {
		c.SessionFile = v
	}
	if v := os.Getenv("SHOPEE_QRIS_FILE"); v != "" {
		c.QRISFile = v
	}
	for key, target := range map[string]*time.Duration{"PAYMENT_EXPIRY": &c.Expiry, "POLL_INTERVAL": &c.PollInterval} {
		if raw, ok := os.LookupEnv(key); ok {
			v, e := time.ParseDuration(raw)
			if e != nil || v < time.Second || v > 24*time.Hour {
				return c, fmt.Errorf("%s must be between 1s and 24h", key)
			}
			*target = v
		}
	}
	if raw, ok := os.LookupEnv("SHOPEE_MAX_PAGES"); ok {
		c.MaxPages, err = strconv.Atoi(raw)
		if err != nil || c.MaxPages < 1 || c.MaxPages > 10000 {
			return c, fmt.Errorf("SHOPEE_MAX_PAGES must be between 1 and 10000")
		}
	}
	for key, target := range map[string]*int64{"UNIQUE_CODE_MIN": &c.UniqueCodeMin, "UNIQUE_CODE_MAX": &c.UniqueCodeMax} {
		if raw, ok := os.LookupEnv(key); ok {
			v, e := strconv.ParseInt(raw, 10, 64)
			if e != nil || v < 1 || v > 999 {
				return c, fmt.Errorf("%s must be an integer between 1 and 999", key)
			}
			*target = v
		}
	}
	if c.UniqueCodeMin > c.UniqueCodeMax {
		return c, fmt.Errorf("UNIQUE_CODE_MIN must not exceed UNIQUE_CODE_MAX")
	}
	for _, origin := range strings.Split(os.Getenv("WEBHOOK_ALLOWED_ORIGINS"), ",") {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		u, e := url.Parse(origin)
		if e != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
			return c, fmt.Errorf("WEBHOOK_ALLOWED_ORIGINS must contain exact http(s) origins without paths")
		}
		c.AllowedOrigins[origin] = true
	}
	if len(c.AllowedOrigins) == 0 {
		return c, fmt.Errorf("WEBHOOK_ALLOWED_ORIGINS is required")
	}
	return c, nil
}
