package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"pg-shopee/internal/domain"
)

type Sender struct {
	Secret         string
	AllowedOrigins map[string]bool
	Client         *http.Client
}

func New(secret string, origins map[string]bool) *Sender {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Sender{Secret: secret, AllowedOrigins: origins, Client: &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (s *Sender) Send(ctx context.Context, d domain.Delivery) error {
	u, err := url.Parse(d.URL)
	if err != nil || u.User != nil || u.Fragment != "" || !s.AllowedOrigins[u.Scheme+"://"+u.Host] {
		return fmt.Errorf("webhook origin is no longer allowed")
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(s.Secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(d.Payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(d.Payload))
	if err != nil {
		return fmt.Errorf("invalid webhook request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-ID", d.ID)
	req.Header.Set("X-Webhook-Timestamp", timestamp)
	req.Header.Set("X-Webhook-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook transport failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}
