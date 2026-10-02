package numbers

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
)

// Watches adds a notice for each watched operator that turned rising or
// scaled (spy.watch_operators), then sends the pending ones to the ops group
// on Telegram, 3 tries each. Without a bot token or the group's chat id they
// are recorded as skipped and show only on the operator page. It returns the
// notices added.
func (r *Runner) Watches(ctx context.Context) (int64, error) {
	return r.run(ctx, JobWatches, func(ctx context.Context) (int64, error) {
		var n int64
		if err := r.db.QueryRow(ctx, `SELECT spy.watch_operators($1)`, r.cfg.Now()).Scan(&n); err != nil {
			return 0, err
		}
		return n, r.sendNotices(ctx)
	})
}

type notice struct {
	id, operator int64
	title, body  string
	attempts     int
}

func (r *Runner) sendNotices(ctx context.Context) error {
	rows, err := r.db.Query(ctx, `
		SELECT id, operator_id, title, body, attempts FROM spy.watch_notice
		WHERE status = 'pending' AND next_try_at <= now()
		ORDER BY next_try_at, id LIMIT 50`)
	if err != nil {
		return fmt.Errorf("read pending notices: %w", err)
	}
	var todo []notice
	for rows.Next() {
		var x notice
		if err := rows.Scan(&x.id, &x.operator, &x.title, &x.body, &x.attempts); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, x := range todo {
		status, errText := "sent", ""
		if r.cfg.Telegram == nil {
			status, errText = "skipped", "no Telegram bot token or ops group chat id in spy-numbers.env"
		} else if err := r.cfg.Telegram.Send(ctx, noticeText(x, r.cfg.BaseURL), false); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			status, errText = "pending", err.Error()
			if x.attempts+1 >= 3 {
				status = "failed"
			}
			r.log.Warn("watch notice not delivered", "notice", x.id, "operator", x.operator, "err", err)
		}
		if _, err := r.db.Exec(ctx, `
			UPDATE spy.watch_notice SET status = $2, attempts = attempts + 1, error = NULLIF($3, ''),
			    next_try_at = now() + interval '1 minute' * (attempts + 1),
			    sent_at = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END
			WHERE id = $1`, x.id, status, errText); err != nil {
			return fmt.Errorf("record notice: %w", err)
		}
	}
	return nil
}

// noticeText is the Telegram message for a notice: its title in bold, its
// text, and a link to the operator's Spy page when baseURL is set.
func noticeText(x notice, baseURL string) string {
	msg := "👁 <b>" + html.EscapeString(x.title) + "</b>\n" + html.EscapeString(x.body)
	if baseURL != "" {
		u := strings.TrimRight(baseURL, "/") + "/spy/operators/" + strconv.FormatInt(x.operator, 10)
		msg += "\n<a href=\"" + html.EscapeString(u) + "\">Abrir no Spy</a>"
	}
	return msg
}
