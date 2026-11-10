package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "pg-shopee/internal/adapter/http"
	"pg-shopee/internal/adapter/memory"
	"pg-shopee/internal/domain"
	"pg-shopee/internal/usecase"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

func newApp() *fiber.App {
	return httpadapter.NewServer(usecase.NewUserService(memory.NewUserRepository(), uuid.NewString), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func request(t *testing.T, app *fiber.App, method, path, body string, status int) []byte {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, status, data)
	}
	if resp.Header.Get("X-Request-ID") == "" {
		t.Fatal("missing request ID")
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatal("expected JSON response")
	}
	return data
}

func TestUserLifecycle(t *testing.T) {
	app := newApp()
	request(t, app, "GET", "/healthz", "", 200)
	if body := request(t, app, "GET", "/api/v1/users", "", 200); string(body) != "[]" {
		t.Fatalf("empty list: %s", body)
	}
	body := request(t, app, "POST", "/api/v1/users", `{"name":" Ada ","email":" ADA@example.com "}`, 201)
	var created struct{ ID, Name, Email string }
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Name != "Ada" || created.Email != "ada@example.com" {
		t.Fatalf("unexpected user: %+v", created)
	}
	got := request(t, app, "GET", "/api/v1/users/"+created.ID, "", 200)
	if string(got) != string(body) {
		t.Fatalf("get returned %s, want %s", got, body)
	}
	var users []json.RawMessage
	if err := json.Unmarshal(request(t, app, "GET", "/api/v1/users", "", 200), &users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("got %d users, want 1", len(users))
	}
	request(t, app, "POST", "/api/v1/users", `{"name":"Other","email":"ADA@example.com"}`, 409)
}

func TestInvalidRequests(t *testing.T) {
	app := newApp()
	for _, body := range []string{`{`, `{}`, `{"name":" ","email":"ada@example.com"}`, `{"name":"Ada","email":"bad"}`, `{"name":"Ada","email":"Ada <ada@example.com>"}`} {
		request(t, app, "POST", "/api/v1/users", body, 400)
	}
	request(t, app, "GET", "/api/v1/users/missing", "", 404)
	request(t, app, "GET", "/missing", "", 404)
}

type failingRepository struct{}

func (failingRepository) Create(context.Context, domain.User) error {
	return errors.New("database password=secret")
}
func (failingRepository) Get(context.Context, string) (domain.User, error) {
	return domain.User{}, errors.New("database password=secret")
}
func (failingRepository) List(context.Context) ([]domain.User, error) {
	return nil, errors.New("database password=secret")
}

func TestInternalErrorsAreRedacted(t *testing.T) {
	app := httpadapter.NewServer(usecase.NewUserService(failingRepository{}, uuid.NewString), slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := request(t, app, "GET", "/api/v1/users", "", 500)
	if string(body) != `{"error":"internal server error"}` {
		t.Fatalf("unexpected error: %s", body)
	}
}
