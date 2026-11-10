package http_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "pg-shopee/internal/adapter/http"
	"pg-shopee/internal/usecase"
)

func TestSwaggerRoutesAndAuthentication(t *testing.T) {
	service := &usecase.TransactionService{Gateway: &testGateway{}}
	app := httpadapter.NewGatewayServer(service, "test-api-key", slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, tc := range []struct{ path, contentType string }{
		{"/swagger/", "text/html"},
		{"/swagger/index.html", "text/html"},
		{"/swagger/openapi.json", "application/json"},
		{"/swagger/swagger-init.js", "text/javascript"},
		{"/swagger/swagger-ui.css", "text/css"},
		{"/swagger/swagger-ui-bundle.js", "text/javascript"},
		{"/swagger/favicon-32x32.png", "image/png"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := app.Test(httptest.NewRequest("GET", tc.path, nil))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), tc.contentType) || len(body) == 0 {
				t.Fatalf("%s: status=%d type=%s length=%d", tc.path, resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
			}
			if tc.path == "/swagger/openapi.json" && !json.Valid(body) {
				t.Fatal("specification is not JSON")
			}
		})
	}
	resp, err := app.Test(httptest.NewRequest("GET", "/swagger", nil))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 308 || resp.Header.Get("Location") != "/swagger/" {
		t.Fatalf("canonical URL: status=%d location=%s", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, path := range []string{"/api/v1/shopee/status", "/api/v1/transactions/order-123"} {
		resp, err := app.Test(httptest.NewRequest("GET", path, nil))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("Swagger registration bypassed authentication for %s: %d", path, resp.StatusCode)
		}
	}
}
