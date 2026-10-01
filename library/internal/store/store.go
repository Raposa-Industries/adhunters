// Package store is the library's rows: it adds creatives, headlines and
// sets, mints names, and lists what the apps show. A creative's bytes live
// in the team's Drive folder (decisions/0020-library-on-drive-only.md): they
// wait in the row until the Drive sync uploads them, and are read back from
// Drive after that. Only the thumbnail stays here.
package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/library/internal/picture"
	"github.com/Raposa-Industries/adhunters/shared/text"
)

// Origins: where a creative, headline or set came from.
const (
	OriginCreate = "create"
	OriginUpload = "upload"
	OriginDrive  = "drive"
)

// AI labels: the person's answer to "made with AI?".
const (
	AIUnset = "unset"
	AIYes   = "ai"
	AINo    = "not_ai"
)

// ErrNotFound means no row has that id.
var ErrNotFound = errors.New("not found")

// ErrGone means the creative's file was deleted from Drive, so its bytes
// are no longer anywhere. Its row and thumbnail stay.
var ErrGone = errors.New("its file was deleted from Drive")

// ErrNoDrive means a creative's bytes are in Drive but the library is not
// signed in to Drive now.
var ErrNoDrive = errors.New("the library is not signed in to Drive")

// Drive reads a file's bytes back from the team's Drive folder.
type Drive interface {
	Download(ctx context.Context, fileID string) ([]byte, error)
}

// notFounder is a Drive error that can say the file is not there, so Open
// can tell a deleted file from a failure.
type notFounder interface{ NotFound() bool }

// BadInput is a request the library refuses before touching anything; its
// text is shown to the person.
type BadInput string

func (e BadInput) Error() string { return string(e) }

// Store is the library's database.
type Store struct {
	db    *pgxpool.Pool
	drive func(context.Context) (Drive, error)
	now   func() time.Time
}

// New returns a store. Until UseDrive is called, bytes already in Drive
// cannot be read (ErrNoDrive).
func New(db *pgxpool.Pool) *Store {
	return &Store{db: db, drive: func(context.Context) (Drive, error) { return nil, ErrNoDrive }, now: time.Now}
}

// UseDrive sets how Open reaches Drive: f returns a client signed in now,
// or ErrNoDrive. Call it before serving.
func (s *Store) UseDrive(f func(context.Context) (Drive, error)) { s.drive = f }

// DB is the pool, for the Drive sync.
func (s *Store) DB() *pgxpool.Pool { return s.db }

// Vertical is one vertical names are minted in.
type Vertical struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Code          string `json:"code"`
	NetworkLetter string `json:"network_letter"`
	NextNumber    int    `json:"next_number"`
}

// Creative is one creative as the apps see it.
type Creative struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	VerticalID string    `json:"vertical_id"`
	Angle      string    `json:"angle"`
	Idea       string    `json:"idea"`
	Origin     string    `json:"origin"`
	OriginRef  string    `json:"origin_ref"`
	AILabel    string    `json:"ai_label"`
	MadeBy     string    `json:"made_by"`
	SHA256     string    `json:"sha256"`
	MediaType  string    `json:"media_type"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	Bytes      int64     `json:"bytes"`
	DriveState string    `json:"drive_state"`
	Hidden     bool      `json:"hidden"`
	CreatedAt  time.Time `json:"created_at"`
	// SetIDs are the sets it is in.
	SetIDs []int64 `json:"set_ids"`
}

// Headline is one headline as the apps see it.
type Headline struct {
	ID         int64     `json:"id"`
	Text       string    `json:"text"`
	SHA256     string    `json:"sha256"`
	VerticalID string    `json:"vertical_id"`
	Angle      string    `json:"angle"`
	Origin     string    `json:"origin"`
	OriginRef  string    `json:"origin_ref"`
	AILabel    string    `json:"ai_label"`
	MadeBy     string    `json:"made_by"`
	Hidden     bool      `json:"hidden"`
	CreatedAt  time.Time `json:"created_at"`
	SetIDs     []int64   `json:"set_ids"`
}

// Set is creatives and headlines kept together.
type Set struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	VerticalID string    `json:"vertical_id"`
	Origin     string    `json:"origin"`
	OriginRef  string    `json:"origin_ref"`
	MadeBy     string    `json:"made_by"`
	CreatedAt  time.Time `json:"created_at"`
	Creatives  int       `json:"creatives"`
	Headlines  int       `json:"headlines"`
}

func checkOrigin(o string) error {
	switch o {
	case OriginCreate, OriginUpload, OriginDrive:
		return nil
	}
	return BadInput(fmt.Sprintf("origin must be create, upload or drive, not %q", o))
}

func checkAI(l string) (string, error) {
	switch l {
	case "":
		return AIUnset, nil
	case AIUnset, AIYes, AINo:
		return l, nil
	}
	return "", BadInput(fmt.Sprintf("ai_label must be unset, ai or not_ai, not %q", l))
}

// ---- verticals ---------------------------------------------------------------

var verticalIDRe = regexp.MustCompile(`^[a-z0-9-]{1,60}$`)

// Verticals lists the verticals, by name.
func (s *Store) Verticals(ctx context.Context) ([]Vertical, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, code, network_letter, next_number FROM library.vertical ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Vertical, error) {
		var v Vertical
		err := r.Scan(&v.ID, &v.Name, &v.Code, &v.NetworkLetter, &v.NextNumber)
		return v, err
	})
}

// ensureVertical makes the vertical id when it is new: its name from name
// (or the id), and a code from its initials that no other vertical has.
func ensureVertical(ctx context.Context, tx pgx.Tx, id, name string) error {
	if id == "" {
		return nil
	}
	if !verticalIDRe.MatchString(id) {
		return BadInput(fmt.Sprintf("vertical %q is not a vertical id (lower case, digits and dashes)", id))
	}
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM library.vertical WHERE id = $1`, id).Scan(&one)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if strings.TrimSpace(name) == "" {
		name = titleFromID(id)
	}
	var codes []string
	rows, err := tx.Query(ctx, `SELECT code FROM library.vertical`)
	if err != nil {
		return err
	}
	codes, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	code := freeCode(name, codes)
	_, err = tx.Exec(ctx, `INSERT INTO library.vertical (id, name, code) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`, id, name, code)
	return err
}

