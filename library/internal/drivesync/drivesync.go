// Package drivesync keeps the team's Google Drive folder and the library in
// step. One pass:
//
//  1. Out: every creative not in Drive yet is uploaded to
//     <vertical>/<set>/<name>.<ext>, and the bytes that waited for it in its
//     row are cleared: from then on Drive holds them and nothing else does
//     (decisions/0020-library-on-drive-only.md). Each set whose headlines
//     changed gets its "Headlines.txt" written again. Our ids ride on each
//     file as Drive app properties, which only the library can read.
//  2. In: the whole folder is listed (each page kept raw in
//     library.drive_page). A picture the library does not have is
//     downloaded once, for its hash and thumbnail, and added as a creative
//     whose bytes are that Drive file: its vertical from the top folder's
//     name, its set from the folder it sits in.
//  3. Gone: after a whole listing, a file that was not in it is marked gone,
//     and so is its creative. The creative's row and thumbnail stay; its
//     bytes went with the file.
//
// Nothing is ever deleted in Drive.
package drivesync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Raposa-Industries/adhunters/library/internal/drive"
	"github.com/Raposa-Industries/adhunters/library/internal/picture"
	"github.com/Raposa-Industries/adhunters/library/internal/store"
)

// App property keys the library puts on the files it knows.
const (
	PropKind = "ahLibrary" // creative, headlines, vertical, set
	PropID   = "ahId"
)

// HeadlinesFile is the name of a set's headline file in Drive.
const HeadlinesFile = "Headlines.txt"

// MaxWrites is how many creatives one pass uploads; the rest wait for the
// next pass.
const MaxWrites = 200

// Result is what one pass did.
type Result struct {
	Written int
	Listed  int
	Added   int
	Gone    int
	Pages   []string
}

// Syncer runs passes. Only one runs at a time.
type Syncer struct {
	st    *store.Store
	drive *drive.Client
	root  string
	log   *slog.Logger
	mu    sync.Mutex
}

// New returns a syncer for the library folder root.
func New(st *store.Store, d *drive.Client, root string, log *slog.Logger) *Syncer {
	return &Syncer{st: st, drive: d, root: root, log: log}
}

// Run does one pass and records it in library.drive_run.
func (s *Syncer) Run(ctx context.Context) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db := s.st.DB()
	var runID int64
	var started time.Time
	if err := db.QueryRow(ctx, `INSERT INTO library.drive_run DEFAULT VALUES RETURNING id, started_at`).Scan(&runID, &started); err != nil {
		return Result{}, err
	}
	res := Result{Pages: []string{}}
	err := s.out(ctx, &res)
	if err == nil {
		err = s.in(ctx, started, &res)
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if _, uerr := db.Exec(context.WithoutCancel(ctx), `
		UPDATE library.drive_run SET finished_at = now(), written = $2, listed = $3, added = $4, gone = $5, pages = $6, error = $7
		WHERE id = $1`, runID, res.Written, res.Listed, res.Added, res.Gone, res.Pages, msg); uerr != nil && err == nil {
		err = uerr
	}
	return res, err
}

// ---- out ---------------------------------------------------------------------

