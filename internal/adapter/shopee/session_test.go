package shopee

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pg-shopee/internal/domain"

	sdk "github.com/hirotomasato/paygateme/shopee"
)

// fakeShopee answers the account, partner and ShopeePay endpoints used by the
// OTP login and silent renewal flows.
type fakeShopee struct {
	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
	alive       bool
	onLoginToc  func()
	calls       []string
}

func (f *fakeShopee) install(t *testing.T) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(f.roundTrip)
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func (f *fakeShopee) roundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.URL.Path)
	var body map[string]any
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
	}
	header := http.Header{"Content-Type": []string{"application/json"}}
	respond := func(payload string) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
	}
	switch r.URL.Host + r.URL.Path {
	case "partner.business.accounts.shopee.co.id/login":
		header.Add("Set-Cookie", "SPC_CLIENTID=client-id; Path=/; Secure")
		return respond(`<html></html>`)
	case "df.infra.sz.shopee.co.id/v2/shpsec/web/report":
		return respond(`{"code":0,"data":{"riskToken":"risk-token"}}`)
	case "partner.business.accounts.shopee.co.id" + sdk.EPCheckPasswordMigration,
		"partner.business.accounts.shopee.co.id" + sdk.EPCheckAccountByPassword:
		return respond(`{"error":0,"data":{}}`)
	case "partner.business.accounts.shopee.co.id" + sdk.EPAuthenticateByPassword:
		return respond(`{"error":0}`)
	case "partner.business.accounts.shopee.co.id" + sdk.EPOtpSettings:
		return respond(`{"error":0,"data":{"default_channel":3,"available_channel_list":[3]}}`)
	case "partner.business.accounts.shopee.co.id" + sdk.EPSendOtp:
		return respond(`{"error":0,"data":{"seed":"seed"}}`)
	case "partner.business.accounts.shopee.co.id" + sdk.EPVerifyOtp:
		if body["otp"] != "123456" {
			return respond(`{"error":2,"msg":"error_otp_invalid"}`)
		}
		return respond(`{"error":0,"data":{"otp_token":"otp-token"}}`)
	case "partner.business.accounts.shopee.co.id" + sdk.EPAuthenticateByOtp:
		return respond(`{"error":0,"data":{"toc_nonce":"toc-nonce","toc_account":{"userid":7}}}`)
	case "partner.business.accounts.shopee.co.id" + sdk.EPLoginStatus:
		if !f.alive {
			return respond(`{"error":1,"msg":"not logged in"}`)
		}
		return respond(`{"error":0,"data":{}}`)
	case "partner.business.accounts.shopee.co.id" + sdk.EPLoginToc:
		if f.onLoginToc != nil {
			f.onLoginToc()
		}
		return respond(`{"error":0,"data":{"nonce":"auth-code"}}`)
	case "partner.business.accounts.shopee.co.id" + sdk.EPAccountLoginToken, "partner.shopee.co.id" + sdk.EPAccountLogin:
		return respond(`<html></html>`)
	case "partner.shopee.co.id" + sdk.EPAccountTobAuth:
		cookie := &http.Cookie{Name: sdk.LiveTokenCookie, Value: testJWT(f.token, "42", time.Now().Add(1000*24*time.Hour)), Domain: "shopee.co.id", Path: "/", Secure: true, Expires: f.tokenExpiry}
		header.Add("Set-Cookie", cookie.String())
		return respond(`<html></html>`)
	case "api.partner.shopee.co.id" + sdk.EPMerchantDetect:
		return respond(`{"code":0,"data":{"selectMerchant":{"merchantList":[{"merchantId":100,"merchantName":"Toko","staffTobUid":42,"isActive":true}]}}}`)
	case "api.partner.shopee.co.id" + sdk.EPUserInfo:
		return respond(`{"code":0,"data":{"merchantId":"100","merchantName":"Toko Test","store_id":"200"}}`)
	case "shopeepay.shopee.co.id" + sdk.EPStores:
		return respond(`{"code":0,"data":{"list":[{"storeId":"200","storeName":"Main"}],"storeCount":1}}`)
	}
	return &http.Response{StatusCode: 404, Header: header, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
}

