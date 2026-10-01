// Package sessions keeps Create's sessions: a vertical and a name, the turns
// a person sends in it (a prompt and the items they picked), and the
// pictures and headlines each turn makes. Any item can be picked for the
// next turn, so a picture or a headline is iterated on for as long as the
// person likes; what they keep is saved into the library, in the session's
// folder inside the vertical's (decision 0019).
//
// The rows are in create_app. Desk and other apps reach the same rows
// through create_api (contract/sql/create), and the page's buttons call the
// same create_api functions, so both end in the same work for the worker
// (worker.go).
package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // DecodeConfig reads an upload's size
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/create/internal/checks"
	"github.com/Raposa-Industries/adhunters/create/internal/openai"
	"github.com/Raposa-Industries/adhunters/shared/files"
)

// Limits of one turn, as create_api.send_turn_v1 checks them.
const (
	MaxImages    = 8
	MaxHeadlines = 20
	// MaxPicked is the most items one turn may start from.
	MaxPicked = 8
	// MaxUpload is the most a picture added to a session may weigh.
	MaxUpload = 20 << 20
)

// ErrNotFound means no such session, item or save.
var ErrNotFound = errors.New("not found")

// BadInput is a request that cannot be done as asked; its text is for the
// person, in pt-BR.
type BadInput string

func (e BadInput) Error() string { return string(e) }

// Store reads and writes Create's sessions.
type Store struct {
	db    *pgxpool.Pool
	files files.Store
	kick  func()
}

// New returns a store. kick, when set, wakes the worker after new work.
func New(db *pgxpool.Pool, fs files.Store, kick func()) *Store {
	if kick == nil {
		kick = func() {}
	}
	return &Store{db: db, files: fs, kick: kick}
}

// DB is the pool, for the service's health check.
func (s *Store) DB() *pgxpool.Pool { return s.db }

// Session is one session.
type Session struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	VerticalID   string    `json:"vertical_id"`
	VerticalName string    `json:"vertical_name"`
	LibrarySetID *int64    `json:"library_set_id"`
	CostUSD      float64   `json:"cost_usd"`
	MadeBy       string    `json:"made_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	// Images and Headlines count its done items; Making its open turns.
	Images    int `json:"images"`
	Headlines int `json:"headlines"`
	Making    int `json:"making"`
}

// Turn is one turn of a session.
type Turn struct {
	ID        int64     `json:"id"`
	SessionID int64     `json:"session_id"`
	Prompt    string    `json:"prompt"`
	Picked    []int64   `json:"picked"`
	Images    int       `json:"images"`
	Headlines int       `json:"headlines"`
	State     string    `json:"state"`
	Error     string    `json:"error"`
	MadeBy    string    `json:"made_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Item is one picture or headline of a session.
