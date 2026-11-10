package http_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	httpadapter "pg-shopee/internal/adapter/http"
	"pg-shopee/internal/adapter/postgres"
	"pg-shopee/internal/domain"
	"pg-shopee/internal/usecase"

	"github.com/google/uuid"
)

type testGateway struct{ state domain.LoginState }

func (g *testGateway) LoginState() domain.LoginState { return g.state }
func (*testGateway) QRIS(n int64) (string, error)    { return "dynamic-qris", nil }
func (*testGateway) Scan(context.Context, time.Time, time.Time) ([]domain.MerchantTransaction, bool, error) {
	return nil, true, nil
}

func TestTransactionHTTP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL HTTP integration tests")
	}
	repo, err := postgres.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	gateway := &testGateway{state: domain.LoginState{Authenticated: true, State: "ready"}}
	service := &usecase.TransactionService{Repository: repo, Gateway: gateway, Scope: uuid.NewString(), Expiry: time.Minute, AllowedOrigins: map[string]bool{"https://merchant.example": true}}
	defer repo.DB.Exec("DELETE FROM gateway_transactions WHERE scope=?", service.Scope)
	const key = "01234567890123456789012345678901"
	app := httpadapter.NewGatewayServer(service, key, slog.New(slog.NewTextHandler(io.Discard, nil)))
	id := uuid.NewString()
	body := `{"transaction_id":"` + id + `","amount":10000,"webhook_url":"https://merchant.example/hook"}`
	call := func(method, path, body, key string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-API-Key", key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, resp.StatusCode, want, data)
		}
		return data
	}
	call("POST", "/api/v1/transactions", body, "", 401)
	call("GET", "/api/v1/shopee/status", "", "wrong-key", 401)
	call("GET", "/healthz", "", "", 200)
	data := call("POST", "/api/v1/transactions", body, key, 201)
	var response struct {
		Transaction domain.Transaction `json:"transaction"`
		Login       domain.LoginState  `json:"login_state"`
	}
	if err = json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	if response.Transaction.TotalAmount != 10001 || response.Transaction.QRIS == "" || !response.Login.Authenticated {
		t.Fatalf("invalid response: %s", data)
	}
	call("POST", "/api/v1/transactions", body, key, 200)
	call("POST", "/api/v1/transactions", strings.Replace(body, "10000", "10001", 1), key, 409)
	call("GET", "/api/v1/transactions/"+id, "", key, 200)
	call("GET", "/api/v1/transactions/missing", "", key, 404)
	call("POST", "/api/v1/transactions", `{`, key, 400)
	for _, value := range []string{"0", "-1", "1.5", "1000000000"} {
		call("POST", "/api/v1/transactions", strings.Replace(body, "10000", value, 1), key, 400)
	}
	call("POST", "/api/v1/transactions", strings.Replace(body, "merchant.example", "untrusted.example", 1), key, 400)
	gateway.state = domain.LoginState{State: "session_expired_or_invalid"}
	call("POST", "/api/v1/transactions", body, key, 200)
	call("POST", "/api/v1/transactions", strings.Replace(body, id, uuid.NewString(), 1), key, 503)
	call("GET", "/api/v1/shopee/status", "", key, 200)
}
