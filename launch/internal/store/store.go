// Package store is Launch's own records in the launch schema: presets,
// pairs, History, moves, drafts, the ads it made and other services'
// requests (migrations/sql). The campaigns
// themselves live on the ad network.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store reads and writes the launch schema.
type Store struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

// ErrNotFound is a row that is not there.
var ErrNotFound = errors.New("store: not found")

// Preset is fields people saved to fill a new group or pair. Fields is the
// page's own JSON (network.Settings for a campaign, network.NewGroup for a
// group); only the fields a person set are in it.
type Preset struct {
	ID        int64           `json:"id"`
	Level     string          `json:"level"` // group or campaign
	Network   string          `json:"network"`
	Account   string          `json:"account"` // "" for every account
	Name      string          `json:"name"`
	Fields    json.RawMessage `json:"fields"`
	MadeBy    string          `json:"made_by"`
	MadeAt    time.Time       `json:"made_at"`
	ChangedBy string          `json:"changed_by"`
	ChangedAt time.Time       `json:"changed_at"`
	Used      int             `json:"used"` // pairs made from it
}

// Presets lists a network's presets for an account (and those for every
// account), campaign presets first, then by name.
func (s *Store) Presets(ctx context.Context, network, account string) ([]Preset, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id, p.level, p.network, p.account, p.name, p.fields, p.made_by, p.made_at, p.changed_by, p.changed_at,
		       (SELECT count(*) FROM launch.pair r WHERE r.preset_id = p.id)
		FROM launch.preset p
		WHERE p.network = $1 AND ($2 = '' OR p.account IN ('', $2))
		ORDER BY p.level DESC, lower(p.name), p.account`, network, account)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Preset, error) {
		var p Preset
		err := r.Scan(&p.ID, &p.Level, &p.Network, &p.Account, &p.Name, &p.Fields, &p.MadeBy, &p.MadeAt, &p.ChangedBy, &p.ChangedAt, &p.Used)
		return p, err
	})
}

// ErrNameTaken is a preset name already used at that level and account.
var ErrNameTaken = errors.New("store: preset name taken")

// SavePreset adds a preset (ID 0) or changes one, and returns its id.
func (s *Store) SavePreset(ctx context.Context, p Preset, who string) (int64, error) {
	var id int64
	var err error
	if p.ID == 0 {
		err = s.db.QueryRow(ctx, `
			INSERT INTO launch.preset (level, network, account, name, fields, made_by, changed_by)
			VALUES ($1, $2, $3, $4, $5, $6, $6) RETURNING id`,
			p.Level, p.Network, p.Account, p.Name, p.Fields, who).Scan(&id)
	} else {
		err = s.db.QueryRow(ctx, `
			UPDATE launch.preset SET account = $2, name = $3, fields = $4, changed_by = $5, changed_at = now()
			WHERE id = $1 RETURNING id`, p.ID, p.Account, p.Name, p.Fields, who).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if isUnique(err) {
		return 0, ErrNameTaken
	}
	return id, err
}

// DeletePreset removes a preset; the pairs made from it keep existing.
func (s *Store) DeletePreset(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM launch.preset WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Pair is a desktop and a mobile campaign made together.
type Pair struct {
	ID        int64     `json:"id"`
	Network   string    `json:"network"`
	Account   string    `json:"account"`
	GroupID   string    `json:"group_id"`
	Name      string    `json:"name"`
	DesktopID string    `json:"desktop_id"`
	MobileID  string    `json:"mobile_id"`
	PresetID  *int64    `json:"preset_id,omitempty"`
	MadeBy    string    `json:"made_by"`
	MadeAt    time.Time `json:"made_at"`
}

// AddPair records a pair and returns its id.
func (s *Store) AddPair(ctx context.Context, p Pair) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `
		INSERT INTO launch.pair (network, account, group_id, name, desktop_id, mobile_id, preset_id, made_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		p.Network, p.Account, p.GroupID, p.Name, p.DesktopID, p.MobileID, p.PresetID, p.MadeBy).Scan(&id)
	return id, err
}

// SetPairCampaign fills one side of a pair once that campaign exists.
func (s *Store) SetPairCampaign(ctx context.Context, id int64, desktop bool, campaign string) error {
	col := "mobile_id"
	if desktop {
		col = "desktop_id"
	}
	_, err := s.db.Exec(ctx, `UPDATE launch.pair SET `+col+` = $2 WHERE id = $1`, id, campaign)
	return err
}