func titleFromID(id string) string {
	words := strings.Split(id, "-")
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// freeCode is a 2 to 4 letter code for a vertical named name that is not in
// taken: the initials of its words, else its first letters, else those with
// a letter added.
func freeCode(name string, taken []string) string {
	used := map[string]bool{}
	for _, c := range taken {
		used[c] = true
	}
	var letters, initials []rune
	start := true
	for _, r := range strings.ToUpper(name) {
		if r > unicode.MaxASCII || !unicode.IsLetter(r) {
			start = true
			continue
		}
		letters = append(letters, r)
		if start {
			initials = append(initials, r)
		}
		start = false
	}
	var tries []string
	if len(initials) >= 2 {
		tries = append(tries, string(initials[:min(4, len(initials))]))
	}
	for n := 2; n <= 4 && n <= len(letters); n++ {
		tries = append(tries, string(letters[:n]))
	}
	for _, t := range tries {
		if !used[t] {
			return t
		}
	}
	base := "V"
	if len(letters) > 0 {
		base = string(letters[0])
	}
	for a := 'A'; a <= 'Z'; a++ {
		for b := 'A'; b <= 'Z'; b++ {
			if c := base + string(a) + string(b); !used[c] {
				return c
			}
		}
	}
	return "ZZZZ"
}

var codeRe = regexp.MustCompile(`^[A-Z]{2,4}$`)

// VerticalChange is a change to a vertical's naming. Nil fields stay.
type VerticalChange struct {
	Name          *string `json:"name"`
	Code          *string `json:"code"`
	NetworkLetter *string `json:"network_letter"`
	// NextNumber may only go up, so a number is never given twice.
	NextNumber *int `json:"next_number"`
}

// ChangeVertical renames or recodes a vertical. Names already minted keep
// theirs: a file handed to an ad network cannot be renamed after the fact.
func (s *Store) ChangeVertical(ctx context.Context, id string, c VerticalChange) (Vertical, error) {
	if c.Code != nil && !codeRe.MatchString(*c.Code) {
		return Vertical{}, BadInput("code must be 2 to 4 capital letters")
	}
	if c.NetworkLetter != nil && !regexp.MustCompile(`^[A-Z]$`).MatchString(*c.NetworkLetter) {
		return Vertical{}, BadInput("network_letter must be one capital letter")
	}
	if c.Name != nil && strings.TrimSpace(*c.Name) == "" {
		return Vertical{}, BadInput("name is empty")
	}
	var v Vertical
	err := s.db.QueryRow(ctx, `
		UPDATE library.vertical SET
			name = COALESCE($2, name),
			code = COALESCE($3, code),
			network_letter = COALESCE($4, network_letter),
			next_number = GREATEST(next_number, COALESCE($5, next_number))
		WHERE id = $1
		RETURNING id, name, code, network_letter, next_number`,
		id, trimPtr(c.Name), c.Code, c.NetworkLetter, c.NextNumber).
		Scan(&v.ID, &v.Name, &v.Code, &v.NetworkLetter, &v.NextNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return Vertical{}, ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return Vertical{}, BadInput("another vertical already has that code")
	}
	return v, err
}

func trimPtr(p *string) *string {
	if p == nil {
		return nil
	}
	t := strings.TrimSpace(*p)
	return &t
}

// ---- sets --------------------------------------------------------------------

// NewSet is what an app sends to start a set.
type NewSet struct {
	Name       string `json:"name"`
	VerticalID string `json:"vertical_id"`
	// VerticalName names a vertical the library has not seen yet.
	VerticalName string `json:"vertical_name"`
	Origin       string `json:"origin"`
	OriginRef    string `json:"origin_ref"`
	MadeBy       string `json:"made_by"`
}

// AddSet makes a set. A name already used in the vertical gets " (2)", " (3)"
// and so on, since it is also a folder's name in Drive.
func (s *Store) AddSet(ctx context.Context, n NewSet) (Set, error) {
	n.Name = text.CleanLine(n.Name)
	if n.Name == "" {
		return Set{}, BadInput("a set needs a name")
	}
	if len([]rune(n.Name)) > 120 {
		return Set{}, BadInput("a set's name is 120 characters at most")
	}
	if n.Origin == "" {
		n.Origin = OriginUpload
	}
	if err := checkOrigin(n.Origin); err != nil {
		return Set{}, err
	}
	var out Set
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := ensureVertical(ctx, tx, n.VerticalID, n.VerticalName); err != nil {
			return err
		}
		name := n.Name
		for i := 2; ; i++ {
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM library.set WHERE COALESCE(vertical_id, '') = $1 AND lower(name) = lower($2))`,
				n.VerticalID, name).Scan(&taken); err != nil {
				return err
			}
			if !taken {
				break
			}
			name = fmt.Sprintf("%s (%d)", n.Name, i)
		}
		return tx.QueryRow(ctx, `
			INSERT INTO library.set (name, vertical_id, origin, origin_ref, made_by)
			VALUES ($1, NULLIF($2, ''), $3, $4, $5)
			RETURNING id, name, COALESCE(vertical_id, ''), origin, origin_ref, made_by, created_at`,
			name, n.VerticalID, n.Origin, n.OriginRef, n.MadeBy).
			Scan(&out.ID, &out.Name, &out.VerticalID, &out.Origin, &out.OriginRef, &out.MadeBy, &out.CreatedAt)
	})
	return out, err
}

// RenameSet gives a set a new name; its Drive folder, when it has one, is
// renamed on the next pass. A name another set of the vertical has is
// refused, since it is also a folder's name.
func (s *Store) RenameSet(ctx context.Context, id int64, name string) (Set, error) {
	name = text.CleanLine(name)
	if name == "" {
		return Set{}, BadInput("a set needs a name")
	}
	if len([]rune(name)) > 120 {
		return Set{}, BadInput("a set's name is 120 characters at most")
	}
	var taken bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM library.set o JOIN library.set s ON s.id = $1
		WHERE o.id <> s.id AND COALESCE(o.vertical_id, '') = COALESCE(s.vertical_id, '') AND lower(o.name) = lower($2))`, id, name).Scan(&taken)
	if err != nil {
		return Set{}, err
	}
	if taken {
		return Set{}, BadInput("another set of this vertical already has that name")
	}
	tag, err := s.db.Exec(ctx, `UPDATE library.set SET name = $2, rename_folder = (drive_folder_id IS NOT NULL AND name <> $2) OR rename_folder
		WHERE id = $1`, id, name)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return Set{}, BadInput("another set of this vertical already has that name")
	}
	if err != nil {
		return Set{}, err
	}
	if tag.RowsAffected() == 0 {
		return Set{}, ErrNotFound
	}
	return s.GetSet(ctx, id)
}

