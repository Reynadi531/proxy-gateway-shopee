package http

import (
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

type userResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func userDTO(user domain.User) userResponse {
	return userResponse{ID: user.ID, Name: user.Name, Email: user.Email}
}

func NewServer(users *usecase.UserService, log *slog.Logger) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:      "pg-shopee",
		BodyLimit:    1024 * 1024,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
		Immutable:    true,
		ErrorHandler: func(c fiber.Ctx, err error) error {
			status, message := http.StatusInternalServerError, "internal server error"
			var fiberError *fiber.Error
			switch {
			case errors.Is(err, domain.ErrInvalidUser):
				status, message = http.StatusBadRequest, domain.ErrInvalidUser.Error()
			case errors.Is(err, domain.ErrUserNotFound):
				status, message = http.StatusNotFound, domain.ErrUserNotFound.Error()
			case errors.Is(err, domain.ErrEmailTaken):
				status, message = http.StatusConflict, domain.ErrEmailTaken.Error()
			case errors.As(err, &fiberError) && fiberError.Code < 500:
				status, message = fiberError.Code, fiberError.Message
			}
			if status >= 500 {
				log.Error("request failed", "error", err, "request_id", requestid.FromContext(c))
			}
			return c.Status(status).JSON(fiber.Map{"error": message})
		},
	})
	app.Use(requestid.New(), recover.New())
	app.Get("/healthz", func(c fiber.Ctx) error { return c.JSON(fiber.Map{"status": "ok"}) })
	api := app.Group("/api/v1/users")
	api.Post("/", func(c fiber.Ctx) error {
		var body struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		}
		if err := c.Bind().JSON(&body); err != nil {
			return fiber.NewError(http.StatusBadRequest, "invalid JSON body")
		}
		user, err := users.Create(c.Context(), body.Name, body.Email)
		if err != nil {
			return err
		}
		c.Location("/api/v1/users/" + user.ID)
		return c.Status(http.StatusCreated).JSON(userDTO(user))
	})
	api.Get("/", func(c fiber.Ctx) error {
		result, err := users.List(c.Context())
		if err != nil {
			return err
		}
		response := make([]userResponse, 0, len(result))
		for _, user := range result {
			response = append(response, userDTO(user))
		}
		return c.JSON(response)
	})
	api.Get("/:id", func(c fiber.Ctx) error {
		user, err := users.Get(c.Context(), c.Params("id"))
		if err != nil {
			return err
		}
		return c.JSON(userDTO(user))
	})
	return app
}
