package judge

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// StatusWords are Taboola's campaign statuses in the words Realize's
// "Delivery Status" column uses. DELETED and GROUP_DELETED are ours: the
// campaign left the list, or its group left the group list.
var StatusWords = map[string]string{
	"RUNNING":            "Running",
	"PAUSED":             "Paused",
	"PENDING_APPROVAL":   "Pending approval",
	"PENDING_START_DATE": "Scheduled",
	"REJECTED":           "Rejected",
	"DEPLETED":           "Depleted (budget spent)",
	"DEPLETED_MONTHLY":   "Depleted (monthly budget spent)",
	"EXPIRED":            "Expired",
	"TERMINATED":         "Terminated",
	"FROZEN":             "Frozen",
	"DELETED":            "Deleted",
	"GROUP_DELETED":      "Campaign group deleted",
}

// StatusWord is a status for people; an unknown one as Taboola wrote it.
func StatusWord(s string) string {
	if w, ok := StatusWords[s]; ok {
		return w
	}
	if s == "" {
		return "new"
	}
	return strings.ReplaceAll(s, "_", " ")
}

// FindStatusChanges records every delivery status change not recorded yet:
// each status after a campaign's first, and the first one too when the
// campaign appeared in an account Intel was already reading (a new
// campaign). The first list of an account only sets where each starts.
// It returns how many it recorded.
func FindStatusChanges(ctx context.Context, db *pgxpool.Pool, now time.Time) (int64, error) {
	tag, err := db.Exec(ctx, `
		WITH seq AS (
			SELECT s.campaign_id, s.account, s.status, s.valid_from,
			       lag(s.status) OVER (PARTITION BY s.campaign_id ORDER BY s.valid_from) AS prev,
			       min(s.valid_from) OVER (PARTITION BY s.account) AS account_first
			FROM intel.tb_campaign_status s
		)
		INSERT INTO intel.status_change (campaign_id, account, old_status, new_status, changed_at, found_at)
		SELECT campaign_id, account, COALESCE(prev, ''), status, valid_from, $1
		FROM seq
		WHERE prev IS DISTINCT FROM status AND (prev IS NOT NULL OR valid_from > account_first)
		ON CONFLICT (campaign_id, changed_at) DO NOTHING`, now)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// SendStatusChanges sends the changes not sent yet as one message per
// round, newest last. Changes older than status_alert_max_age_hours (a
// reload or a first run finds old ones) and changes away from Deleted or a
// deleted group are marked skipped instead. Without a sender nothing is
// marked, so they go once Telegram is set.
func SendStatusChanges(ctx context.Context, db *pgxpool.Pool, log *slog.Logger, s Settings, now time.Time, send Sender, baseURL string) (int, error) {
	maxAge := time.Duration(s.get("status_alert_max_age_hours", 6) * float64(time.Hour))
	// Deleted is final in Taboola, and so is a deleted group: a change away
	// from either means Intel took one as gone too early, so it is not news.
	if _, err := db.Exec(ctx, `UPDATE intel.status_change SET skipped = true
		WHERE sent_at IS NULL AND NOT skipped AND (changed_at < $1 OR old_status IN ('DELETED', 'GROUP_DELETED'))`, now.Add(-maxAge)); err != nil {
		return 0, err
	}
	if send == nil {
		return 0, nil
	}
	rows, err := db.Query(ctx, `
		SELECT c.id, c.campaign_id, c.account, COALESCE(t.group_id, 0), COALESCE(t.name, ''), c.old_status, c.new_status, c.changed_at
		FROM intel.status_change c LEFT JOIN intel.tb_campaign t ON t.campaign_id = c.campaign_id
		WHERE c.sent_at IS NULL AND NOT c.skipped
		ORDER BY c.changed_at, c.id
		LIMIT 50`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []int64
	var lines []string
	base := strings.TrimRight(baseURL, "/")
	for rows.Next() {
		var id, campaign, group int64
		var account, name, old, cur string
		var at time.Time
		if err := rows.Scan(&id, &campaign, &account, &group, &name, &old, &cur, &at); err != nil {
			return 0, err
		}
		ids = append(ids, id)
		label := name
		if label == "" {
			label = fmt.Sprintf("campaign %d", campaign)
		}
		change := StatusWord(old) + " → " + StatusWord(cur)
		if old == "" {
			change = "new, " + StatusWord(cur)
		}
		line := fmt.Sprintf("%s (%s · %d): <b>%s</b>", html.EscapeString(label), html.EscapeString(account), campaign, html.EscapeString(change))
		if base != "" && cur != "DELETED" {
			g := "-"
			if group != 0 {
				g = fmt.Sprint(group)
			}
			line += fmt.Sprintf("\n%s/intel/taboola/%s/g/%s/c/%d", base, url.PathEscape(account), g, campaign)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	title := "Delivery status changed"
	if len(ids) > 1 {
		title = fmt.Sprintf("Delivery status: %d changes", len(ids))
	}
	msg := "<b>Intel · " + title + "</b>\n" + strings.Join(lines, "\n")
	if err := send.Send(ctx, msg, false); err != nil {
		log.Error("status changes not sent", "err", err, "count", len(ids))
		return 0, nil
	}
	if _, err := db.Exec(ctx, `UPDATE intel.status_change SET sent_at = $2 WHERE id = ANY($1)`, ids, now); err != nil {
		return 0, err
	}
	return len(ids), nil
}
