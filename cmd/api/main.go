package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	httpadapter "pg-shopee/internal/adapter/http"
	"pg-shopee/internal/adapter/memory"
	"pg-shopee/internal/config"
	"pg-shopee/internal/usecase"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	repository := memory.NewUserRepository()
	users := usecase.NewUserService(repository, uuid.NewString)
	app := httpadapter.NewServer(users, log)
	log.Info("starting server", "address", cfg.HTTPAddress)
	return app.Listen(cfg.HTTPAddress, fiber.ListenConfig{
		DisableStartupMessage: true,
		GracefulContext:       ctx,
		ShutdownTimeout:       cfg.ShutdownTimeout,
	})
}
