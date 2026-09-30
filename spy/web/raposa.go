package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// raposaReadable says whether this login may read Raposa's views; without
// them the ad page leaves Raposa out rather than failing.
func (s *Server) raposaReadable(ctx context.Context) bool {
	var ok bool
	err := s.db.QueryRow(ctx, `
		SELECT to_regclass('raposa_api.investigation_v1') IS NOT NULL
		   AND has_table_privilege('raposa_api.investigation_v1', 'SELECT')
		   AND has_table_privilege('raposa_api.evidence_v1', 'SELECT')`).Scan(&ok)
	return err == nil && ok
}

// raposaFor is what Raposa knows about a creative: its investigations,
// newest first, and the landing pages and sellers they found.
func (s *Server) raposaFor(ctx context.Context, creative int) (any, error) {
	if !s.raposaReadable(ctx) {
		return map[string]any{"available": false}, nil
	}
	inv, err := s.rowsQ(ctx, `
		SELECT id, mode, origin, requested_by, requested_at, status, stage, stage_note, visits_done, visits_target,
		       is_cloaked, cloaked_confidence, completed_at
		FROM raposa_api.investigation_v1
		WHERE creative_id = $1
		ORDER BY id DESC LIMIT 10`, creative)
	if err != nil {
		return nil, err
	}
	pages, err := s.rowsQ(ctx, `
		SELECT domain, final_url, page_kind, title, checkout_platform, seller_platform, seller_account,
		       count(*) AS visits, max(recorded_at) AS last_at
		FROM raposa_api.evidence_v1
		WHERE creative_id = $1
		GROUP BY 1, 2, 3, 4, 5, 6, 7
		ORDER BY last_at DESC LIMIT 20`, creative)
	if err != nil {
		return nil, err
	}
	return map[string]any{"available": true, "investigations": inv, "pages": pages}, nil
}

// investigate asks Raposa to investigate a creative (and one of its ads):
// {"mode": "deep" | "quick", "ad_id": 123}. When one is already waiting or
// running, Raposa answers with that one. The person asking is recorded.
func (s *Server) investigate(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	// Only the pages may call this: a JSON body a form on another site
	// cannot send, and the same origin when the browser says one.
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return nil, bad("send JSON")
	}
	if o := r.Header.Get("Origin"); o != "" && o != "https://"+r.Host && o != "http://"+r.Host {
		return nil, bad("wrong origin")
	}
	var body struct {
		Mode string `json:"mode"`
		AdID *int   `json:"ad_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4096)).Decode(&body); err != nil {
		return nil, bad("the body must be JSON: %v", err)
	}
	if body.Mode == "" {
		body.Mode = "deep"
	}
	if body.Mode != "deep" && body.Mode != "quick" {
		return nil, bad("mode must be deep or quick")
	}
	var inv int64
	if err := s.db.QueryRow(r.Context(), `SELECT raposa_api.request_investigation_v1($1, $2, $3, $4)`,
		id, body.Mode, body.AdID, who(r)).Scan(&inv); err != nil {
		return nil, err
	}
	s.log.Info("investigation requested", "creative", id, "mode", body.Mode, "investigation", inv, "by", who(r))
	return map[string]any{"investigation_id": inv}, nil
}