// Pairs lists an account's pairs, newest first.
func (s *Store) Pairs(ctx context.Context, network, account string) ([]Pair, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, network, account, group_id, name, desktop_id, mobile_id, preset_id, made_by, made_at
		FROM launch.pair WHERE network = $1 AND account = $2 ORDER BY made_at DESC, id DESC`, network, account)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Pair, error) {
		var p Pair
		err := r.Scan(&p.ID, &p.Network, &p.Account, &p.GroupID, &p.Name, &p.DesktopID, &p.MobileID, &p.PresetID, &p.MadeBy, &p.MadeAt)
		return p, err
	})
}

// Change is one row of History.
type Change struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	Who        string          `json:"who"`
	AskedBy    string          `json:"asked_by"`
	Network    string          `json:"network"`
	Account    string          `json:"account"`
	GroupID    string          `json:"group_id"`
	CampaignID string          `json:"campaign_id"`
	Kind       string          `json:"kind"`
	Summary    string          `json:"summary"`
	Before     json.RawMessage `json:"before,omitempty"`
	After      json.RawMessage `json:"after,omitempty"`
	Result     string          `json:"result"`
	Problems   []string        `json:"problems"`
}

// Record adds a row to History and returns its id.
func (s *Store) Record(ctx context.Context, c Change) (int64, error) {
	if c.Problems == nil {
		c.Problems = []string{}
	}
	var id int64
	err := s.db.QueryRow(ctx, `
		INSERT INTO launch.change (who, asked_by, network, account, group_id, campaign_id, kind, summary, before, after, result, problems)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		c.Who, c.AskedBy, c.Network, c.Account, c.GroupID, c.CampaignID, c.Kind, c.Summary,
		nullJSON(c.Before), nullJSON(c.After), c.Result, c.Problems).Scan(&id)
	return id, err
}

// SetResult updates a History row once what it waited for happened.
func (s *Store) SetResult(ctx context.Context, id int64, result string) error {
	_, err := s.db.Exec(ctx, `UPDATE launch.change SET result = $2 WHERE id = $1`, id, result)
	return err
}

// Filter narrows History; empty fields match everything.
type Filter struct {
	Network, Account, Campaign string
	Limit                      int
}

// History lists changes, newest first. With Campaign it matches the
// campaign's own rows.
func (s *Store) History(ctx context.Context, f Filter) ([]Change, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, at, who, asked_by, network, account, group_id, campaign_id, kind, summary, before, after, result, problems
		FROM launch.change
		WHERE ($1 = '' OR network = $1) AND ($2 = '' OR account = $2) AND ($3 = '' OR campaign_id = $3)
		ORDER BY at DESC, id DESC LIMIT $4`, f.Network, f.Account, f.Campaign, f.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Change, error) {
		var c Change
		err := r.Scan(&c.ID, &c.At, &c.Who, &c.AskedBy, &c.Network, &c.Account, &c.GroupID, &c.CampaignID, &c.Kind,
			&c.Summary, &c.Before, &c.After, &c.Result, &c.Problems)
		return c, err
	})
}

// Move is a copy in another group waiting for a person to start it.
type Move struct {
	ID           int64      `json:"id"`
	ChangeID     int64      `json:"change_id"`
	Network      string     `json:"network"`
	Account      string     `json:"account"`
	FromCampaign string     `json:"from_campaign"`
	ToCampaign   string     `json:"to_campaign"`
	ToGroup      string     `json:"to_group"`
	Originals    string     `json:"originals"`
	State        string     `json:"state"`
	MadeAt       time.Time  `json:"made_at"`
	DoneAt       *time.Time `json:"done_at,omitempty"`
}

// AddMove records a move; one whose originals are 'when_started' waits.
func (s *Store) AddMove(ctx context.Context, m Move) (int64, error) {
	state := "done"
	if m.Originals == "when_started" {
		state = "waiting"
	}
	var id int64
	err := s.db.QueryRow(ctx, `
		INSERT INTO launch.move (change_id, network, account, from_campaign, to_campaign, to_group, originals, state, done_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, CASE WHEN $8 = 'done' THEN now() END) RETURNING id`,
		m.ChangeID, m.Network, m.Account, m.FromCampaign, m.ToCampaign, m.ToGroup, m.Originals, state).Scan(&id)
	return id, err
}

// Waiting lists the moves whose originals wait for their copies to start.
func (s *Store) Waiting(ctx context.Context) ([]Move, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, change_id, network, account, from_campaign, to_campaign, to_group, originals, state, made_at, done_at
		FROM launch.move WHERE state = 'waiting' ORDER BY made_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Move, error) {
		var m Move
		err := r.Scan(&m.ID, &m.ChangeID, &m.Network, &m.Account, &m.FromCampaign, &m.ToCampaign, &m.ToGroup, &m.Originals, &m.State, &m.MadeAt, &m.DoneAt)
		return m, err
	})
}

// EndMove marks a waiting move done or cancelled; false when it was not
// waiting (someone else ended it first).
func (s *Store) EndMove(ctx context.Context, id int64, state string) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE launch.move SET state = $2, done_at = now() WHERE id = $1 AND state = 'waiting'`, id, state)
	return err == nil && tag.RowsAffected() == 1, err
}

// Draft is a new pair not sent yet.
type Draft struct {
	ID        int64           `json:"id"`
	Network   string          `json:"network"`
	Account   string          `json:"account"`
	Name      string          `json:"name"`
	Body      json.RawMessage `json:"body,omitempty"`
	MadeBy    string          `json:"made_by"`
	ChangedAt time.Time       `json:"changed_at"`
}