func testJWT(token, userID string, exp time.Time) string {
	payload, _ := json.Marshal(map[string]any{"token": token, "userid": userID, "businessId": "business", "exp": exp.Unix()})
	return "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func readSessionFile(t *testing.T, path string) sdk.Session {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var session sdk.Session
	if err = json.Unmarshal(data, &session); err != nil {
		t.Fatal(err)
	}
	return session
}

func TestSessionManagerOTPLoginAndRenewal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	fake := &fakeShopee{token: "first", tokenExpiry: time.Now().Add(24 * time.Hour).Truncate(time.Second), alive: true}
	fake.install(t)
	manager := NewSessionManager(path, "100", "200")

	if _, err := manager.Status(); !errors.Is(err, domain.ErrReloginRequired) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := manager.VerifyOTP(ctx, "123456"); !errors.Is(err, domain.ErrNoPendingLogin) {
		t.Fatalf("verify without request: %v", err)
	}
	request, err := manager.RequestOTP(ctx, "081234567890", "")
	if err != nil {
		t.Fatal(err)
	}
	if request.Channel != 3 || request.ExpiresAt.IsZero() {
		t.Fatalf("otp request: %+v", request)
	}
	if _, err = manager.VerifyOTP(ctx, "000000"); err == nil || !strings.Contains(err.Error(), "error_otp_invalid") {
		t.Fatalf("wrong otp: %v", err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("wrong OTP wrote a session")
	}
	info, err := manager.VerifyOTP(ctx, "123456")
	if err != nil {
		t.Fatal(err)
	}
	if info.MerchantID != "100" || info.MerchantName != "Toko Test" || info.StoreID != "200" || !info.Renewable || !info.TokenPresent || !info.ExpiresAt.Equal(fake.tokenExpiry) {
		t.Fatalf("login info: %+v", info)
	}
	stat, err := os.Stat(path)
	if err != nil || stat.Mode().Perm() != 0o600 {
		t.Fatalf("session file mode: %v %v", stat, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
	if _, err = manager.VerifyOTP(ctx, "123456"); !errors.Is(err, domain.ErrNoPendingLogin) {
		t.Fatalf("login was reusable: %v", err)
	}

	// The gateway accepts the written session.
	gateway := &Gateway{SessionFile: path, MerchantID: "100", StoreID: "200", PollInterval: time.Second}
	if state := gateway.LoginState(); !state.Authenticated || state.State != "unverified" {
		t.Fatalf("gateway state: %+v", state)
	}

	fake.token, fake.tokenExpiry = "renewed", time.Now().Add(48*time.Hour).Truncate(time.Second)
	info, err = manager.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ExpiresAt.Equal(fake.tokenExpiry) {
		t.Fatalf("renewed expiry %v, want %v", info.ExpiresAt, fake.tokenExpiry)
	}
	jar := sdk.NewCookieJar()
	jar.Restore(readSessionFile(t, path).Cookies)
	if credential, err := sdk.ReadMerchantCredential(jar); err != nil || credential.Token != "renewed" {
		t.Fatalf("renewed credential: %+v %v", credential, err)
	}

	// A session replaced during renewal is kept.
	replacement := []byte(`{"merchant":{"id":"100"},"storeId":"200"}`)
	fake.onLoginToc = func() { _ = os.WriteFile(path, replacement, 0o600) }
	if _, err = manager.Refresh(ctx); !errors.Is(err, domain.ErrSessionReplaced) {
		t.Fatalf("replaced during renewal: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != string(replacement) {
		t.Fatal("renewal overwrote a replaced session")
	}
	if _, err = manager.Refresh(ctx); !errors.Is(err, domain.ErrReloginRequired) {
		t.Fatalf("session without renewal credential: %v", err)
	}
	if info, err = manager.Status(); err != nil || info.TokenPresent || info.Renewable {
		t.Fatalf("replacement status: %+v %v", info, err)
	}
}

func TestSessionManagerRenewalNeedsOTPWhenAccountSessionEnded(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "session.json")
	fake := &fakeShopee{token: "first", tokenExpiry: time.Now().Add(24 * time.Hour), alive: false}
	fake.install(t)
	manager := NewSessionManager(path, "100", "200")
	if _, err := manager.RequestOTP(ctx, "081234567890", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.VerifyOTP(ctx, "123456"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	_, err := manager.Refresh(ctx)
	if !errors.Is(err, domain.ErrReloginRequired) {
		t.Fatalf("dead account session: %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("failed renewal changed the session file")
	}
}

func TestSessionManagerStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	manager := NewSessionManager(path, "100", "200")
	write := func(session sdk.Session) {
		t.Helper()
		data, _ := json.Marshal(session)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Status(); !errors.Is(err, domain.ErrReloginRequired) {
		t.Fatalf("invalid JSON: %v", err)
	}
	cookieExpiry := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	tokenCookie := sdk.Cookie{Name: sdk.LiveTokenCookie, Value: testJWT("t", "42", time.Now().Add(time.Hour)), Domain: "shopee.co.id", Path: "/", Secure: true, Expires: cookieExpiry.UnixMilli()}
	write(sdk.Session{AccountID: "42", Merchant: sdk.MerchantSummary{ID: "999"}, StoreID: "200", Cookies: []sdk.Cookie{tokenCookie}})
	if _, err := manager.Status(); !errors.Is(err, domain.ErrReloginRequired) {
		t.Fatalf("scope mismatch: %v", err)
	}
	// An expired cookie still reports its expiry rather than a missing token.
	write(sdk.Session{AccountID: "42", Merchant: sdk.MerchantSummary{ID: "100"}, StoreID: "200", Cookies: []sdk.Cookie{tokenCookie}, SwitchCredential: &sdk.SwitchCredential{TocNonce: "n"}})
	info, err := manager.Status()
	if err != nil || !info.TokenPresent || !info.Renewable || !info.ExpiresAt.Equal(cookieExpiry) {
		t.Fatalf("expired cookie: %+v %v", info, err)
	}
	// The JWT expiry applies when it is earlier than the cookie expiry.
	jwtExpiry := time.Now().Add(time.Hour).Truncate(time.Second)
	tokenCookie.Value, tokenCookie.Expires = testJWT("t", "42", jwtExpiry), time.Now().Add(24*time.Hour).UnixMilli()
	write(sdk.Session{AccountID: "42", Merchant: sdk.MerchantSummary{ID: "100"}, StoreID: "200", Cookies: []sdk.Cookie{tokenCookie}})
	if info, err = manager.Status(); err != nil || !info.ExpiresAt.Equal(jwtExpiry) || info.Renewable {
		t.Fatalf("jwt expiry: %+v %v", info, err)
	}
	// A token for another account counts as missing.
	write(sdk.Session{AccountID: "43", Merchant: sdk.MerchantSummary{ID: "100"}, StoreID: "200", Cookies: []sdk.Cookie{tokenCookie}})
	if info, err = manager.Status(); err != nil || info.TokenPresent {
		t.Fatalf("foreign token: %+v %v", info, err)
	}
}

func TestClassifyDropsCauses(t *testing.T) {
	err := classify(errors.New("Post \"https://x/?toc_nonce=secret\": dial tcp: refused"), false)
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("leaked cause: %v", err)
	}
	if err = classify(context.DeadlineExceeded, false); strings.Contains(err.Error(), "http") {
		t.Fatalf("timeout: %v", err)
	}
}