func (s *Syncer) out(ctx context.Context, res *Result) error {
	if err := s.renames(ctx, res); err != nil {
		return err
	}
	db := s.st.DB()
	rows, err := db.Query(ctx, `
		SELECT c.id, c.name, c.media_type, COALESCE(c.vertical_id, ''),
		       COALESCE((SELECT min(set_id) FROM library.set_creative WHERE creative_id = c.id), 0)
		FROM library.creative c WHERE c.drive_state = 'waiting' ORDER BY c.id LIMIT $1`, MaxWrites)
	if err != nil {
		return err
	}
	type waiting struct {
		id               int64
		name, mt, vertID string
		setID            int64
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (waiting, error) {
		var w waiting
		err := r.Scan(&w.id, &w.name, &w.mt, &w.vertID, &w.setID)
		return w, err
	})
	if err != nil {
		return err
	}
	for _, w := range list {
		if err := ctx.Err(); err != nil {
			return err
		}
		folder, err := s.folderFor(ctx, w.vertID, w.setID)
		if err != nil {
			return err
		}
		if err := s.upload(ctx, w.id, w.name+picture.Ext(w.mt), w.mt, folder); err != nil {
			if errors.Is(err, drive.ErrSignedOut) || ctx.Err() != nil {
				return err
			}
			s.log.Error("creative not copied to drive", "creative", w.id, "err", err)
			_, _ = db.Exec(ctx, `UPDATE library.creative SET drive_error = $2 WHERE id = $1`, w.id, err.Error())
			continue
		}
		res.Written++
	}
	return s.headlines(ctx, res)
}

// renames gives each renamed set's Drive folder the set's new name.
func (s *Syncer) renames(ctx context.Context, res *Result) error {
	db := s.st.DB()
	rows, err := db.Query(ctx, `SELECT id, name, drive_folder_id FROM library.set WHERE rename_folder AND drive_folder_id IS NOT NULL ORDER BY id`)
	if err != nil {
		return err
	}
	type renamed struct {
		id           int64
		name, folder string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (renamed, error) {
		var x renamed
		err := r.Scan(&x.id, &x.name, &x.folder)
		return x, err
	})
	if err != nil {
		return err
	}
	for _, x := range list {
		err := s.drive.Rename(ctx, x.folder, x.name)
		if err != nil && !isNotFound(err) {
			if errors.Is(err, drive.ErrSignedOut) || ctx.Err() != nil {
				return err
			}
			s.log.Error("set folder not renamed in drive", "set", x.id, "err", err)
			continue
		}
		// A folder no longer there is made again, with the new name, when
		// something next goes in it.
		if _, err := db.Exec(ctx, `UPDATE library.set SET rename_folder = false WHERE id = $1 AND name = $2`, x.id, x.name); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `UPDATE library.drive_file SET name = $2 WHERE file_id = $1`, x.folder, x.name); err != nil {
			return err
		}
		res.Written++
	}
	return nil
}

func (s *Syncer) upload(ctx context.Context, id int64, name, mt, folder string) error {
	data, err := s.st.Pending(ctx, id)
	if err != nil {
		return err
	}
	f, err := s.drive.Upload(ctx, folder, name, mt, data, map[string]string{PropKind: "creative", PropID: strconv.FormatInt(id, 10)})
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.st.DB(), func(tx pgx.Tx) error {
		if err := recordFile(ctx, tx, f, "image", id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE library.creative SET drive_state = 'in_drive', drive_file_id = $2, pending = NULL,
			drive_error = '', updated_at = now() WHERE id = $1`, id, f.ID)
		return err
	})
}

// headlines writes each changed set's Headlines.txt: its headlines not
// hidden, one per line.
func (s *Syncer) headlines(ctx context.Context, res *Result) error {
	db := s.st.DB()
	rows, err := db.Query(ctx, `
		SELECT id, COALESCE(vertical_id, ''), COALESCE(headlines_file_id, ''), headlines_changed_at
		FROM library.set
		WHERE headlines_changed_at IS NOT NULL
		  AND (headlines_written_at IS NULL OR headlines_written_at < headlines_changed_at)
		ORDER BY id`)
	if err != nil {
		return err
	}
	type changed struct {
		id      int64
		vertID  string
		fileID  string
		changed time.Time
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (changed, error) {
		var c changed
		err := r.Scan(&c.id, &c.vertID, &c.fileID, &c.changed)
		return c, err
	})
	if err != nil {
		return err
	}
	for _, c := range list {
		hs, err := s.st.Headlines(ctx, store.Filter{SetID: c.id, Limit: 500})
		if err != nil {
			return err
		}
		var b strings.Builder
		for _, h := range hs {
			b.WriteString(h.Text)
			b.WriteString("\n")
		}
		data := []byte(b.String())
		var f drive.File
		if c.fileID != "" {
			f, err = s.drive.Replace(ctx, c.fileID, "text/plain", data)
		}
		if c.fileID == "" || isNotFound(err) {
			var folder string
			if folder, err = s.folderFor(ctx, c.vertID, c.id); err != nil {
				return err
			}
			f, err = s.drive.Upload(ctx, folder, HeadlinesFile, "text/plain", data,
				map[string]string{PropKind: "headlines", PropID: strconv.FormatInt(c.id, 10)})
		}
		if err != nil {
			if errors.Is(err, drive.ErrSignedOut) || ctx.Err() != nil {
				return err
			}
			s.log.Error("set headlines not written to drive", "set", c.id, "err", err)
			continue
		}
		err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			if err := recordFile(ctx, tx, f, "headlines", 0); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE library.set SET headlines_file_id = $2, headlines_written_at = $3 WHERE id = $1`, c.id, f.ID, c.changed)
			return err
		})
		if err != nil {
			return err
		}
		res.Written++
	}
	return nil
}

