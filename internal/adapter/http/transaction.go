package http

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"pg-shopee/internal/domain"
	"pg-shopee/internal/usecase"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"
)

func NewGatewayServer(service *usecase.TransactionService, apiKey string, log *slog.Logger) *fiber.App {
	app := fiber.New(fiber.Config{AppName: "pg-shopee", BodyLimit: 16384, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, Immutable: true,
		ErrorHandler: func(c fiber.Ctx, err error) error {
			code, message := 500, "internal server error"
			var fe *fiber.Error
			switch {
			case errors.Is(err, domain.ErrInvalidTransaction):
				code, message = 400, err.Error()
			case errors.Is(err, domain.ErrTransactionNotFound):
				code, message = 404, err.Error()
			case errors.Is(err, domain.ErrTransactionConflict):
				code, message = 409, err.Error()
			case errors.Is(err, domain.ErrAmountUnavailable):
				code, message = 409, err.Error()
			case errors.Is(err, domain.ErrSessionUnavailable):
				code, message = 503, err.Error()
			case errors.As(err, &fe) && fe.Code < 500:
				code, message = fe.Code, fe.Message
			}
			if code >= 500 {
				log.Error("request failed", "error", err, "request_id", requestid.FromContext(c))
			}
			return c.Status(code).JSON(fiber.Map{"error": message, "login_state": service.Gateway.LoginState()})
		}})
	app.Use(requestid.New(), recover.New())
	registerSwagger(app)
	app.Get("/healthz", func(c fiber.Ctx) error { return c.JSON(fiber.Map{"status": "ok"}) })
	expected := sha256.Sum256([]byte(apiKey))
	api := app.Group("/api/v1", func(c fiber.Ctx) error {
		got := sha256.Sum256([]byte(c.Get("X-API-Key")))
		if apiKey == "" || subtle.ConstantTimeCompare(got[:], expected[:]) != 1 {
			return c.Status(401).JSON(fiber.Map{"error": "invalid API key"})
		}
		c.Set("Cache-Control", "no-store")
		return c.Next()
	})
	api.Get("/shopee/status", func(c fiber.Ctx) error { return c.JSON(service.Gateway.LoginState()) })
	api.Post("/transactions", func(c fiber.Ctx) error {
		var body usecase.CreateTransactionInput
		if err := json.Unmarshal(c.Body(), &body); err != nil {
			return fiber.NewError(http.StatusBadRequest, "invalid JSON body")
		}
		ctx, cancel := context.WithTimeout(c.Context(), 10*time.Second)
		defer cancel()
		transaction, created, err := service.Create(ctx, body)
		if err != nil {
			return err
		}
		code := http.StatusOK
		if created {
			code = http.StatusCreated
		}
		return c.Status(code).JSON(fiber.Map{"transaction": transaction, "login_state": service.Gateway.LoginState()})
	})
	api.Get("/transactions/:id", func(c fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.Context(), 10*time.Second)
		defer cancel()
		transaction, err := service.Repository.Get(ctx, c.Params("id"))
		if err != nil {
			return err
		}
		return c.JSON(fiber.Map{"transaction": transaction, "login_state": service.Gateway.LoginState()})
	})
	return app
}
