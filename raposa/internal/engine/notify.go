package engine

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
)

// Watches are delivered to the ops group "AdHunters operation" on Telegram
// (decision 0004): each event that some watch wants is posted to the group
// once, however many watches want it, naming who set them and linking the
// investigation's page. System alerts go to "AdHunters alerts", never here.

// delivery is one event on its way to one watch.
type delivery struct {
	EventID       int64
	WatchID       int64
	By            string
	Title         string
	Body          string
	Investigation int64
	Attempts      int16
}

// notify sends pending deliveries until ctx ends.
func (e *Engine) notify(ctx context.Context) {
	for ctx.Err() == nil {
		n, err := e.NotifyOnce(ctx)
		if err != nil && ctx.Err() == nil {
			e.log.Error("notifier", "err", err)
		}
		if n == 0 || err != nil {
			sleep(ctx, 5*time.Second)
		}
	}
}

// NotifyOnce sends the events of up to 50 pending deliveries, one message per
// event, and returns how many deliveries it handled.
func (e *Engine) NotifyOnce(ctx context.Context) (int, error) {
	rows, err := e.store.pool.Query(ctx, `
		SELECT d.event_id, d.watch_id, w.created_by, ev.title, ev.body, ev.investigation_id, d.attempts
		FROM raposa.delivery d
		JOIN raposa.watch w ON w.id = d.watch_id
		JOIN raposa.event ev ON ev.id = d.event_id
		WHERE d.status = 'pending' AND d.next_try_at <= now()
		ORDER BY d.next_try_at, d.event_id, d.watch_id
		LIMIT 50`)
	if err != nil {
		return 0, fmt.Errorf("read pending deliveries: %w", err)
	}
	var todo []delivery
	for rows.Next() {
		var d delivery
		if err := rows.Scan(&d.EventID, &d.WatchID, &d.By, &d.Title, &d.Body, &d.Investigation, &d.Attempts); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, ev := range byEvent(todo) {
		status, errText := "sent", ""
		var attempts int16
		for _, d := range ev {
			attempts = max(attempts, d.Attempts)
		}
		if e.cfg.Telegram == nil {
			status, errText = "skipped", "no Telegram bot token or ops group chat id in raposa-engine.env"
		} else if err := e.cfg.Telegram.Send(ctx, watchText(ev, e.cfg.BaseURL), false); err != nil {
			errText = err.Error()
			status = "pending"
			if attempts+1 >= 3 {
				status = "failed"
			}
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		// Every pending delivery of the event shares the one message, those
		// past this batch's limit included.
		tag, err := e.store.pool.Exec(ctx, `
			UPDATE raposa.delivery
			SET status = $2, attempts = attempts + 1, error = NULLIF($3, ''),
			    next_try_at = now() + interval '1 minute' * (attempts + 1),
			    sent_at = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END
			WHERE event_id = $1 AND status = 'pending'`, ev[0].EventID, status, clean(errText))
		if err != nil {
			return 0, fmt.Errorf("record delivery: %w", err)
		}
		if status != "pending" {
			e.m.deliveries.WithLabelValues(status).Add(float64(tag.RowsAffected()))
		}
	}
	return len(todo), nil
}

// byEvent groups deliveries by their event, in the order the events first
// appear.
func byEvent(ds []delivery) [][]delivery {
	var out [][]delivery
	at := map[int64]int{}
	for _, d := range ds {
		i, ok := at[d.EventID]
		if !ok {
			i = len(out)
			at[d.EventID] = i
			out = append(out, nil)
		}
		out[i] = append(out[i], d)
	}
	return out
}

// watchText is the Telegram message for one event: its title in bold, its
// text, who watches it, and a link to the investigation when baseURL is set.
func watchText(ev []delivery, baseURL string) string {
	d := ev[0]
	msg := "🦊 <b>" + html.EscapeString(d.Title) + "</b>"
	if d.Body != "" {
		msg += "\n" + html.EscapeString(d.Body)
	}
	var who []string
	seen := map[string]bool{}
	for _, x := range ev {
		if x.By != "" && !seen[x.By] {
			seen[x.By] = true
			who = append(who, x.By)
		}
	}
	if len(who) > 0 {
		msg += "\nAcompanhando: " + html.EscapeString(strings.Join(who, ", "))
	}
	if baseURL != "" {
		u := strings.TrimRight(baseURL, "/") + "/i/" + strconv.FormatInt(d.Investigation, 10)
		msg += "\n<a href=\"" + html.EscapeString(u) + "\">Abrir no Raposa</a>"
	}
	return msg
}