func isNotFound(err error) bool {
	var e *drive.Error
	return errors.As(err, &e) && e.Status == 404
}

// folderFor is the Drive folder a creative or a set's headlines go in: the
// set's folder inside the vertical's (inside its platform's folder there,
// <vertical>/<platform>/<set>, for a set made with a platform), the
// vertical's, or the library folder. A set that already has its folder keeps
// it wherever it is.
func (s *Syncer) folderFor(ctx context.Context, vertID string, setID int64) (string, error) {
	db := s.st.DB()
	parent := s.root
	if vertID != "" {
		var name string
		var id *string
		if err := db.QueryRow(ctx, `SELECT name, drive_folder_id FROM library.vertical WHERE id = $1`, vertID).Scan(&name, &id); err != nil {
			return "", err
		}
		f, err := s.ensureFolder(ctx, id, parent, name, "vertical", vertID)
		if err != nil {
			return "", err
		}
		if id == nil || *id != f {
			if _, err := db.Exec(ctx, `UPDATE library.vertical SET drive_folder_id = $2 WHERE id = $1`, vertID, f); err != nil {
				return "", err
			}
		}
		parent = f
	}
	if setID != 0 {
		var name, setVert, platform string
		var id *string
		if err := db.QueryRow(ctx, `SELECT name, COALESCE(vertical_id, ''), drive_folder_id, COALESCE(platform, '') FROM library.set WHERE id = $1`,
			setID).Scan(&name, &setVert, &id, &platform); err != nil {
			return "", err
		}
		// A set of another vertical (a creative saved into two) keeps its
		// own place; a creative goes under its first set only when they agree.
		if setVert == vertID {
			setParent := parent
			if pf := store.PlatformFolder(platform); pf != "" && vertID != "" && !s.folderLive(ctx, id) {
				f, err := s.ensureFolder(ctx, nil, parent, pf, "platform", vertID+"/"+platform)
				if err != nil {
					return "", err
				}
				setParent = f
			}
			f, err := s.ensureFolder(ctx, id, setParent, name, "set", strconv.FormatInt(setID, 10))
			if err != nil {
				return "", err
			}
			if id == nil || *id != f {
				if _, err := db.Exec(ctx, `UPDATE library.set SET drive_folder_id = $2 WHERE id = $1`, setID, f); err != nil {
					return "", err
				}
			}
			parent = f
		}
	}
	return parent, nil
}

// folderLive reports whether the folder known by id is still in Drive.
func (s *Syncer) folderLive(ctx context.Context, id *string) bool {
	if id == nil || *id == "" {
		return false
	}
	var gone bool
	err := s.st.DB().QueryRow(ctx, `SELECT gone_at IS NOT NULL FROM library.drive_file WHERE file_id = $1`, *id).Scan(&gone)
	return err == nil && !gone
}

