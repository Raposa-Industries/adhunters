package numbers

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Raposa-Industries/adhunters/shared/pushcut"
)

// Watches adds a notice for each watched operator that turned rising or
// scaled (spy.watch_operators), then sends the pending ones through Pushcut,
// 3 tries each. Without a key or a notification name they are recorded as
// skipped and show only on the operator page. It returns the notices added.
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
		if r.cfg.Pushcut == nil || r.cfg.Notification == "" {
			status, errText = "skipped", "no Pushcut key or notification name in spy-numbers.env"
		} else if err := r.cfg.Pushcut.Send(ctx, r.cfg.Notification, pushcut.Message{
			Title: x.title, Text: x.body, Input: strconv.FormatInt(x.operator, 10),
		}); err != nil {
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
