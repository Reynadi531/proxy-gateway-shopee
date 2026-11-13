package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const DefaultBaseURL = "https://api.telegram.org"

// Client is a minimal Telegram Bot API client for long polling and plain-text replies.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

func NewClient(token, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{token: token, baseURL: baseURL, http: &http.Client{}}
}

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Date      int64  `json:"date"`
	Text      string `json:"text"`
}

type User struct {
	ID int64 `json:"id"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// APIError is a Bot API error response.
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

// GetUpdates long polls for new messages starting at offset.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	var updates []Update
	ctx, cancel := context.WithTimeout(ctx, timeout+15*time.Second)
	defer cancel()
	err := c.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": int(timeout.Seconds()), "allowed_updates": []string{"message"}}, &updates)
	return updates, err
}

// SendMessage sends plain text; silent disables the notification sound.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, silent bool) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text, "disable_notification": silent, "link_preview_options": map[string]bool{"is_disabled": true}}, nil)
}

// DeleteMessage removes a message, used for messages holding credentials.
func (c *Client) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: invalid request", method)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error embeds the request URL, which contains the bot token.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("telegram %s: read response: %w", method, err)
	}
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return &APIError{Method: method, Code: resp.StatusCode, Description: "unreadable response"}
	}
	if !envelope.OK {
		return &APIError{Method: method, Code: envelope.ErrorCode, Description: envelope.Description, RetryAfter: time.Duration(envelope.Parameters.RetryAfter) * time.Second}
	}
	if result != nil {
		if err = json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("telegram %s: decode result: %w", method, err)
		}
	}
	return nil
}