// ensureFolder returns the folder known by id when it is still there, else a
// folder of that name already in parent, else a new one.
func (s *Syncer) ensureFolder(ctx context.Context, id *string, parent, name, kind, ref string) (string, error) {
	db := s.st.DB()
	if id != nil && *id != "" {
		var gone bool
		err := db.QueryRow(ctx, `SELECT gone_at IS NOT NULL FROM library.drive_file WHERE file_id = $1`, *id).Scan(&gone)
		if err == nil && !gone {
			return *id, nil
		}
	}
	var found string
	err := db.QueryRow(ctx, `SELECT file_id FROM library.drive_file
		WHERE kind = 'folder' AND parent_id = $1 AND lower(name) = lower($2) AND gone_at IS NULL
		ORDER BY first_seen_at LIMIT 1`, parent, name).Scan(&found)
	if err == nil {
		return found, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	f, err := s.drive.CreateFolder(ctx, parent, name, map[string]string{PropKind: kind, PropID: ref})
	if err != nil {
		return "", err
	}
	p, err := s.pathOf(ctx, parent)
	if err != nil {
		return "", err
	}
	_, err = db.Exec(ctx, `INSERT INTO library.drive_file (file_id, kind, name, mime_type, parent_id, path, modified_at)
		VALUES ($1, 'folder', $2, $3, $4, $5, $6) ON CONFLICT (file_id) DO NOTHING`,
		f.ID, f.Name, f.MimeType, parent, p, nullTime(f.ModifiedTime))
	return f.ID, err
}

// pathOf is the folder path of a folder under the library folder ("" for
// the library folder itself).
func (s *Syncer) pathOf(ctx context.Context, folder string) (string, error) {
	if folder == s.root {
		return "", nil
	}
	var p, name string
	err := s.st.DB().QueryRow(ctx, `SELECT path, name FROM library.drive_file WHERE file_id = $1`, folder).Scan(&p, &name)
	if err != nil {
		return "", err
	}
	return joinPath(p, name), nil
}

func joinPath(p, name string) string {
	if p == "" {
		return name
	}
	return p + "/" + name
}

func recordFile(ctx context.Context, tx pgx.Tx, f drive.File, kind string, creative int64) error {
	parent := ""
	if len(f.Parents) > 0 {
		parent = f.Parents[0]
	}
	var cid *int64
	if creative != 0 {
		cid = &creative
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO library.drive_file (file_id, kind, name, mime_type, md5, size, parent_id, path, modified_at, creative_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7,
		        COALESCE((SELECT CASE WHEN path = '' THEN name ELSE path || '/' || name END FROM library.drive_file WHERE file_id = $7), ''),
		        $8, $9)
		ON CONFLICT (file_id) DO UPDATE SET name = EXCLUDED.name, md5 = EXCLUDED.md5, size = EXCLUDED.size,
		    modified_at = EXCLUDED.modified_at, creative_id = COALESCE(EXCLUDED.creative_id, library.drive_file.creative_id),
		    seen_at = now(), gone_at = NULL, error = ''`,
		f.ID, kind, f.Name, f.MimeType, f.MD5, f.Bytes(), parent, nullTime(f.ModifiedTime), cid)
	return err
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// ---- in ----------------------------------------------------------------------

var pictureTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true, "image/gif": true}

type folderAt struct {
	id, path string
	depth    int
}

func (s *Syncer) in(ctx context.Context, started time.Time, res *Result) error {
	queue := []folderAt{{id: s.root}}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		token := ""
		for {
			page, err := s.drive.List(ctx, dir.id, token)
			if err != nil {
				return err
			}
			key, err := s.keepRaw(ctx, page.Raw)
			if err != nil {
				return err
			}
			res.Pages = append(res.Pages, key)
			for _, f := range page.Files {
				res.Listed++
				sub, err := s.seen(ctx, f, dir, res)
				if err != nil {
					if errors.Is(err, drive.ErrSignedOut) || ctx.Err() != nil {
						return err
					}
					s.log.Error("drive file not read", "file", f.ID, "name", f.Name, "err", err)
					_, _ = s.st.DB().Exec(ctx, `UPDATE library.drive_file SET error = $2, seen_at = now() WHERE file_id = $1`, f.ID, err.Error())
					continue
				}
				if sub != nil {
					queue = append(queue, *sub)
				}
			}
			if page.Next == "" {
				break
			}
			token = page.Next
		}
	}
	return s.gone(ctx, started, res)
}

// keepRaw stores a listing page as it came, in library.drive_page, and
// returns its sha256; identical pages share one row.
func (s *Syncer) keepRaw(ctx context.Context, raw []byte) (string, error) {
	h := sha256.Sum256(raw)
	sum := hex.EncodeToString(h[:])
	_, err := s.st.DB().Exec(ctx, `INSERT INTO library.drive_page (sha256, body) VALUES ($1, $2) ON CONFLICT DO NOTHING`, sum, raw)
	return sum, err
}

// seen handles one listed file and returns the folder to list next, when it
// is one.
func (s *Syncer) seen(ctx context.Context, f drive.File, dir folderAt, res *Result) (*folderAt, error) {
	db := s.st.DB()
	p := dir.path
	switch {
	case f.IsFolder():
		_, err := db.Exec(ctx, `
			INSERT INTO library.drive_file (file_id, kind, name, mime_type, parent_id, path, modified_at)
			VALUES ($1, 'folder', $2, $3, $4, $5, $6)
			ON CONFLICT (file_id) DO UPDATE SET name = EXCLUDED.name, parent_id = EXCLUDED.parent_id, path = EXCLUDED.path,
			    modified_at = EXCLUDED.modified_at, seen_at = now(), gone_at = NULL, error = ''`,
			f.ID, f.Name, f.MimeType, dir.id, p, nullTime(f.ModifiedTime))
		return &folderAt{id: f.ID, path: joinPath(p, f.Name), depth: dir.depth + 1}, err
	case f.AppProperties[PropKind] == "headlines":
		return nil, s.touch(ctx, f, "headlines", dir, 0)
	case !pictureTypes[f.MimeType]:
		return nil, s.touch(ctx, f, "other", dir, 0)
	}

	// A picture. Known with the same bytes: nothing to read.
	var known int64
	var md5 string
	err := db.QueryRow(ctx, `SELECT COALESCE(creative_id, 0), md5 FROM library.drive_file WHERE file_id = $1`, f.ID).Scan(&known, &md5)
	if err == nil && known != 0 && md5 == f.MD5 {
		return nil, s.touch(ctx, f, "image", dir, known)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if f.Bytes() > picture.MaxBytes {
		return nil, fmt.Errorf("%s is %d MB, over the library's %d MB", f.Name, f.Bytes()>>20, picture.MaxBytes>>20)
	}
	data, err := s.drive.Download(ctx, f.ID)
	if err != nil {
		return nil, err
	}
	vertID, setID, err := s.placeOf(ctx, dir)
	if err != nil {
		return nil, err
	}
	stem := strings.TrimSuffix(f.Name, path.Ext(f.Name))
	c, created, err := s.st.AddCreative(ctx, store.NewCreative{
		Name: stem, VerticalID: vertID, SetID: setID, Origin: store.OriginDrive, OriginRef: "drive:" + f.ID, DriveFileID: f.ID,
	}, data)
	if err != nil {
		return nil, err
	}
	if created {
		res.Added++
	}
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if err := recordFile(ctx, tx, withParent(f, dir.id), "image", c.ID); err != nil {
			return err
		}
		// The file's bytes changed: the creative it held is no longer there.
		if known != 0 && known != c.ID {
			if _, err := tx.Exec(ctx, `UPDATE library.creative SET drive_state = 'gone', updated_at = now()
				WHERE id = $1 AND drive_file_id = $2`, known, f.ID); err != nil {
				return err
			}
		}
		// Bytes an app saved that someone also put in Drive: this file
		// holds them, so they need not be uploaded.
		_, err := tx.Exec(ctx, `UPDATE library.creative SET drive_state = 'in_drive', drive_file_id = $2, pending = NULL,
			drive_error = '', updated_at = now() WHERE id = $1 AND drive_state <> 'in_drive'`, c.ID, f.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if f.AppProperties[PropID] != strconv.FormatInt(c.ID, 10) {
		if err := s.drive.Label(ctx, f.ID, map[string]string{PropKind: "creative", PropID: strconv.FormatInt(c.ID, 10)}); err != nil {
			s.log.Warn("drive file not labelled", "file", f.ID, "err", err)
		}
	}
	return nil, nil
}

func withParent(f drive.File, parent string) drive.File {
	if len(f.Parents) == 0 {
		f.Parents = []string{parent}
	}
	return f
}

func (s *Syncer) touch(ctx context.Context, f drive.File, kind string, dir folderAt, creative int64) error {
	return pgx.BeginFunc(ctx, s.st.DB(), func(tx pgx.Tx) error {
		return recordFile(ctx, tx, withParent(f, dir.id), kind, creative)
	})
}

// placeOf reads a vertical and a set from where a file sits: the top folder
// names the vertical (by name or code), and the folder it is directly in,
// when that is below the vertical's, names the set. A platform's folder
// (<vertical>/Taboola) is no set: the sets are the folders inside it, and
// one a person made there by hand gets that platform.
func (s *Syncer) placeOf(ctx context.Context, dir folderAt) (string, int64, error) {
	if dir.path == "" {
		return "", 0, nil
	}
	db := s.st.DB()
	top, _, _ := strings.Cut(dir.path, "/")
	var vertID string
	err := db.QueryRow(ctx, `SELECT id FROM library.vertical WHERE lower(name) = lower($1) OR code = upper($1) ORDER BY id LIMIT 1`, top).Scan(&vertID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", 0, err
	}
	if dir.depth < 2 {
		return vertID, 0, nil
	}
	parts := strings.Split(dir.path, "/")
	if dir.depth == 2 && store.PlatformOfFolder(parts[len(parts)-1]) != "" {
		return vertID, 0, nil
	}
	platform := ""
	if len(parts) == 3 {
		platform = store.PlatformOfFolder(parts[1])
	}
	var setID int64
	err = db.QueryRow(ctx, `SELECT id FROM library.set WHERE drive_folder_id = $1`, dir.id).Scan(&setID)
	if err == nil {
		return vertID, setID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", 0, err
	}
	name := path.Base(dir.path)
	set, err := s.st.AddSet(ctx, store.NewSet{Name: name, VerticalID: vertID, Origin: store.OriginDrive, OriginRef: "drive:" + dir.id,
		Platform: platform})
	if err != nil {
		return "", 0, err
	}
	_, err = db.Exec(ctx, `UPDATE library.set SET drive_folder_id = $2 WHERE id = $1`, set.ID, dir.id)
	return vertID, set.ID, err
}

// gone marks what the whole listing did not see.
func (s *Syncer) gone(ctx context.Context, started time.Time, res *Result) error {
	return pgx.BeginFunc(ctx, s.st.DB(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE library.drive_file SET gone_at = now() WHERE gone_at IS NULL AND seen_at < $1`, started)
		if err != nil {
			return err
		}
		res.Gone = int(tag.RowsAffected())
		_, err = tx.Exec(ctx, `
			UPDATE library.creative c SET drive_state = 'gone', updated_at = now()
			FROM library.drive_file f
			WHERE f.file_id = c.drive_file_id AND f.gone_at IS NOT NULL AND c.drive_state = 'in_drive'`)
		return err
	})
}
