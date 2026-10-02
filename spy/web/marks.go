package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Raposa-Industries/adhunters/shared/access"
)

// What people mark from the pages: an operator hidden, watched or given a
// nickname (spy_api.mark_operator_v1), and a creative's vertical set by hand
// (spy_api.fix_vertical_v1). Both write through spy_api functions granted to
// the spy_web login alone.

// postJSON reads a small JSON body. Only the pages may post: a JSON body a
// form on another site cannot send, and the same origin when the browser
// says one.
func postJSON(r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return bad("send JSON")
	}
	if o := r.Header.Get("Origin"); o != "" && o != "https://"+r.Host && o != "http://"+r.Host {
		return bad("wrong origin")
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4096)).Decode(v); err != nil {
		return bad("the body must be JSON: %v", err)
	}
	return nil
}

// marksFor is an operator's mark (nil when it has none) and its latest watch
// notices.
func (s *Server) marksFor(ctx context.Context, id int) (any, []map[string]any, error) {
	marks, err := s.rows(ctx, `
		SELECT hidden, watched, nickname, watched_since, made_by, updated_at
		FROM spy_api.operator_mark_v1 WHERE operator_id = $1`, id)
	if err != nil {
		return nil, nil, err
	}
	notices, err := s.rows(ctx, `
		SELECT id, reason, at, title, body, status, error, sent_at
		FROM spy_api.watch_notice_v1 WHERE operator_id = $1
		ORDER BY at DESC, id DESC LIMIT 20`, id)
	if err != nil {
		return nil, nil, err
	}
	var mark any
	if len(marks) > 0 {
		mark = marks[0]
	}
	return mark, notices, nil
}

// markOperator changes what people marked on an operator:
// {"hidden": true, "watched": true, "nickname": "Memo"}; a field left out
// stays as it is, an empty nickname takes it away.
func (s *Server) markOperator(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		Hidden   *bool   `json:"hidden"`
		Watched  *bool   `json:"watched"`
		Nickname *string `json:"nickname"`
	}
	if err := postJSON(r, &body); err != nil {
		return nil, err
	}
	ctx := r.Context()
	if _, err := s.row(ctx, `SELECT id FROM spy_api.operator_v1 WHERE id = $1`, id); err != nil {
		return nil, err
	}
	var hidden, watched bool
	var nickname *string
	err = s.db.QueryRow(ctx, `SELECT hidden, watched, nickname FROM spy_api.operator_mark_v1 WHERE operator_id = $1`, id).
		Scan(&hidden, &watched, &nickname)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if body.Hidden != nil {
		hidden = *body.Hidden
	}
	if body.Watched != nil {
		watched = *body.Watched
	}
	if body.Nickname != nil {
		n := strings.Join(strings.Fields(*body.Nickname), " ")
		if utf8.RuneCountInString(n) > 60 {
			return nil, bad("a nickname has 60 characters at most")
		}
		nickname = &n
	}
	if _, err := s.db.Exec(ctx, `SELECT spy_api.mark_operator_v1($1, $2, $3, $4, $5)`,
		id, hidden, watched, nickname, access.Email(r)); err != nil {
		return nil, err
	}
	s.cache.clear()
	s.log.Info("operator marked", "operator", id, "hidden", hidden, "watched", watched, "nickname", nickname != nil && *nickname != "",
		"by", access.Email(r))
	mark, notices, err := s.marksFor(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"mark": mark, "notices": notices}, nil
}

// fixVertical sets a creative's vertical by hand: {"vertical_id": "joint_pain"}
// (an id from the list), or {"vertical_id": ""} to give it back to the
// classifier.
func (s *Server) fixVertical(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		VerticalID string `json:"vertical_id"`
	}
	if err := postJSON(r, &body); err != nil {
		return nil, err
	}
	var cat, vert *string
	if body.VerticalID != "" {
		for _, c := range s.list.Categories {
			for _, v := range c.Verticals {
				if v.ID == body.VerticalID {
					cat, vert = &c.ID, &v.ID
				}
			}
		}
		if vert == nil {
			return nil, bad("%q is not a vertical of the list", body.VerticalID)
		}
	}
	ctx := r.Context()
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tracks_api.creative_v1 WHERE id = $1)`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, errNotFound
	}
	if _, err := s.db.Exec(ctx, `SELECT spy_api.fix_vertical_v1($1, $2, $3, $4)`, id, cat, vert, access.Email(r)); err != nil {
		return nil, err
	}
	s.cache.clear()
	s.log.Info("vertical set by hand", "creative", id, "vertical", body.VerticalID, "by", access.Email(r))
	k, err := s.row(ctx, `
		SELECT category_id, vertical_id, confidence AS vertical_confidence, unsure AS vertical_unsure, source AS vertical_source
		FROM spy_api.creative_class_v1 WHERE creative_id = $1`, id)
	if errors.Is(err, errNotFound) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	s.name([]map[string]any{k}, "category_id", "vertical_id")
	return k, nil
}
