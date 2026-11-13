package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"

func TestClientCalls(t *testing.T) {
	var got []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params map[string]any
		_ = json.NewDecoder(r.Body).Decode(&params)
		got = append(got, params)
		switch r.URL.Path {
		case "/bot" + testToken + "/getUpdates":
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":5,"message":{"message_id":9,"from":{"id":77},"chat":{"id":77,"type":"private"},"date":1700000000,"text":"/status"}}]}`))
		case "/bot" + testToken + "/sendMessage":
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":7}}`))
		case "/bot" + testToken + "/deleteMessage":
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient(testToken, server.URL)
	ctx := context.Background()

	updates, err := client.GetUpdates(ctx, 5, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].UpdateID != 5 || updates[0].Message.From.ID != 77 || updates[0].Message.Chat.Type != "private" || updates[0].Message.Text != "/status" {
		t.Fatalf("updates: %+v", updates)
	}
	if got[0]["offset"] != float64(5) || got[0]["timeout"] != float64(1) {
		t.Fatalf("getUpdates params: %v", got[0])
	}

	err = client.SendMessage(ctx, 77, "hello", true)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 429 || apiErr.RetryAfter != 7*time.Second {
		t.Fatalf("sendMessage error: %v", err)
	}
	if got[1]["chat_id"] != float64(77) || got[1]["text"] != "hello" || got[1]["disable_notification"] != true {
		t.Fatalf("sendMessage params: %v", got[1])
	}

	if err = client.DeleteMessage(ctx, 77, 9); err != nil {
		t.Fatal(err)
	}
	if got[2]["message_id"] != float64(9) {
		t.Fatalf("deleteMessage params: %v", got[2])
	}
}

func TestClientErrorsDoNotLeakToken(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	err := NewClient(testToken, url).SendMessage(context.Background(), 1, "x", false)
	if err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "ABCDEFGHIJ") {
		t.Fatalf("transport error: %v", err)
	}
}