// Sets lists sets, newest first, optionally of one vertical.
func (s *Store) Sets(ctx context.Context, vertical string, limit int) ([]Set, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(ctx, `
		SELECT s.id, s.name, COALESCE(s.vertical_id, ''), s.origin, s.origin_ref, s.made_by, s.created_at,
		       (SELECT count(*) FROM library.set_creative sc JOIN library.creative c ON c.id = sc.creative_id
		         WHERE sc.set_id = s.id AND c.hidden_at IS NULL),
		       (SELECT count(*) FROM library.set_headline sh JOIN library.headline h ON h.id = sh.headline_id
		         WHERE sh.set_id = s.id AND h.hidden_at IS NULL)
		FROM library.set s
		WHERE $1 = '' OR s.vertical_id = $1
		ORDER BY s.created_at DESC, s.id DESC
		LIMIT $2`, vertical, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Set, error) {
		var x Set
		err := r.Scan(&x.ID, &x.Name, &x.VerticalID, &x.Origin, &x.OriginRef, &x.MadeBy, &x.CreatedAt, &x.Creatives, &x.Headlines)
		return x, err
	})
}

// GetSet returns one set.
func (s *Store) GetSet(ctx context.Context, id int64) (Set, error) {
	var x Set
	err := s.db.QueryRow(ctx, `
		SELECT s.id, s.name, COALESCE(s.vertical_id, ''), s.origin, s.origin_ref, s.made_by, s.created_at,
		       (SELECT count(*) FROM library.set_creative WHERE set_id = s.id),
		       (SELECT count(*) FROM library.set_headline WHERE set_id = s.id)
		FROM library.set s WHERE s.id = $1`, id).
		Scan(&x.ID, &x.Name, &x.VerticalID, &x.Origin, &x.OriginRef, &x.MadeBy, &x.CreatedAt, &x.Creatives, &x.Headlines)
	if errors.Is(err, pgx.ErrNoRows) {
		return Set{}, ErrNotFound
	}
	return x, err
}

