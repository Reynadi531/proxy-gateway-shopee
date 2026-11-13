package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	httpadapter "pg-shopee/internal/adapter/http"
	"pg-shopee/internal/adapter/postgres"
	"pg-shopee/internal/adapter/shopee"
	"pg-shopee/internal/adapter/telegram"
	"pg-shopee/internal/adapter/webhook"
	"pg-shopee/internal/config"
	"pg-shopee/internal/usecase"

	"github.com/gofiber/fiber/v3"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
func run(log *slog.Logger) error {
	cfg, err := config.LoadGateway()
	if err != nil {
		return err
	}
	qr, err := os.ReadFile(cfg.QRISFile)
	if err != nil {
		return fmt.Errorf("read static QRIS: %w", err)
	}
	gateway, err := shopee.New(cfg.SessionFile, strings.TrimSpace(string(qr)), cfg.MerchantID, cfg.StoreID, cfg.MaxPages, cfg.PollInterval)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	repo, err := postgres.Open(startup, cfg.DatabaseURL)
	cancel()
	if err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	defer repo.Close()
	repo.UniqueCodeMin = cfg.UniqueCodeMin
	repo.UniqueCodeMax = cfg.UniqueCodeMax
	service := &usecase.TransactionService{Repository: repo, Gateway: gateway, Scope: "shopee:" + cfg.MerchantID + ":" + cfg.StoreID, Expiry: cfg.Expiry, AllowedOrigins: cfg.AllowedOrigins}
	workers, cancelWorkers := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { service.RunPoller(workers, cfg.PollInterval, log) })
	wg.Go(func() { service.RunWebhooks(workers, webhook.New(cfg.WebhookSecret, cfg.AllowedOrigins), log) })
	if tg := cfg.Telegram; tg.Enabled() {
		sessions := shopee.NewSessionManager(cfg.SessionFile, cfg.MerchantID, cfg.StoreID)
		bot := telegram.New(telegram.NewClient(tg.BotToken, ""), sessions, gateway, telegram.Config{Admins: tg.AdminIDs, Phone: tg.Phone, Password: tg.Password, RefreshBefore: tg.RefreshBefore, CheckInterval: tg.CheckInterval}, log.With("component", "telegram"))
		wg.Go(func() { bot.Run(workers) })
		log.Info("telegram session bot enabled", "admins", len(tg.AdminIDs))
	}
	defer func() { cancelWorkers(); wg.Wait() }()
	app := httpadapter.NewGatewayServer(service, cfg.APIKey, log)
	log.Info("starting server", "address", cfg.HTTPAddress)
	return app.Listen(cfg.HTTPAddress, fiber.ListenConfig{DisableStartupMessage: true, GracefulContext: ctx, ShutdownTimeout: cfg.ShutdownTimeout})
}
