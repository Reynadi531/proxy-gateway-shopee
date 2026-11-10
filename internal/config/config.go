package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddress     string
	ShutdownTimeout time.Duration
}

func Load() (Config, error) {
	cfg := Config{HTTPAddress: ":8080", ShutdownTimeout: 10 * time.Second}
	if value, ok := os.LookupEnv("HTTP_ADDRESS"); ok {
		cfg.HTTPAddress = value
	}
	_, port, err := net.SplitHostPort(cfg.HTTPAddress)
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDRESS must be host:port: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return Config{}, fmt.Errorf("HTTP_ADDRESS port must be between 1 and 65535")
	}
	if value, ok := os.LookupEnv("SHUTDOWN_TIMEOUT"); ok {
		timeout, err := time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT must be a positive duration")
		}
		cfg.ShutdownTimeout = timeout
	}
	return cfg, nil
}