func setExists(ctx context.Context, tx pgx.Tx, id int64) error {
	if id == 0 {
		return nil
	}
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM library.set WHERE id = $1`, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return BadInput(fmt.Sprintf("set %d does not exist", id))
	}
	return err
}

// ---- creatives ---------------------------------------------------------------

// NewCreative is what comes with a creative's bytes.
type NewCreative struct {
	// Name is used only when there is no vertical to mint one from.
	Name         string `json:"name"`
	VerticalID   string `json:"vertical_id"`
	VerticalName string `json:"vertical_name"`
	SetID        int64  `json:"set_id"`
	Angle        string `json:"angle"`
	Idea         string `json:"idea"`
	Origin       string `json:"origin"`
	OriginRef    string `json:"origin_ref"`
	AILabel      string `json:"ai_label"`
	MadeBy       string `json:"made_by"`
	// DriveFileID is set by the Drive sync for a picture read from Drive:
	// that file already holds the bytes, so none wait for an upload.
	DriveFileID string `json:"-"`
}

// AddCreative keeps a picture: its row, thumbnail and, until the Drive sync
// uploads them, its bytes. The same bytes again return the creative that has
// them (added to the set, when one is given) and created false; when that
// creative's Drive file was deleted, these bytes take its place.
func (s *Store) AddCreative(ctx context.Context, n NewCreative, b []byte) (Creative, bool, error) {
	info, err := picture.Read(b)
	if err != nil {
		return Creative{}, false, BadInput(err.Error())
	}
	if n.Origin == "" {
		n.Origin = OriginUpload
	}
	if err := checkOrigin(n.Origin); err != nil {
		return Creative{}, false, err
	}
	if n.AILabel, err = checkAI(n.AILabel); err != nil {
		return Creative{}, false, err
	}
	if existing, err := s.creativeBySHA(ctx, info.SHA256); err == nil {
		if existing.DriveState == "gone" {
			if err := s.revive(ctx, existing.ID, n.DriveFileID, b); err != nil {
				return Creative{}, false, err
			}
			existing.DriveState = "waiting"
			if n.DriveFileID != "" {
				existing.DriveState = "in_drive"
			}
		}
		if n.SetID != 0 {
			if err := s.addToSet(ctx, n.SetID, existing.ID); err != nil {
				return Creative{}, false, err
			}
			c, err := s.Creative(ctx, existing.ID)
			return c, false, err
		}
		return existing, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Creative{}, false, err
	}

	th, err := picture.Thumb(b)
	if err != nil {
		return Creative{}, false, BadInput(err.Error())
	}
	pending, state, fileID := b, "waiting", (*string)(nil)
	if n.DriveFileID != "" {
		pending, state, fileID = nil, "in_drive", &n.DriveFileID
	}

	var id int64
	created := true
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := ensureVertical(ctx, tx, n.VerticalID, n.VerticalName); err != nil {
			return err
		}
		if err := setExists(ctx, tx, n.SetID); err != nil {
			return err
		}
		name := text.CleanLine(n.Name)
		var number *int
		if n.VerticalID != "" && n.Origin != OriginDrive {
			var code, letter string
			var next int
			if err := tx.QueryRow(ctx, `
				UPDATE library.vertical SET next_number = next_number + 1 WHERE id = $1
				RETURNING code, network_letter, next_number - 1`, n.VerticalID).Scan(&code, &letter, &next); err != nil {
				return err
			}
			name = fmt.Sprintf("%s%s%d", code, letter, next)
			number = &next
		}
		if name == "" {
			name = "AH" + info.SHA256[:8]
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO library.creative (name, vertical_id, vertical_number, angle, idea, origin, origin_ref, ai_label,
			    made_by, sha256, md5, pending, thumb, media_type, width, height, bytes, drive_state, drive_file_id)
			VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
			ON CONFLICT (sha256) DO NOTHING
			RETURNING id`,
			name, n.VerticalID, number, text.CleanLine(n.Angle), strings.TrimSpace(n.Idea), n.Origin, n.OriginRef, n.AILabel,
			n.MadeBy, info.SHA256, info.MD5, pending, th, info.MediaType, info.Width, info.Height, info.Bytes, state, fileID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			// Someone saved the same bytes a moment ago; roll back the number.
			created = false
			return errSameBytes
		}
		if err != nil {
			return err
		}
		if n.SetID != 0 {
			return addToSetTx(ctx, tx, n.SetID, id)
		}
		return nil
	})
	if errors.Is(err, errSameBytes) {
		existing, err := s.creativeBySHA(ctx, info.SHA256)
		if err != nil {
			return Creative{}, false, err
		}
		if n.SetID != 0 {
			if err := s.addToSet(ctx, n.SetID, existing.ID); err != nil {
				return Creative{}, false, err
			}
		}
		c, err := s.Creative(ctx, existing.ID)
		return c, false, err
	}
	if err != nil {
		return Creative{}, false, err
	}
	c, err := s.Creative(ctx, id)
	return c, created, err
}

