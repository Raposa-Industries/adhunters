// Package briefs keeps Create's briefs, their references and the options
// made from them, and runs the worker that makes those options (worker.go).
// The rows are in create_app; Desk and other apps reach the same rows
// through create_api (contract/sql/create), and a page's button and a
// create_api call end in the same jobs.
package briefs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // DecodeConfig reads a reference's size
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/create/internal/checks"
	"github.com/Raposa-Industries/adhunters/create/internal/openai"
	"github.com/Raposa-Industries/adhunters/shared/files"
)

// MaxReference is the most a reference picture may weigh.
const MaxReference = 20 << 20

// ErrNotFound means no such brief, reference, option or save.
var ErrNotFound = errors.New("not found")

// BadInput is a request that cannot be done as asked; its text is for the
// person, in pt-BR.
type BadInput string

func (e BadInput) Error() string { return string(e) }

// Store reads and writes Create's rows.
type Store struct {
	db    *pgxpool.Pool
	files files.Store
	kick  func()
}

// New returns a store. kick, when set, wakes the worker after new jobs.
func New(db *pgxpool.Pool, fs files.Store, kick func()) *Store {
	if kick == nil {
		kick = func() {}
	}
	return &Store{db: db, files: fs, kick: kick}
}

// DB is the pool, for the service's health check.
func (s *Store) DB() *pgxpool.Pool { return s.db }

// Brief is one brief.
type Brief struct {
	ID           int64           `json:"id"`
	Name         string          `json:"name"`
	VerticalID   string          `json:"vertical_id"`
	VerticalName string          `json:"vertical_name"`
	Ages         string          `json:"ages"`
	Images       int             `json:"images"`
	Headlines    int             `json:"headlines"`
	Angles       []string        `json:"angles"`
	OwnHeadlines []string        `json:"own_headlines"`
	Extra        string          `json:"extra"`
	Analysis     []openai.Aspect `json:"analysis"`
	State        string          `json:"state"`
	Error        string          `json:"error"`
	Rounds       int             `json:"rounds"`
	CostUSD      float64         `json:"cost_usd"`
	RequestedBy  string          `json:"requested_by"`
	Origin       string          `json:"origin"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`

	// Options and Chosen are counts, filled in lists.
	Options int `json:"options"`
	Chosen  int `json:"chosen"`
}

const briefCols = `b.id, b.name, b.vertical_id, b.vertical_name, b.ages, b.images, b.headlines, b.angles, b.own_headlines,
	b.extra, b.analysis, b.state, b.error, b.rounds, b.cost_usd::float8, b.requested_by, b.origin, b.created_at, b.updated_at`

func scanBrief(row pgx.Row, extra ...any) (Brief, error) {
	var b Brief
	var analysis []byte
	dest := append([]any{&b.ID, &b.Name, &b.VerticalID, &b.VerticalName, &b.Ages, &b.Images, &b.Headlines, &b.Angles,
		&b.OwnHeadlines, &b.Extra, &analysis, &b.State, &b.Error, &b.Rounds, &b.CostUSD, &b.RequestedBy, &b.Origin,
		&b.CreatedAt, &b.UpdatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return b, ErrNotFound
		}
		return b, err
	}
	if err := json.Unmarshal(analysis, &b.Analysis); err != nil {
		return b, err
	}
	if b.Analysis == nil {
		b.Analysis = []openai.Aspect{}
	}
	return b, nil
}

// Input is what a page sends for a brief; a nil field is left as it is.
type Input struct {
	Name         *string          `json:"name"`
	VerticalID   *string          `json:"vertical_id"`
	VerticalName *string          `json:"vertical_name"`
	Ages         *string          `json:"ages"`
	Images       *int             `json:"images"`
	Headlines    *int             `json:"headlines"`
	Angles       *[]string        `json:"angles"`
	OwnHeadlines *[]string        `json:"own_headlines"`
	Extra        *string          `json:"extra"`
	Analysis     *[]openai.Aspect `json:"analysis"`
}

