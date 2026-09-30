package judge

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LinkMoves copies the moves Launch published into intel.campaign_link: a
// campaign moved to another group is a copy with a new id (Taboola refuses
// to change a campaign's group, seen 2026-09-30), and the copy carries the
// old one's history on. Until Launch publishes launch_api.campaign_move_v1
// there is nothing to read, and nothing happens. The view lists only finished moves.
func LinkMoves(ctx context.Context, db *pgxpool.Pool) (int64, error) {
	var exists bool
	if err := db.QueryRow(ctx, `SELECT to_regclass('launch_api.campaign_move_v1') IS NOT NULL`).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	tag, err := db.Exec(ctx, `
		INSERT INTO intel.campaign_link (old_campaign_id, new_campaign_id, account, linked_at, source)
		SELECT old_campaign_id, new_campaign_id, account, moved_at, 'launch' FROM launch_api.campaign_move_v1
		WHERE old_campaign_id IS NOT NULL AND new_campaign_id IS NOT NULL
		ON CONFLICT DO NOTHING`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
