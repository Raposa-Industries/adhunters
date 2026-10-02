// Package telegram posts messages to a Telegram group through a bot: system
// messages to "AdHunters alerts", and what the team acts on (Intel's alerts,
// policy changes) to the ops group "AdHunters operation" (decision 0004); watches go through Pushcut elsewhere. observe-bot and intel-numbers
// both send through it (decision 0013).
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client posts to one chat.
type Client struct {
	Token  string
	ChatID string
	// API is Telegram's bot API; a test points it elsewhere.
	API  string
	HTTP *http.Client
}

// New returns a client for the bot token and chat id.
func New(token, chatID string) *Client {
	return &Client{Token: token, ChatID: chatID, API: "https://api.telegram.org", HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Send posts an HTML message. A silent message arrives without a sound.
func (c *Client) Send(ctx context.Context, html string, silent bool) error {
	body, _ := json.Marshal(map[string]any{
		"chat_id":                  c.ChatID,
		"text":                     html,
		"parse_mode":               "HTML",
		"disable_notification":     silent,
		"disable_web_page_preview": true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.API+"/bot"+c.Token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// The URL holds the token; never let it reach a log line.
		return fmt.Errorf("telegram: send failed: %v", redact(err, c.Token))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram: %s: %s", resp.Status, b)
	}
	return nil
}

// redact removes the bot token from an error that carries the request URL.
func redact(err error, token string) error {
	if token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "<token>"))
}
