package engine

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

// Watches are delivered through Pushcut: each watch names a notification the
// person defined in their Pushcut app, and every event it wants is POSTed to
// it once. Pushcut is for watches only; system alerts go to Telegram, never
// here.

// pushcutURL is Pushcut's API. A test points it elsewhere.
var pushcutURL = "https://api.pushcut.io/v1/notifications/"

// delivery is one event on its way to one watch.
type delivery struct {
	EventID       int64
	WatchID       int64
	Notification  string
	Title         string
	Body          string
	Investigation int64
	Attempts      int16
}

// notify sends pending deliveries until ctx ends.
func (e *Engine) notify(ctx context.Context) {
	client := &http.Client{Timeout: 15 * time.Second}
	for ctx.Err() == nil {
		n, err := e.NotifyOnce(ctx, client)
		if err != nil && ctx.Err() == nil {
			e.log.Error("notifier", "err", err)
		}
		if n == 0 || err != nil {
			sleep(ctx, 5*time.Second)
		}
	}
}

// NotifyOnce sends up to 20 pending deliveries and returns how many it
// handled.
func (e *Engine) NotifyOnce(ctx context.Context, client *http.Client) (int, error) {
	rows, err := e.store.pool.Query(ctx, `
		SELECT d.event_id, d.watch_id, w.pushcut_notification, ev.title, ev.body, ev.investigation_id, d.attempts
		FROM raposa.delivery d
		JOIN raposa.watch w ON w.id = d.watch_id
		JOIN raposa.event ev ON ev.id = d.event_id
		WHERE d.status = 'pending' AND d.next_try_at <= now()
		ORDER BY d.next_try_at, d.event_id
		LIMIT 20`)
	if err != nil {
		return 0, fmt.Errorf("read pending deliveries: %w", err)
	}
	var todo []delivery
	for rows.Next() {
		var d delivery
		if err := rows.Scan(&d.EventID, &d.WatchID, &d.Notification, &d.Title, &d.Body, &d.Investigation, &d.Attempts); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, d := range todo {
		status, errText := "sent", ""
		switch {
		case e.cfg.PushcutKey == "":
			status, errText = "skipped", "no Pushcut key on this box"
		default:
			if err := e.pushcut(ctx, client, d); err != nil {
				errText = err.Error()
				status = "pending"
				if d.Attempts+1 >= 3 {
					status = "failed"
				}
			}
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if _, err := e.store.pool.Exec(ctx, `
			UPDATE raposa.delivery
			SET status = $3, attempts = attempts + 1, error = NULLIF($4, ''),
			    next_try_at = now() + interval '1 minute' * (attempts + 1),
			    sent_at = CASE WHEN $3 = 'sent' THEN now() ELSE sent_at END
			WHERE event_id = $1 AND watch_id = $2`, d.EventID, d.WatchID, status, clean(errText)); err != nil {
			return 0, fmt.Errorf("record delivery: %w", err)
		}
		if status != "pending" {
			e.m.deliveries.WithLabelValues(status).Inc()
		}
	}
	return len(todo), nil
}

// pushcut sends one notification.
func (e *Engine) pushcut(ctx context.Context, client *http.Client, d delivery) error {
	body, err := json.Marshal(map[string]any{
		"title": d.Title,
		"text":  d.Body,
		"input": fmt.Sprintf("%d", d.Investigation),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pushcutURL+url.PathEscape(d.Notification), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("API-Key", e.cfg.PushcutKey)
	resp, err := client.Do(req)
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
