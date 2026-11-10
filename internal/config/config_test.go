package config_test

import (
	"testing"
	"time"

	"pg-shopee/internal/config"
)

func TestLoad(t *testing.T) {
	t.Setenv("HTTP_ADDRESS", "127.0.0.1:3000")
	t.Setenv("SHUTDOWN_TIMEOUT", "5s")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddress != "127.0.0.1:3000" || cfg.ShutdownTimeout != 5*time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	for _, address := range []string{"", "localhost", ":0", ":65536", ":http"} {
		t.Setenv("HTTP_ADDRESS", address)
		if _, err := config.Load(); err == nil {
			t.Errorf("accepted address %q", address)
		}
	}
	t.Setenv("HTTP_ADDRESS", ":8080")
	for _, timeout := range []string{"", "0s", "-1s", "invalid"} {
		t.Setenv("SHUTDOWN_TIMEOUT", timeout)
		if _, err := config.Load(); err == nil {
			t.Errorf("accepted timeout %q", timeout)
		}
	}
}