var errSameBytes = errors.New("same bytes")

// revive gives a creative whose Drive file was deleted its bytes again:
// waiting for an upload, or in the Drive file fileID when one holds them.
func (s *Store) revive(ctx context.Context, id int64, fileID string, b []byte) error {
	var err error
	if fileID != "" {
		_, err = s.db.Exec(ctx, `UPDATE library.creative SET drive_state = 'in_drive', drive_file_id = $2, pending = NULL,
			drive_error = '', updated_at = now() WHERE id = $1 AND drive_state = 'gone'`, id, fileID)
	} else {
		_, err = s.db.Exec(ctx, `UPDATE library.creative SET drive_state = 'waiting', pending = $2, drive_error = '', updated_at = now()
			WHERE id = $1 AND drive_state = 'gone'`, id, b)
	}
	return err
}

// Pending returns the bytes of a creative waiting for its upload, for the
// Drive sync.
func (s *Store) Pending(ctx context.Context, id int64) ([]byte, error) {
	var b []byte
	err := s.db.QueryRow(ctx, `SELECT pending FROM library.creative WHERE id = $1`, id).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err == nil && b == nil {
		return nil, fmt.Errorf("creative %d has no bytes waiting for Drive", id)
	}
	return b, err
}

// addToSet puts a creative in a set, after what is there.
func (s *Store) addToSet(ctx context.Context, setID, creativeID int64) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := setExists(ctx, tx, setID); err != nil {
			return err
		}
		return addToSetTx(ctx, tx, setID, creativeID)
	})
}

