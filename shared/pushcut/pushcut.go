// Package pushcut sends watch notifications to people's phones through
// Pushcut (decision 0004): each watch names a notification the person made
// in their Pushcut app, and one POST shows it. Pushcut is for watches only;
// system alerts go to Telegram (shared/telegram), never here. Raposa's
// watches still have their own copy (raposa/internal/engine/notify.go).
package pushcut

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client sends with one API key.
type Client struct {
	Key string
	// API is Pushcut's notifications endpoint; a test points it elsewhere.
	API  string
	HTTP *http.Client
}

// New returns a client for the API key.
func New(key string) *Client {
	return &Client{Key: key, API: "https://api.pushcut.io/v1/notifications/", HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Message is what the phone shows. Input reaches the notification's
// actions (an id to open, say).
type Message struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Input string `json:"input,omitempty"`
}

// Send shows m through the named notification.
func (c *Client) Send(ctx context.Context, notification string, m Message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.API+url.PathEscape(notification), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("API-Key", c.Key)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("pushcut: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("pushcut answered %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