type Item struct {
	ID         int64     `json:"id"`
	SessionID  int64     `json:"session_id"`
	TurnID     *int64    `json:"turn_id"`
	Kind       string    `json:"kind"`
	Origin     string    `json:"origin"`
	FromIDs    []int64   `json:"from_ids"`
	Text       string    `json:"text"`
	Brief      string    `json:"brief"`
	Angle      string    `json:"angle"`
	State      string    `json:"state"`
	Error      string    `json:"error"`
	MediaType  string    `json:"media_type"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	CostUSD    float64   `json:"cost_usd"`
	LibraryRef string    `json:"library_ref"`
	CreatedAt  time.Time `json:"created_at"`
	// ImageURL is where the page loads a done picture.
	ImageURL string `json:"image_url,omitempty"`
	// Warnings are Taboola's rules a headline breaks; they never block.
	Warnings []checks.Warning `json:"warnings,omitempty"`
	// Saved is whether a done save holds it.
	Saved bool `json:"saved"`

	fileKey string
}

// Save is one save of items into the library.
type Save struct {
	ID           int64      `json:"id"`
	SessionID    int64      `json:"session_id"`
	ItemIDs      []int64    `json:"item_ids"`
	AILabel      string     `json:"ai_label"`
	State        string     `json:"state"`
	Error        string     `json:"error"`
	LibrarySetID *int64     `json:"library_set_id"`
	MadeBy       string     `json:"made_by"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

// Detail is a session with everything in it, oldest first.
type Detail struct {
	Session Session `json:"session"`
	Turns   []Turn  `json:"turns"`
	Items   []Item  `json:"items"`
	Saves   []Save  `json:"saves"`
}

// apiError turns a create_api function's refusal into words for the person.
func apiError(err error) error {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "P0001" {
		return err
	}
	m := pe.Message
	switch {
	case strings.HasPrefix(m, "no session"):
		return ErrNotFound
	case strings.HasPrefix(m, "every picked item"):
		return BadInput("algum item escolhido não está pronto ou não é desta sessão")
	case strings.HasPrefix(m, "every item"):
		return BadInput("algum item não está pronto ou não é desta sessão")
	case strings.HasPrefix(m, "images must be"):
		return BadInput(fmt.Sprintf("peça de 0 a %d imagens e de 0 a %d headlines, e não zero dos dois", MaxImages, MaxHeadlines))
	case strings.HasPrefix(m, "a turn needs"):
		return BadInput("escreva um pedido ou escolha imagens ou headlines")
	case strings.HasPrefix(m, "pick at least"):
		return BadInput("escolha o que salvar")
	case strings.HasPrefix(m, "a session needs"):
		return BadInput("a sessão precisa de um nome e de uma vertical")
	}
	return BadInput(m)
}

// ---- sessions --------------------------------------------------------------

// NewSession opens a session; a name already used in the vertical opens
// that one again.
func (s *Store) NewSession(ctx context.Context, name, verticalID, verticalName, who string) (Session, error) {
	name = openai.CleanLine(name)
	switch {
	case name == "":
		return Session{}, BadInput("dê um nome à sessão")
	case len([]rune(name)) > 120:
		return Session{}, BadInput("o nome tem no máximo 120 caracteres")
	case strings.ContainsAny(name, `/\`):
		return Session{}, BadInput("o nome vira uma pasta: sem / nem \\")
	case verticalID == "" || verticalName == "":
		return Session{}, BadInput("escolha a vertical")
	}
	var id int64
	err := s.db.QueryRow(ctx, `SELECT create_api.new_session_v1($1, $2, $3, $4, '')`, name, verticalID, verticalName, who).Scan(&id)
	if err != nil {
		return Session{}, apiError(err)
	}
	return s.Session(ctx, id)
}

const sessionCols = `s.id, s.name, s.vertical_id, s.vertical_name, s.library_set_id, s.cost_usd::float8, s.made_by, s.created_at, s.updated_at,
	(SELECT count(*) FROM create_app.item i WHERE i.session_id = s.id AND i.state = 'done' AND i.kind = 'image'),
	(SELECT count(*) FROM create_app.item i WHERE i.session_id = s.id AND i.state = 'done' AND i.kind = 'headline'),
	(SELECT count(*) FROM create_app.turn t WHERE t.session_id = s.id AND t.state = 'making')`

func scanSession(row pgx.Row) (Session, error) {
	var v Session
	err := row.Scan(&v.ID, &v.Name, &v.VerticalID, &v.VerticalName, &v.LibrarySetID, &v.CostUSD, &v.MadeBy, &v.CreatedAt, &v.UpdatedAt,
		&v.Images, &v.Headlines, &v.Making)
	return v, err
}

// Session reads one session.
func (s *Store) Session(ctx context.Context, id int64) (Session, error) {
	v, err := scanSession(s.db.QueryRow(ctx, `SELECT `+sessionCols+` FROM create_app.session s WHERE s.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return v, err
}

// Sessions lists sessions, the last used first, of one vertical when it is
// given.
func (s *Store) Sessions(ctx context.Context, verticalID string, limit int) ([]Session, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT `+sessionCols+` FROM create_app.session s
		WHERE $1 = '' OR s.vertical_id = $1 ORDER BY s.updated_at DESC, s.id DESC LIMIT $2`, verticalID, limit)
	if err != nil {
		return nil, err
	}
	out := []Session{}
	for rows.Next() {
		v, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Rename gives a session another name. The site renames its library set
// (and so its Drive folder) too, once it has one.
func (s *Store) Rename(ctx context.Context, id int64, name string) (Session, error) {
	name = openai.CleanLine(name)
	if name == "" || len([]rune(name)) > 120 || strings.ContainsAny(name, `/\`) {
		return Session{}, BadInput("dê um nome de até 120 caracteres, sem / nem \\")
	}
	tag, err := s.db.Exec(ctx, `UPDATE create_app.session SET name = $2, updated_at = now() WHERE id = $1`, id, name)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return Session{}, BadInput("já há uma sessão com esse nome nesta vertical")
	}
	if err != nil {
		return Session{}, err
	}
	if tag.RowsAffected() == 0 {
		return Session{}, ErrNotFound
	}
	return s.Session(ctx, id)
}

// Detail reads a session with its turns, items and saves.
func (s *Store) Detail(ctx context.Context, id int64) (Detail, error) {
	v, err := s.Session(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Session: v, Turns: []Turn{}, Items: []Item{}, Saves: []Save{}}
	rows, err := s.db.Query(ctx, `SELECT `+turnCols+` FROM create_app.turn WHERE session_id = $1 ORDER BY id`, id)
	if err != nil {
		return Detail{}, err
	}
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			rows.Close()
			return Detail{}, err
		}
		d.Turns = append(d.Turns, t)
	}
	if err := rows.Err(); err != nil {
		return Detail{}, err
	}
	if d.Items, err = s.items(ctx, `session_id = $1`, id); err != nil {
		return Detail{}, err
	}
	rows, err = s.db.Query(ctx, `SELECT `+saveCols+` FROM create_app.session_save v JOIN create_app.session x ON x.id = v.session_id
		WHERE v.session_id = $1 ORDER BY v.id`, id)
	if err != nil {
		return Detail{}, err
	}
	for rows.Next() {
		sv, err := scanSave(rows)
		if err != nil {
			rows.Close()
			return Detail{}, err
		}
		d.Saves = append(d.Saves, sv)
	}
	return d, rows.Err()
}

// ---- turns -----------------------------------------------------------------

const turnCols = `id, session_id, prompt, picked, images, headlines, state, error, made_by, created_at`

func scanTurn(row pgx.Row) (Turn, error) {
	var t Turn
	err := row.Scan(&t.ID, &t.SessionID, &t.Prompt, &t.Picked, &t.Images, &t.Headlines, &t.State, &t.Error, &t.MadeBy, &t.CreatedAt)
	return t, err
}

// Turn reads one turn.
func (s *Store) Turn(ctx context.Context, id int64) (Turn, error) {
	t, err := scanTurn(s.db.QueryRow(ctx, `SELECT `+turnCols+` FROM create_app.turn WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Turn{}, ErrNotFound
	}
	return t, err
}

// Send is what a person sends in a session.
type Send struct {
	Prompt    string  `json:"prompt"`
	Picked    []int64 `json:"picked"`
	Images    int     `json:"images"`
	Headlines int     `json:"headlines"`
}

// Send starts a turn.
func (s *Store) Send(ctx context.Context, sessionID int64, in Send, who string) (Turn, error) {
	in.Prompt = strings.TrimSpace(in.Prompt)
	if len([]rune(in.Prompt)) > 4000 {
		return Turn{}, BadInput("o pedido tem no máximo 4000 caracteres")
	}
	if len(in.Picked) > MaxPicked {
		return Turn{}, BadInput(fmt.Sprintf("escolha no máximo %d itens por vez", MaxPicked))
	}
	if in.Picked == nil {
		in.Picked = []int64{}
	}
	if in.Images > 0 && in.Prompt == "" {
		var pics int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM create_app.item WHERE id = ANY ($1) AND kind = 'image'`, in.Picked).Scan(&pics); err != nil {
			return Turn{}, err
		}
		if pics == 0 {
			return Turn{}, BadInput("escreva o que a imagem deve mostrar, ou escolha uma imagem para variar")
		}
	}
	var id int64
	err := s.db.QueryRow(ctx, `SELECT create_api.send_turn_v1($1, $2, $3, $4, $5, $6, '')`,
		sessionID, in.Prompt, in.Picked, in.Images, in.Headlines, who).Scan(&id)
	if err != nil {
		return Turn{}, apiError(err)
	}
	s.kick()
	return s.Turn(ctx, id)
}

// ---- items -----------------------------------------------------------------

const itemCols = `i.id, i.session_id, i.turn_id, i.kind, i.origin, i.from_ids, i.text, i.brief, i.angle, i.state, i.error,
	i.media_type, i.width, i.height, i.cost_usd::float8, i.library_ref, i.created_at, COALESCE(i.file_key, ''),
	EXISTS (SELECT 1 FROM create_app.session_save v WHERE v.session_id = i.session_id AND v.state = 'done' AND i.id = ANY (v.item_ids))`

func scanItem(row pgx.Row) (Item, error) {
	var it Item
	err := row.Scan(&it.ID, &it.SessionID, &it.TurnID, &it.Kind, &it.Origin, &it.FromIDs, &it.Text, &it.Brief, &it.Angle, &it.State,
		&it.Error, &it.MediaType, &it.Width, &it.Height, &it.CostUSD, &it.LibraryRef, &it.CreatedAt, &it.fileKey, &it.Saved)
	if err != nil {
		return it, err
	}
	if it.Kind == "image" && it.State == "done" {
		it.ImageURL = fmt.Sprintf("/create/files/items/%d", it.ID)
	}
	if it.Kind == "headline" {
		it.Warnings = checks.Headline(it.Text)
	}
	return it, nil
}

func (s *Store) items(ctx context.Context, where string, args ...any) ([]Item, error) {
	rows, err := s.db.Query(ctx, `SELECT `+itemCols+` FROM create_app.item i WHERE `+where+` ORDER BY i.id`, args...)
	if err != nil {
		return nil, err
	}
	out := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Item reads one item.
func (s *Store) Item(ctx context.Context, id int64) (Item, error) {
	it, err := scanItem(s.db.QueryRow(ctx, `SELECT `+itemCols+` FROM create_app.item i WHERE i.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	return it, err
}

// ItemsFrom returns a session's done items that came from ref (a library
// id, or spy:creative:<id>), oldest first.
func (s *Store) ItemsFrom(ctx context.Context, sessionID int64, ref string) ([]Item, error) {
	return s.items(ctx, `i.session_id = $1 AND i.library_ref = $2 AND i.state = 'done'`, sessionID, ref)
}

// AddPicture adds a picture to a session from a person's computer (origin
// upload), the library (origin library, ref its creative's id) or a Spy ad
// (origin spy, ref spy:creative:<id>). It is kept before its row is written.
func (s *Store) AddPicture(ctx context.Context, sessionID int64, origin, ref string, b []byte) (Item, error) {
	if _, err := s.Session(ctx, sessionID); err != nil {
		return Item{}, err
	}
	pic, err := readPicture(b)
	if err != nil {
		return Item{}, err
	}
	key, err := files.PutBytes(ctx, s.files, b, pic.mediaType)
	if err != nil {
		return Item{}, err
	}
	var id int64
	err = s.db.QueryRow(ctx, `
		INSERT INTO create_app.item (session_id, kind, origin, state, file_key, media_type, width, height, sha256, library_ref, finished_at)
		VALUES ($1, 'image', $2, 'done', $3, $4, $5, $6, $7, $8, now()) RETURNING id`,
		sessionID, origin, key, pic.mediaType, pic.width, pic.height, pic.sha256, ref).Scan(&id)
	if err != nil {
		return Item{}, err
	}
	s.touch(ctx, sessionID)
	return s.Item(ctx, id)
}

// AddHeadline adds a headline a person typed (origin typed), took from the
// library (origin library, ref its id) or from a Spy ad (origin spy). Headlines are always English;
// Taboola's rules only warn.
func (s *Store) AddHeadline(ctx context.Context, sessionID int64, origin, ref, text string) (Item, error) {
	text = openai.CleanLine(text)
	if text == "" {
		return Item{}, BadInput("escreva a headline")
	}
	if len([]rune(text)) > 200 {
		return Item{}, BadInput("a headline tem no máximo 200 caracteres")
	}
	if _, err := s.Session(ctx, sessionID); err != nil {
		return Item{}, err
	}
	var id int64
	err := s.db.QueryRow(ctx, `
		INSERT INTO create_app.item (session_id, kind, origin, text, state, library_ref, finished_at)
		VALUES ($1, 'headline', $2, $3, 'done', $4, now()) RETURNING id`, sessionID, origin, text, ref).Scan(&id)
	if err != nil {
		return Item{}, err
	}
	s.touch(ctx, sessionID)
	return s.Item(ctx, id)
}

// EditHeadline changes a headline's text. A saved one is already in the
// library as it was, so it is refused: the person adds a new one instead.
func (s *Store) EditHeadline(ctx context.Context, id int64, text string) (Item, error) {
	it, err := s.Item(ctx, id)
	if err != nil {
		return Item{}, err
	}
	text = openai.CleanLine(text)
	switch {
	case it.Kind != "headline":
		return Item{}, BadInput("só headlines são editadas")
	case it.Saved:
		return Item{}, BadInput("essa headline já está na biblioteca; escreva uma nova")
	case text == "" || len([]rune(text)) > 200:
		return Item{}, BadInput("a headline precisa de 1 a 200 caracteres")
	}
	if _, err := s.db.Exec(ctx, `UPDATE create_app.item SET text = $2 WHERE id = $1`, id, text); err != nil {
		return Item{}, err
	}
	return s.Item(ctx, id)
}

func (s *Store) touch(ctx context.Context, sessionID int64) {
	_, _ = s.db.Exec(ctx, `UPDATE create_app.session SET updated_at = now() WHERE id = $1`, sessionID)
}

// ItemFile opens a picture's bytes.
func (s *Store) ItemFile(ctx context.Context, id int64) (io.ReadCloser, string, error) {
	it, err := s.Item(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if it.fileKey == "" {
		return nil, "", ErrNotFound
	}
	rc, err := s.files.Get(ctx, it.fileKey)
	return rc, it.MediaType, err
}

func (s *Store) itemBytes(ctx context.Context, it Item) ([]byte, error) {
	rc, err := s.files.Get(ctx, it.fileKey)
	if err != nil {
		return nil, fmt.Errorf("item %d: %w", it.ID, err)
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

type picture struct {
	mediaType     string
	width, height int
	sha256        string
}

func readPicture(b []byte) (picture, error) {
	if len(b) == 0 {
		return picture{}, BadInput("arquivo vazio")
	}
	if len(b) > MaxUpload {
		return picture{}, BadInput(fmt.Sprintf("imagem acima de %d MB", MaxUpload>>20))
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

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ---- saves -----------------------------------------------------------------

const saveCols = `v.id, v.session_id, v.item_ids, v.ai_label, v.state, v.error, x.library_set_id, v.made_by, v.created_at, v.finished_at`

func scanSave(row pgx.Row) (Save, error) {
	var v Save
	err := row.Scan(&v.ID, &v.SessionID, &v.ItemIDs, &v.AILabel, &v.State, &v.Error, &v.LibrarySetID, &v.MadeBy, &v.CreatedAt, &v.FinishedAt)
	return v, err
}

// SaveByID reads one save.
func (s *Store) SaveByID(ctx context.Context, id int64) (Save, error) {
	v, err := scanSave(s.db.QueryRow(ctx, `SELECT `+saveCols+` FROM create_app.session_save v JOIN create_app.session x ON x.id = v.session_id
		WHERE v.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Save{}, ErrNotFound
	}
	return v, err
}

// Save saves items into the library, in the session's set.
func (s *Store) Save(ctx context.Context, sessionID int64, itemIDs []int64, aiLabel, who string) (Save, error) {
	switch aiLabel {
	case "":
		aiLabel = "ai"
	case "ai", "not_ai", "unset":
	default:
		return Save{}, BadInput("ai_label é ai, not_ai ou unset")
	}
	if len(itemIDs) > 100 {
		return Save{}, BadInput("salve no máximo 100 itens por vez")
	}
	if _, err := s.Session(ctx, sessionID); err != nil {
		return Save{}, err
	}
	var id int64
	err := s.db.QueryRow(ctx, `SELECT create_api.save_items_v1($1, $2, $3, $4, '')`, sessionID, itemIDs, aiLabel, who).Scan(&id)
	if err != nil {
		return Save{}, apiError(err)
	}
	s.kick()
	return s.SaveByID(ctx, id)
}