func addToSetTx(ctx context.Context, tx pgx.Tx, setID, creativeID int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO library.set_creative (set_id, creative_id, position)
		VALUES ($1, $2, (SELECT COALESCE(max(position), 0) + 1 FROM library.set_creative WHERE set_id = $1))
		ON CONFLICT DO NOTHING`, setID, creativeID)
	return err
}

func (s *Store) creativeBySHA(ctx context.Context, sum string) (Creative, error) {
	var id int64
	err := s.db.QueryRow(ctx, `SELECT id FROM library.creative WHERE sha256 = $1`, sum).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Creative{}, ErrNotFound
	}
	if err != nil {
		return Creative{}, err
	}
	return s.Creative(ctx, id)
}

const creativeCols = `c.id, c.name, COALESCE(c.vertical_id, ''), c.angle, c.idea, c.origin, c.origin_ref, c.ai_label,
	c.made_by, c.sha256, c.media_type, c.width, c.height, c.bytes, c.drive_state, c.hidden_at IS NOT NULL, c.created_at,
	ARRAY(SELECT set_id FROM library.set_creative WHERE creative_id = c.id ORDER BY set_id)`

func scanCreative(r pgx.CollectableRow) (Creative, error) {
	var c Creative
	err := r.Scan(&c.ID, &c.Name, &c.VerticalID, &c.Angle, &c.Idea, &c.Origin, &c.OriginRef, &c.AILabel,
		&c.MadeBy, &c.SHA256, &c.MediaType, &c.Width, &c.Height, &c.Bytes, &c.DriveState, &c.Hidden, &c.CreatedAt, &c.SetIDs)
	return c, err
}

// Creative returns one creative.
func (s *Store) Creative(ctx context.Context, id int64) (Creative, error) {
	rows, err := s.db.Query(ctx, `SELECT `+creativeCols+` FROM library.creative c WHERE c.id = $1`, id)
	if err != nil {
		return Creative{}, err
	}
	c, err := pgx.CollectExactlyOneRow(rows, scanCreative)
	if errors.Is(err, pgx.ErrNoRows) {
		return Creative{}, ErrNotFound
	}
	return c, err
}

// Filter narrows a list. Empty fields do not filter.
type Filter struct {
	VerticalID string
	SetID      int64
	Angle      string
	Origin     string
	AILabel    string
	// Search matches the name, idea or angle (creatives) or the text
	// (headlines), ignoring case.
	Search string
	// Hidden lists the hidden ones instead.
	Hidden bool
	// Before is the id to continue after, from the end of the last page.
	Before int64
	Limit  int
}

func (f Filter) limit() int {
	if f.Limit <= 0 || f.Limit > 500 {
		return 120
	}
	return f.Limit
}

// Creatives lists creatives, newest first (in set order when a set is given).
func (s *Store) Creatives(ctx context.Context, f Filter) ([]Creative, error) {
	order := "c.id DESC"
	if f.SetID != 0 {
		order = "(SELECT position FROM library.set_creative WHERE set_id = $2 AND creative_id = c.id), c.id"
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+creativeCols+` FROM library.creative c
		WHERE ($1 = '' OR c.vertical_id = $1)
		  AND ($2 = 0 OR EXISTS (SELECT 1 FROM library.set_creative WHERE set_id = $2 AND creative_id = c.id))
		  AND ($3 = '' OR c.angle = $3)
		  AND ($4 = '' OR c.origin = $4)
		  AND ($5 = '' OR c.ai_label = $5)
		  AND ($6 = '' OR c.name ILIKE '%' || $6 || '%' OR c.idea ILIKE '%' || $6 || '%' OR c.angle ILIKE '%' || $6 || '%')
		  AND (c.hidden_at IS NOT NULL) = $7
		  AND ($8 = 0 OR c.id < $8)
		ORDER BY `+order+`
		LIMIT $9`,
		f.VerticalID, f.SetID, f.Angle, f.Origin, f.AILabel, likeEscape(f.Search), f.Hidden, f.Before, f.limit())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanCreative)
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSpace(s))
}

// Change is an edit to a creative or headline. Nil fields stay.
type Change struct {
	Angle   *string `json:"angle"`
	AILabel *string `json:"ai_label"`
	Hidden  *bool   `json:"hidden"`
	// AddToSet puts it in one more set.
	AddToSet int64 `json:"add_to_set"`
}

func (c Change) check() error {
	if c.AILabel != nil {
		if _, err := checkAI(*c.AILabel); err != nil || *c.AILabel == "" {
			return BadInput("ai_label must be unset, ai or not_ai")
		}
	}
	return nil
}

// ChangeCreative edits a creative.
func (s *Store) ChangeCreative(ctx context.Context, id int64, c Change) (Creative, error) {
	if err := c.check(); err != nil {
		return Creative{}, err
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE library.creative SET
				angle = COALESCE($2, angle),
				ai_label = COALESCE($3, ai_label),
				hidden_at = CASE WHEN $4::boolean IS NULL THEN hidden_at
				                 WHEN $4 THEN COALESCE(hidden_at, now()) ELSE NULL END,
				updated_at = now()
			WHERE id = $1`, id, trimPtr(c.Angle), c.AILabel, c.Hidden)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if c.AddToSet != 0 {
			if err := setExists(ctx, tx, c.AddToSet); err != nil {
				return err
			}
			return addToSetTx(ctx, tx, c.AddToSet, id)
		}
		return nil
	})
	if err != nil {
		return Creative{}, err
	}
	return s.Creative(ctx, id)
}