// SaveDraft adds (ID 0) or replaces a draft and returns its id.
func (s *Store) SaveDraft(ctx context.Context, d Draft) (int64, error) {
	var id int64
	var err error
	if d.ID == 0 {
		err = s.db.QueryRow(ctx, `INSERT INTO launch.draft (network, account, name, body, made_by) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			d.Network, d.Account, d.Name, d.Body, d.MadeBy).Scan(&id)
	} else {
		err = s.db.QueryRow(ctx, `UPDATE launch.draft SET account = $2, name = $3, body = $4, changed_at = now() WHERE id = $1 RETURNING id`,
			d.ID, d.Account, d.Name, d.Body).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// Drafts lists drafts without their bodies, newest first.
func (s *Store) Drafts(ctx context.Context) ([]Draft, error) {
	rows, err := s.db.Query(ctx, `SELECT id, network, account, name, made_by, changed_at FROM launch.draft ORDER BY changed_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Draft, error) {
		var d Draft
		err := r.Scan(&d.ID, &d.Network, &d.Account, &d.Name, &d.MadeBy, &d.ChangedAt)
		return d, err
	})
}

// Draft reads one draft with its body.
func (s *Store) Draft(ctx context.Context, id int64) (Draft, error) {
	var d Draft
	err := s.db.QueryRow(ctx, `SELECT id, network, account, name, body, made_by, changed_at FROM launch.draft WHERE id = $1`, id).
		Scan(&d.ID, &d.Network, &d.Account, &d.Name, &d.Body, &d.MadeBy, &d.ChangedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// DeleteDraft removes a draft (sent, or thrown away).
func (s *Store) DeleteDraft(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM launch.draft WHERE id = $1`, id)
	return err
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// AddItems notes the ads Launch made in one campaign, with our ad ids.
func (s *Store) AddItems(ctx context.Context, network, account, campaign string, items map[string]string) error {
	if len(items) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for item, adID := range items {
		b.Queue(`INSERT INTO launch.item (network, account, campaign_id, item_id, ad_id) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (network, account, campaign_id, item_id) DO UPDATE SET ad_id = EXCLUDED.ad_id`,
			network, account, campaign, item, adID)
	}
	return s.db.SendBatch(ctx, b).Close()
}

// Request is a change another service asked for (launch_api.new_request_v1).
type Request struct {
	ID          int64           `json:"id"`
	Kind        string          `json:"kind"`
	Input       json.RawMessage `json:"input"`
	RequestedBy string          `json:"requested_by"`
	Origin      string          `json:"origin"`
	State       string          `json:"state"`
	ConfirmedBy string          `json:"confirmed_by"`
	Result      json.RawMessage `json:"result"`
	MadeAt      time.Time       `json:"made_at"`
	DecidedAt   *time.Time      `json:"decided_at"`
}

const requestCols = `id, kind, input, requested_by, origin, state, confirmed_by, COALESCE(result, 'null'), made_at, decided_at`

func scanRequest(row pgx.Row) (Request, error) {
	var r Request
	err := row.Scan(&r.ID, &r.Kind, &r.Input, &r.RequestedBy, &r.Origin, &r.State, &r.ConfirmedBy, &r.Result, &r.MadeAt, &r.DecidedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// Request reads one request.
func (s *Store) Request(ctx context.Context, id int64) (Request, error) {
	return scanRequest(s.db.QueryRow(ctx, `SELECT `+requestCols+` FROM launch.request WHERE id = $1`, id))
}

// Requests lists the waiting requests first, then the latest decided ones.
func (s *Store) Requests(ctx context.Context, limit int) ([]Request, error) {
	rows, err := s.db.Query(ctx, `SELECT `+requestCols+` FROM launch.request
		ORDER BY state <> 'waiting', COALESCE(decided_at, made_at) DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ErrDecided is a request someone already confirmed or refused.
var ErrDecided = errors.New("store: request already decided")

// Decide moves a waiting request to confirmed or refused, once: a second
// person pressing at the same time gets ErrDecided.
func (s *Store) Decide(ctx context.Context, id int64, state, who string) (Request, error) {
	r, err := scanRequest(s.db.QueryRow(ctx, `UPDATE launch.request SET state = $2, confirmed_by = $3, decided_at = now()
		WHERE id = $1 AND state = 'waiting' RETURNING `+requestCols, id, state, who))
	if errors.Is(err, ErrNotFound) {
		if _, err := s.Request(ctx, id); err != nil {
			return r, err
		}
		return r, ErrDecided
	}
	return r, err
}

// Finish records how a confirmed request went: sent or failed.
func (s *Store) Finish(ctx context.Context, id int64, state string, result any) error {
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE launch.request SET state = $2, result = $3 WHERE id = $1 AND state = 'confirmed'`, id, state, b)
	return err
}
