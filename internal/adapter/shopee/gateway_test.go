package shopee

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hirotomasato/paygateme/qris"
	sdk "github.com/hirotomasato/paygateme/shopee"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSessionHealthFeedAndReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	qr, err := qris.BuildEmv(qris.EmvTlvMap{"00": "01", "01": "11", "53": "360", "58": "ID"})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := New(path, qr, "merchant", "store", 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if state := gateway.LoginState(); state.Authenticated || state.State != "session_missing" {
		t.Fatalf("missing session: %+v", state)
	}
	if err = os.WriteFile(path, []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	if state := gateway.LoginState(); state.State != "session_invalid" {
		t.Fatalf("invalid session: %+v", state)
	}
	writeSession := func(exp time.Time, token string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"token": token, "userid": "account", "businessId": "merchant", "exp": exp.Unix()})
		jwt := "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
		session := sdk.Session{AccountID: "account", Merchant: sdk.MerchantSummary{ID: "merchant"}, StoreID: "store", Cookies: []sdk.Cookie{{Name: sdk.LiveTokenCookie, Value: jwt, Domain: "partner.shopee.co.id", Path: "/", Secure: true}}}
		data, _ := json.Marshal(session)
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeSession(time.Now().Add(-time.Hour), "old")
	if state := gateway.LoginState(); state.Authenticated || state.State != "session_expired_or_invalid" {
		t.Fatalf("expired: %+v", state)
	}
	writeSession(time.Now().Add(time.Hour), "active")
	if state := gateway.LoginState(); !state.Authenticated || state.State != "unverified" {
		t.Fatalf("unverified: %+v", state)
	}
	mode := "ok"
	previous := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"code":0,"data":{"list":[]}}`
		switch mode {
		case "auth":
			body = `{"code":200020,"msg":"token invalid"}`
		case "truncated":
			body = `{"code":0,"data":{"list":[],"next_position":"next-page"}}`
		case "ok":
			body = `{"code":0,"data":{"list":[{"transactionId":"tx-1","amount":"50.001","createTime":1800000000,"merchantId":"merchant","storeId":"store","status":3,"service":1}]}}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	defer func() { http.DefaultTransport = previous }()
	start, end := time.Now().Add(-time.Hour), time.Now()
	feed, complete, err := gateway.Scan(context.Background(), start, end)
	if err != nil || !complete || len(feed) != 1 || feed[0].Amount != 50001 || feed[0].Status != "completed" {
		t.Fatalf("feed %+v complete %t err %v", feed, complete, err)
	}
	if state := gateway.LoginState(); state.State != "ready" || state.LastSuccessfulAt == nil {
		t.Fatalf("ready: %+v", state)
	}
	dynamic, err := gateway.QRIS(50001)
	if err != nil {
		t.Fatal(err)
	}
	tags, err := qris.ParseEmv(dynamic)
	if err != nil || tags["54"] != "50001" {
		t.Fatalf("QRIS amount %+v %v", tags, err)
	}
	mode = "truncated"
	_, complete, err = gateway.Scan(context.Background(), start, end)
	if err != nil || complete || gateway.LoginState().State != "scan_incomplete" {
		t.Fatal("truncated scan reported ready")
	}
	mode = "auth"
	_, _, err = gateway.Scan(context.Background(), start, end)
	if err == nil || gateway.LoginState().Authenticated || gateway.LoginState().State != "auth_required" {
		t.Fatalf("rejection: %v %+v", err, gateway.LoginState())
	}
	writeSession(time.Now().Add(time.Hour), "replacement")
	if state := gateway.LoginState(); !state.Authenticated || state.State != "unverified" || state.LastSuccessfulAt != nil {
		t.Fatalf("replacement: %+v", state)
	}
	gateway.StoreID = "other-store"
	if state := gateway.LoginState(); state.State != "scope_mismatch" {
		t.Fatalf("scope mismatch: %+v", state)
	}
}
