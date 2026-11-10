package webhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"pg-shopee/internal/adapter/webhook"
	"pg-shopee/internal/domain"
)

func TestWebhookSigningFailureAndRedirect(t *testing.T) {
	const secret = "a-secret-only-the-website-and-gateway-share"
	mode := 200
	targetHits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHits++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(r.Header.Get("X-Webhook-Timestamp") + "."))
		mac.Write(body)
		if r.Header.Get("X-Webhook-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || r.Header.Get("X-Webhook-ID") != "event-1" {
			t.Error("invalid signature or event id")
		}
		if mode == 302 {
			w.Header().Set("Location", target.URL)
		}
		w.WriteHeader(mode)
	}))
	defer server.Close()
	sender := webhook.New(secret, map[string]bool{server.URL: true})
	d := domain.Delivery{ID: "event-1", URL: server.URL + "/hook", Payload: []byte(`{"status":"paid"}`)}
	if err := sender.Send(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{500, 302} {
		mode = status
		if err := sender.Send(context.Background(), d); err == nil {
			t.Fatalf("accepted HTTP %d", status)
		}
	}
	if targetHits != 0 {
		t.Fatal("followed webhook redirect")
	}
	d.URL = target.URL
	if err := sender.Send(context.Background(), d); err == nil {
		t.Fatal("accepted an unconfigured origin")
	}
}