// Open returns a creative's bytes (or its thumbnail) and media type: the
// bytes waiting for their upload, else its file in Drive.
func (s *Store) Open(ctx context.Context, id int64, thumb bool) (io.ReadCloser, string, error) {
	var b []byte
	var mt, state string
	var fileID *string
	err := s.db.QueryRow(ctx, `SELECT CASE WHEN $2 THEN thumb ELSE pending END, CASE WHEN $2 THEN 'image/jpeg' ELSE media_type END,
		drive_state, drive_file_id FROM library.creative WHERE id = $1`, id, thumb).Scan(&b, &mt, &state, &fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	switch {
	case b != nil:
	case thumb:
		return nil, "", ErrNotFound
	case state != "in_drive" || fileID == nil:
		return nil, "", ErrGone
	default:
		d, err := s.drive(ctx)
		if err != nil {
			return nil, "", err
		}
		if b, err = d.Download(ctx, *fileID); err != nil {
			var nf notFounder
			if errors.As(err, &nf) && nf.NotFound() {
				return nil, "", ErrGone
			}
			return nil, "", err
		}
	}
	return io.NopCloser(bytes.NewReader(b)), mt, nil
}

// ---- headlines ---------------------------------------------------------------

// NewHeadline is one headline an app keeps.
type NewHeadline struct {
	Text         string `json:"text"`
	VerticalID   string `json:"vertical_id"`
	VerticalName string `json:"vertical_name"`
	SetID        int64  `json:"set_id"`
	Angle        string `json:"angle"`
	Origin       string `json:"origin"`
	OriginRef    string `json:"origin_ref"`
	AILabel      string `json:"ai_label"`
	MadeBy       string `json:"made_by"`
}

// MaxHeadline is the longest headline kept, in characters. Taboola's hard
// ceiling is lower (60 is its guideline); the library keeps what people
// wrote and the apps warn.
const MaxHeadline = 200

// HeadlineSHA is the hash a headline is found by: of its cleaned text.
func HeadlineSHA(t string) string {
	s := sha256.Sum256([]byte(text.CleanLine(t)))
	return hex.EncodeToString(s[:])
}

// AddHeadlines keeps headlines, cleaned of hidden characters. A text already
// kept returns the headline that has it (added to the set, when given).
func (s *Store) AddHeadlines(ctx context.Context, in []NewHeadline) ([]Headline, error) {
	if len(in) == 0 {
		return nil, BadInput("no headlines")
	}
	if len(in) > 500 {
		return nil, BadInput("500 headlines at most in one call")
	}
	ids := make([]int64, 0, len(in))
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		for i, h := range in {
			t := text.CleanLine(h.Text)
			if t == "" {
				return BadInput(fmt.Sprintf("headline %d is empty", i+1))
			}
			if len([]rune(t)) > MaxHeadline {
				return BadInput(fmt.Sprintf("headline %d is over %d characters", i+1, MaxHeadline))
			}
			if h.Origin == "" {
				h.Origin = OriginUpload
			}
			if err := checkOrigin(h.Origin); err != nil {
				return err
			}
			label, err := checkAI(h.AILabel)
			if err != nil {
				return err
			}
			if err := ensureVertical(ctx, tx, h.VerticalID, h.VerticalName); err != nil {
				return err
			}
			if err := setExists(ctx, tx, h.SetID); err != nil {
				return err
			}
			var id int64
			err = tx.QueryRow(ctx, `
				WITH ins AS (
					INSERT INTO library.headline (text, sha256, vertical_id, angle, origin, origin_ref, ai_label, made_by)
					VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, $7, $8)
					ON CONFLICT (sha256) DO NOTHING
					RETURNING id)
				SELECT id FROM ins UNION ALL SELECT id FROM library.headline WHERE sha256 = $2 LIMIT 1`,
				t, HeadlineSHA(t), h.VerticalID, text.CleanLine(h.Angle), h.Origin, h.OriginRef, label, h.MadeBy).Scan(&id)
			if err != nil {
				return err
			}
			if h.SetID != 0 {
				if _, err := tx.Exec(ctx, `
					INSERT INTO library.set_headline (set_id, headline_id, position)
					VALUES ($1, $2, (SELECT COALESCE(max(position), 0) + 1 FROM library.set_headline WHERE set_id = $1))
					ON CONFLICT DO NOTHING`, h.SetID, id); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE library.set SET headlines_changed_at = now() WHERE id = $1`, h.SetID); err != nil {
					return err
				}
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]Headline, 0, len(ids))
	for _, id := range ids {
		h, err := s.Headline(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

const headlineCols = `h.id, h.text, h.sha256, COALESCE(h.vertical_id, ''), h.angle, h.origin, h.origin_ref, h.ai_label,
	h.made_by, h.hidden_at IS NOT NULL, h.created_at,
	ARRAY(SELECT set_id FROM library.set_headline WHERE headline_id = h.id ORDER BY set_id)`

func scanHeadline(r pgx.CollectableRow) (Headline, error) {
	var h Headline
	err := r.Scan(&h.ID, &h.Text, &h.SHA256, &h.VerticalID, &h.Angle, &h.Origin, &h.OriginRef, &h.AILabel,
		&h.MadeBy, &h.Hidden, &h.CreatedAt, &h.SetIDs)
	return h, err
}

// Headline returns one headline.
func (s *Store) Headline(ctx context.Context, id int64) (Headline, error) {
	rows, err := s.db.Query(ctx, `SELECT `+headlineCols+` FROM library.headline h WHERE h.id = $1`, id)
	if err != nil {
		return Headline{}, err
	}
	h, err := pgx.CollectExactlyOneRow(rows, scanHeadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return Headline{}, ErrNotFound
	}
	return h, err
}

// Headlines lists headlines, newest first (in set order when a set is given).
func (s *Store) Headlines(ctx context.Context, f Filter) ([]Headline, error) {
	order := "h.id DESC"
	if f.SetID != 0 {
		order = "(SELECT position FROM library.set_headline WHERE set_id = $2 AND headline_id = h.id), h.id"
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+headlineCols+` FROM library.headline h
		WHERE ($1 = '' OR h.vertical_id = $1)
		  AND ($2 = 0 OR EXISTS (SELECT 1 FROM library.set_headline WHERE set_id = $2 AND headline_id = h.id))
		  AND ($3 = '' OR h.angle = $3)
		  AND ($4 = '' OR h.origin = $4)
		  AND ($5 = '' OR h.ai_label = $5)
		  AND ($6 = '' OR h.text ILIKE '%' || $6 || '%')
		  AND (h.hidden_at IS NOT NULL) = $7
		  AND ($8 = 0 OR h.id < $8)
		ORDER BY `+order+`
		LIMIT $9`,
		f.VerticalID, f.SetID, f.Angle, f.Origin, f.AILabel, likeEscape(f.Search), f.Hidden, f.Before, f.limit())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanHeadline)
}

// ChangeHeadline edits a headline. Its text never changes: a new text is a
// new headline, because the ad id is made from it.
func (s *Store) ChangeHeadline(ctx context.Context, id int64, c Change) (Headline, error) {
	if err := c.check(); err != nil {
		return Headline{}, err
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE library.headline SET
				angle = COALESCE($2, angle),
				ai_label = COALESCE($3, ai_label),
				hidden_at = CASE WHEN $4::boolean IS NULL THEN hidden_at
				                 WHEN $4 THEN COALESCE(hidden_at, now()) ELSE NULL END,
				updated_at = now()
			WHERE id = $1`, id, trimPtr(c.Angle), c.AILabel, c.Hidden)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if c.AddToSet != 0 {
			if err := setExists(ctx, tx, c.AddToSet); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO library.set_headline (set_id, headline_id, position)
				VALUES ($1, $2, (SELECT COALESCE(max(position), 0) + 1 FROM library.set_headline WHERE set_id = $1))
				ON CONFLICT DO NOTHING`, c.AddToSet, id); err != nil {
				return err
			}
		}
		// Hiding or adding changes what the set's headline file says.
		_, err = tx.Exec(ctx, `UPDATE library.set SET headlines_changed_at = now()
			WHERE id IN (SELECT set_id FROM library.set_headline WHERE headline_id = $1)`, id)
		return err
	})
	if err != nil {
		return Headline{}, err
	}
	return s.Headline(ctx, id)
}

// ---- the old bucket ----------------------------------------------------------

// TakeFromBucket moves what the old bucket held into the rows, once, after
// the library went Drive only: each thumbnail, and the bytes of each
// creative still waiting for its upload. get reads a key from the bucket. It
// returns how many creatives it filled in; a key the bucket does not have is
// logged by the caller from the error and skipped.
func (s *Store) TakeFromBucket(ctx context.Context, get func(context.Context, string) (io.ReadCloser, error)) (int, []error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, CASE WHEN thumb IS NULL THEN thumb_key ELSE '' END,
		       CASE WHEN pending IS NULL AND drive_state = 'waiting' THEN file_key ELSE '' END
		FROM library.creative
		WHERE (thumb IS NULL AND thumb_key <> '') OR (pending IS NULL AND drive_state = 'waiting' AND file_key <> '')
		ORDER BY id`)
	if err != nil {
		return 0, []error{err}
	}
	type old struct {
		id         int64
		thumb, raw string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (old, error) {
		var o old
		err := r.Scan(&o.id, &o.thumb, &o.raw)
		return o, err
	})
	if err != nil {
		return 0, []error{err}
	}
	read := func(key string) ([]byte, error) {
		rc, err := get(ctx, key)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	n := 0
	var errs []error
	for _, o := range list {
		var th, raw []byte
		if o.thumb != "" {
			if th, err = read(o.thumb); err != nil {
				errs = append(errs, fmt.Errorf("creative %d thumbnail %s: %w", o.id, o.thumb, err))
			}
		}
		if o.raw != "" {
			if raw, err = read(o.raw); err != nil {
				errs = append(errs, fmt.Errorf("creative %d file %s: %w", o.id, o.raw, err))
			}
		}
		if th == nil && raw == nil {
			continue
		}
		if _, err := s.db.Exec(ctx, `UPDATE library.creative SET thumb = COALESCE(thumb, $2), pending = COALESCE(pending, $3) WHERE id = $1`,
			o.id, th, raw); err != nil {
			return n, append(errs, err)
		}
		n++
	}
	return n, errs
}