func (in Input) check() error {
	if in.Images != nil && (*in.Images < 0 || *in.Images > openai.MaxImages) {
		return BadInput(fmt.Sprintf("imagens: de 0 a %d", openai.MaxImages))
	}
	if in.Headlines != nil && (*in.Headlines < 0 || *in.Headlines > openai.MaxHeadlines) {
		return BadInput(fmt.Sprintf("títulos: de 0 a %d", openai.MaxHeadlines))
	}
	return nil
}

func cleanList(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, l := range in {
		l = openai.CleanLine(l)
		if l != "" && !seen[strings.ToLower(l)] {
			seen[strings.ToLower(l)] = true
			out = append(out, l)
		}
	}
	return out
}

// NewBrief starts a draft from a page.
func (s *Store) NewBrief(ctx context.Context, in Input, by string) (Brief, error) {
	if err := in.check(); err != nil {
		return Brief{}, err
	}
	var id int64
	if err := s.db.QueryRow(ctx, `INSERT INTO create_app.brief (requested_by) VALUES ($1) RETURNING id`, by).Scan(&id); err != nil {
		return Brief{}, err
	}
	return s.Change(ctx, id, in)
}

// Change changes a brief's fields. They count from the next round.
func (s *Store) Change(ctx context.Context, id int64, in Input) (Brief, error) {
	if err := in.check(); err != nil {
		return Brief{}, err
	}
	set := []string{}
	args := []any{id}
	add := func(col string, v any) {
		args = append(args, v)
		set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if in.Name != nil {
		add("name", openai.CleanLine(*in.Name))
	}
	if in.VerticalID != nil {
		add("vertical_id", strings.TrimSpace(*in.VerticalID))
	}
	if in.VerticalName != nil {
		add("vertical_name", openai.CleanLine(*in.VerticalName))
	}
	if in.Ages != nil {
		add("ages", openai.CleanLine(*in.Ages))
	}
	if in.Images != nil {
		add("images", *in.Images)
	}
	if in.Headlines != nil {
		add("headlines", *in.Headlines)
	}
	if in.Angles != nil {
		add("angles", cleanList(*in.Angles))
	}
	if in.OwnHeadlines != nil {
		add("own_headlines", cleanList(*in.OwnHeadlines))
	}
	if in.Extra != nil {
		add("extra", strings.TrimSpace(*in.Extra))
	}
	if in.Analysis != nil {
		clean := []openai.Aspect{}
		for _, a := range *in.Analysis {
			a = openai.Aspect{Aspect: openai.CleanLine(a.Aspect), Fixed: openai.CleanLine(a.Fixed), Variable: openai.CleanLine(a.Variable)}
			if a.Aspect != "" {
				clean = append(clean, a)
			}
		}
		b, err := json.Marshal(clean)
		if err != nil {
			return Brief{}, err
		}
		add("analysis", b)
	}
	if len(set) > 0 {
		tag, err := s.db.Exec(ctx, `UPDATE create_app.brief SET `+strings.Join(set, ", ")+`, updated_at = now() WHERE id = $1`, args...)
		if err != nil {
			return Brief{}, err
		}
		if tag.RowsAffected() == 0 {
			return Brief{}, ErrNotFound
		}
	}
	return s.Brief(ctx, id)
}

// Brief reads one brief.
func (s *Store) Brief(ctx context.Context, id int64) (Brief, error) {
	return scanBrief(s.db.QueryRow(ctx, `SELECT `+briefCols+` FROM create_app.brief b WHERE b.id = $1`, id))
}

// Briefs lists briefs, newest first, before an id when before > 0.
func (s *Store) Briefs(ctx context.Context, before int64, limit int) ([]Brief, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+briefCols+`,
		       (SELECT count(*) FROM create_app.option o WHERE o.brief_id = b.id AND o.state = 'done'),
		       (SELECT count(*) FROM create_app.option o WHERE o.brief_id = b.id AND o.chosen)
		FROM create_app.brief b WHERE ($1 = 0 OR b.id < $1) ORDER BY b.id DESC LIMIT $2`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Brief{}
	for rows.Next() {
		var options, chosen int64
		b, err := scanBrief(rows, &options, &chosen)
		if err != nil {
			return nil, err
		}
		b.Options, b.Chosen = int(options), int(chosen)
		out = append(out, b)
	}
	return out, rows.Err()
}

// ---- references ------------------------------------------------------------

// Reference kinds.
const (
	RefSpyAd           = "spy_ad"
	RefLibraryCreative = "library_creative"
	RefUpload          = "upload"
)

// Reference is one picture a brief is made from.
type Reference struct {
	ID        int64     `json:"id"`
	BriefID   int64     `json:"brief_id"`
	Kind      string    `json:"kind"`
	RefID     string    `json:"ref_id"`
	Headline  string    `json:"headline"`
	State     string    `json:"state"`
	Error     string    `json:"error"`
	MediaType string    `json:"media_type"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	URL       string    `json:"url,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	fileKey string
}

const refCols = `id, brief_id, kind, ref_id, headline, state, error, media_type, width, height, created_at, file_key`

func scanRef(row pgx.Row) (Reference, error) {
	var r Reference
	err := row.Scan(&r.ID, &r.BriefID, &r.Kind, &r.RefID, &r.Headline, &r.State, &r.Error, &r.MediaType, &r.Width,
		&r.Height, &r.CreatedAt, &r.fileKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if r.State == "kept" {
		r.URL = fmt.Sprintf("/create/files/references/%d", r.ID)
	}
	return r, err
}

func (s *Store) references(ctx context.Context, briefID int64) ([]Reference, error) {
	rows, err := s.db.Query(ctx, `SELECT `+refCols+` FROM create_app.reference WHERE brief_id = $1 AND removed_at IS NULL ORDER BY id`, briefID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reference{}
	for rows.Next() {
		r, err := scanRef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddReference adds a Spy ad or a library creative by id; the worker fetches
// its picture before anything reads it.
func (s *Store) AddReference(ctx context.Context, briefID int64, kind, refID string) (Reference, error) {
	refID = strings.TrimSpace(refID)
	if kind != RefSpyAd && kind != RefLibraryCreative {
		return Reference{}, BadInput("referência: um anúncio do Spy ou um criativo da biblioteca")
	}
	if refID == "" {
		return Reference{}, BadInput("referência sem id")
	}
	if _, err := s.Brief(ctx, briefID); err != nil {
		return Reference{}, err
	}
	var existing int64
	err := s.db.QueryRow(ctx, `SELECT id FROM create_app.reference WHERE brief_id = $1 AND kind = $2 AND ref_id = $3 AND removed_at IS NULL`,
		briefID, kind, refID).Scan(&existing)
	if err == nil {
		return s.reference(ctx, existing)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Reference{}, err
	}
	var id int64
	if err := s.db.QueryRow(ctx, `INSERT INTO create_app.reference (brief_id, kind, ref_id) VALUES ($1, $2, $3) RETURNING id`,
		briefID, kind, refID).Scan(&id); err != nil {
		return Reference{}, err
	}
	s.kick()
	return s.reference(ctx, id)
}

// AddUpload keeps a picture from the person's computer as a reference.
func (s *Store) AddUpload(ctx context.Context, briefID int64, b []byte) (Reference, error) {
	if _, err := s.Brief(ctx, briefID); err != nil {
		return Reference{}, err
	}
	pic, err := readPicture(b)
	if err != nil {
		return Reference{}, err
	}
	key, err := files.PutBytes(ctx, s.files, b, pic.mediaType)
	if err != nil {
		return Reference{}, err
	}
	var id int64
	if err := s.db.QueryRow(ctx, `
		INSERT INTO create_app.reference (brief_id, kind, state, file_key, media_type, width, height, sha256)
		VALUES ($1, 'upload', 'kept', $2, $3, $4, $5, $6) RETURNING id`,
		briefID, key, pic.mediaType, pic.width, pic.height, pic.sha256).Scan(&id); err != nil {
		return Reference{}, err
	}
	return s.reference(ctx, id)
}

// RemoveReference takes a reference out of its brief; the row stays.
func (s *Store) RemoveReference(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `UPDATE create_app.reference SET removed_at = now() WHERE id = $1 AND removed_at IS NULL`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) reference(ctx context.Context, id int64) (Reference, error) {
	return scanRef(s.db.QueryRow(ctx, `SELECT `+refCols+` FROM create_app.reference WHERE id = $1`, id))
}

type picture struct {
	mediaType     string
	width, height int
	sha256        string
}

// readPicture checks that b is a JPEG, PNG or GIF OpenAI can read.
func readPicture(b []byte) (picture, error) {
	if len(b) == 0 {
		return picture{}, BadInput("arquivo vazio")
	}
	if len(b) > MaxReference {
		return picture{}, BadInput(fmt.Sprintf("imagem acima de %d MB", MaxReference>>20))
	}
	mt := http.DetectContentType(b)
	switch mt {
	case "image/jpeg", "image/png", "image/gif":
	default:
		return picture{}, BadInput("envie JPG, PNG ou GIF")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return picture{}, BadInput("imagem ilegível")
	}
	return picture{mediaType: mt, width: cfg.Width, height: cfg.Height, sha256: sha256hex(b)}, nil
}

// ---- options ---------------------------------------------------------------

// Option is one picture or headline made for a brief.
type Option struct {
	ID         int64            `json:"id"`
	BriefID    int64            `json:"brief_id"`
	Round      int              `json:"round"`
	Kind       string           `json:"kind"`
	Angle      string           `json:"angle"`
	Idea       string           `json:"idea"`
	Text       string           `json:"text"`
	ParentID   *int64           `json:"parent_id"`
	Note       string           `json:"note"`
	State      string           `json:"state"`
	Error      string           `json:"error"`
	MediaType  string           `json:"media_type"`
	Width      int              `json:"width"`
	Height     int              `json:"height"`
	CostUSD    float64          `json:"cost_usd"`
	Chosen     bool             `json:"chosen"`
	Starred    bool             `json:"starred"`
	URL        string           `json:"url,omitempty"`
	Warnings   []checks.Warning `json:"warnings"`
	CreatedAt  time.Time        `json:"created_at"`
	FinishedAt *time.Time       `json:"finished_at"`

	fileKey string
}

const optionCols = `id, brief_id, round, kind, angle, idea, text, parent_id, note, state, error, media_type, width, height,
	cost_usd::float8, chosen, starred, created_at, finished_at, file_key`

func scanOption(row pgx.Row) (Option, error) {
	var o Option
	err := row.Scan(&o.ID, &o.BriefID, &o.Round, &o.Kind, &o.Angle, &o.Idea, &o.Text, &o.ParentID, &o.Note, &o.State,
		&o.Error, &o.MediaType, &o.Width, &o.Height, &o.CostUSD, &o.Chosen, &o.Starred, &o.CreatedAt, &o.FinishedAt, &o.fileKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, ErrNotFound
	}
	o.Warnings = []checks.Warning{}
	if o.Kind == "headline" {
		o.Warnings = checks.Headline(o.Text)
	}
	if o.Kind == "image" && o.State == "done" {
		o.URL = fmt.Sprintf("/create/files/options/%d", o.ID)
	}
	return o, err
}

// Option reads one option.
func (s *Store) Option(ctx context.Context, id int64) (Option, error) {
	return scanOption(s.db.QueryRow(ctx, `SELECT `+optionCols+` FROM create_app.option WHERE id = $1`, id))
}

func (s *Store) options(ctx context.Context, briefID int64) ([]Option, error) {
	rows, err := s.db.Query(ctx, `SELECT `+optionCols+` FROM create_app.option WHERE brief_id = $1 ORDER BY id`, briefID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Option{}
	for rows.Next() {
		o, err := scanOption(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Mark is the person's marks on an option; nil is left as it is. Text
// rewrites a headline (cleaned), before it is saved.
type Mark struct {
	Chosen  *bool   `json:"chosen"`
	Starred *bool   `json:"starred"`
	Text    *string `json:"text"`
}

// MarkOption sets the person's marks on an option.
func (s *Store) MarkOption(ctx context.Context, id int64, m Mark) (Option, error) {
	o, err := s.Option(ctx, id)
	if err != nil {
		return o, err
	}
	if m.Text != nil {
		if o.Kind != "headline" {
			return o, BadInput("só títulos têm texto")
		}
		t := openai.CleanLine(*m.Text)
		if t == "" {
			return o, BadInput("título vazio")
		}
		m.Text = &t
	}
	if m.Chosen != nil && *m.Chosen && o.State != "done" {
		return o, BadInput("essa opção ainda não está pronta")
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE create_app.option SET chosen = COALESCE($2, chosen), starred = COALESCE($3, starred), text = COALESCE($4, text)
		WHERE id = $1`, id, m.Chosen, m.Starred, m.Text); err != nil {
		return o, err
	}
	return s.Option(ctx, id)
}

// ---- asking for work -------------------------------------------------------

// Round is what one "make" asks for.
type Round struct {
	Images    int  `json:"images"`
	Headlines int  `json:"headlines"`
	NewAngle  bool `json:"new_angle"`
}

// Read asks the worker to read the brief's performing ads into the
// analysis. The person edits it before making options.
func (s *Store) Read(ctx context.Context, briefID int64) (Brief, error) {
	refs, err := s.references(ctx, briefID)
	if err != nil {
		return Brief{}, err
	}
	if len(refs) == 0 {
		return Brief{}, BadInput("adicione anúncios de referência antes de ler")
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockBrief(ctx, tx, briefID); err != nil {
			return err
		}
		if busy, err := hasOpenJob(ctx, tx, briefID, "read"); err != nil || busy {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO create_app.job (brief_id, kind) VALUES ($1, 'read')`, briefID); err != nil {
			return err
		}
		return settleTx(ctx, tx, briefID)
	})
	if err != nil {
		return Brief{}, err
	}
	s.kick()
	return s.Brief(ctx, briefID)
}

// Make asks for one more round of options: the brief's counts for a
// normal round, or r's (the "3 more, new angle" button).
func (s *Store) Make(ctx context.Context, briefID int64, r *Round) (Brief, error) {
	b, err := s.Brief(ctx, briefID)
	if err != nil {
		return b, err
	}
	round := Round{Images: b.Images, Headlines: b.Headlines}
	if r != nil {
		round = *r
	}
	if round.Images < 0 || round.Images > openai.MaxImages || round.Headlines < 0 || round.Headlines > openai.MaxHeadlines {
		return b, BadInput(fmt.Sprintf("imagens de 0 a %d e títulos de 0 a %d", openai.MaxImages, openai.MaxHeadlines))
	}
	if round.Images+round.Headlines == 0 {
		return b, BadInput("peça ao menos uma imagem ou um título")
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockBrief(ctx, tx, briefID); err != nil {
			return err
		}
		if busy, err := hasOpenJob(ctx, tx, briefID, "plan"); err != nil {
			return err
		} else if busy {
			return BadInput("já tem uma rodada sendo planejada")
		}
		var n int
		if err := tx.QueryRow(ctx, `UPDATE create_app.brief SET rounds = rounds + 1 WHERE id = $1 RETURNING rounds`, briefID).Scan(&n); err != nil {
			return err
		}
		input, _ := json.Marshal(map[string]any{"round": n, "images": round.Images, "headlines": round.Headlines, "new_angle": round.NewAngle})
		if _, err := tx.Exec(ctx, `INSERT INTO create_app.job (brief_id, kind, input) VALUES ($1, 'plan', $2)`, briefID, input); err != nil {
			return err
		}
		return settleTx(ctx, tx, briefID)
	})
	if err != nil {
		return Brief{}, err
	}
	s.kick()
	return s.Brief(ctx, briefID)
}

// Again makes a picture again from a done one, changed by the person's
// note. The new picture is a new option; the old one stays.
func (s *Store) Again(ctx context.Context, optionID int64, note string) (Option, error) {
	note = strings.TrimSpace(note)
	o, err := s.Option(ctx, optionID)
	if err != nil {
		return o, err
	}
	if o.Kind != "image" || o.State != "done" {
		return o, BadInput("só uma imagem pronta pode ser refeita")
	}
	if note == "" {
		return o, BadInput("escreva o que mudar")
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockBrief(ctx, tx, o.BriefID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO create_app.option (brief_id, round, kind, angle, idea, parent_id, note, state)
			VALUES ($1, $2, 'image', $3, $4, $5, $6, 'waiting') RETURNING id`,
			o.BriefID, o.Round, o.Angle, o.Idea, o.ID, note).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO create_app.job (brief_id, kind, option_id) VALUES ($1, 'image', $2)`, o.BriefID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO create_app.event (brief_id, line) VALUES ($1, $2)`, o.BriefID,
			fmt.Sprintf("Refazendo a imagem %d: %s", o.ID, note)); err != nil {
			return err
		}
		return settleTx(ctx, tx, o.BriefID)
	})
	if err != nil {
		return Option{}, err
	}
	s.kick()
	return s.Option(ctx, id)
}

// ---- saves -----------------------------------------------------------------

// Save is chosen options saved into the library as one set.
type Save struct {
	ID           int64      `json:"id"`
	BriefID      int64      `json:"brief_id"`
	Name         string     `json:"name"`
	OptionIDs    []int64    `json:"option_ids"`
	AILabel      string     `json:"ai_label"`
	State        string     `json:"state"`
	Error        string     `json:"error"`
	LibrarySetID *int64     `json:"library_set_id"`
	RequestedBy  string     `json:"requested_by"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

const saveCols = `id, brief_id, name, option_ids, ai_label, state, error, library_set_id, requested_by, created_at, finished_at`

func scanSave(row pgx.Row) (Save, error) {
	var v Save
	err := row.Scan(&v.ID, &v.BriefID, &v.Name, &v.OptionIDs, &v.AILabel, &v.State, &v.Error, &v.LibrarySetID,
		&v.RequestedBy, &v.CreatedAt, &v.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// Save asks for chosen options to be saved into the library, through
// create_api.save_set_v1, the function Desk calls too. An empty name is the
// brief's name, or its vertical and date.
func (s *Store) Save(ctx context.Context, briefID int64, optionIDs []int64, name, aiLabel, by string) (Save, error) {
	b, err := s.Brief(ctx, briefID)
	if err != nil {
		return Save{}, err
	}
	switch aiLabel {
	case "ai", "not_ai", "unset":
	default:
		return Save{}, BadInput("marque se as imagens são feitas por IA")
	}
	if len(optionIDs) == 0 {
		return Save{}, BadInput("escolha ao menos uma opção")
	}
	name = openai.CleanLine(name)
	if name == "" {
		name = b.Name
	}
	if name == "" {
		name = strings.TrimSpace(b.VerticalName + " " + time.Now().UTC().Format("02/01 15:04"))
	}
	var id int64
	err = s.db.QueryRow(ctx, `SELECT create_api.save_set_v1($1, $2, $3, $4, '', $5)`, briefID, optionIDs, name, by, aiLabel).Scan(&id)
	if err != nil {
		var msg string
		if strings.Contains(err.Error(), "every option must be done") {
			msg = "só opções prontas desta rodada podem ser salvas"
		}
		if msg != "" {
			return Save{}, BadInput(msg)
		}
		return Save{}, err
	}
	s.kick()
	return s.SaveByID(ctx, id)
}

// SaveByID reads one save.
func (s *Store) SaveByID(ctx context.Context, id int64) (Save, error) {
	return scanSave(s.db.QueryRow(ctx, `SELECT `+saveCols+` FROM create_app.save WHERE id = $1`, id))
}

func (s *Store) saves(ctx context.Context, briefID int64) ([]Save, error) {
	rows, err := s.db.Query(ctx, `SELECT `+saveCols+` FROM create_app.save WHERE brief_id = $1 ORDER BY id`, briefID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Save{}
	for rows.Next() {
		v, err := scanSave(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- the whole brief -------------------------------------------------------

// Event is one line of a brief's making log.
type Event struct {
	ID     int64     `json:"id"`
	Line   string    `json:"line"`
	Failed bool      `json:"failed"`
	At     time.Time `json:"at"`
}

// Detail is a brief with everything its page shows.
type Detail struct {
	Brief      Brief       `json:"brief"`
	References []Reference `json:"references"`
	Options    []Option    `json:"options"`
	Saves      []Save      `json:"saves"`
	Events     []Event     `json:"events"`
}

// Detail reads a brief and everything its page shows.
func (s *Store) Detail(ctx context.Context, id int64) (Detail, error) {
	var d Detail
	var err error
	if d.Brief, err = s.Brief(ctx, id); err != nil {
		return d, err
	}
	if d.References, err = s.references(ctx, id); err != nil {
		return d, err
	}
	if d.Options, err = s.options(ctx, id); err != nil {
		return d, err
	}
	if d.Saves, err = s.saves(ctx, id); err != nil {
		return d, err
	}
	rows, err := s.db.Query(ctx, `SELECT id, line, failed, at FROM create_app.event WHERE brief_id = $1 ORDER BY id DESC LIMIT 200`, id)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	d.Events = []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Line, &e.Failed, &e.At); err != nil {
			return d, err
		}
		d.Events = append(d.Events, e)
	}
	return d, rows.Err()
}

// OptionFile opens a done picture's bytes.
func (s *Store) OptionFile(ctx context.Context, id int64) (io.ReadCloser, string, error) {
	o, err := s.Option(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if o.fileKey == "" {
		return nil, "", ErrNotFound
	}
	rc, err := s.files.Get(ctx, o.fileKey)
	return rc, o.MediaType, err
}

// ReferenceFile opens a kept reference's bytes.
func (s *Store) ReferenceFile(ctx context.Context, id int64) (io.ReadCloser, string, error) {
	r, err := s.reference(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if r.fileKey == "" {
		return nil, "", ErrNotFound
	}
	rc, err := s.files.Get(ctx, r.fileKey)
	return rc, r.MediaType, err
}

// ---- helpers ---------------------------------------------------------------

func lockBrief(ctx context.Context, tx pgx.Tx, id int64) error {
	var got int64
	err := tx.QueryRow(ctx, `SELECT id FROM create_app.brief WHERE id = $1 FOR UPDATE`, id).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func hasOpenJob(ctx context.Context, tx pgx.Tx, briefID int64, kind string) (bool, error) {
	var busy bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM create_app.job WHERE brief_id = $1 AND kind = $2 AND state IN ('waiting', 'running'))`,
		briefID, kind).Scan(&busy)
	return busy, err
}

func settleTx(ctx context.Context, tx pgx.Tx, briefID int64) error {
	_, err := tx.Exec(ctx, `SELECT create_app.settle($1)`, briefID)
	return err
}
