package sessions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Raposa-Industries/adhunters/create/internal/library"
	"github.com/Raposa-Industries/adhunters/create/internal/openai"
	"github.com/Raposa-Industries/adhunters/shared/files"
)

// AI is what the worker needs of OpenAI (*openai.Client).
type AI interface {
	Plan(ctx context.Context, r openai.PlanRequest) (openai.Plan, error)
	Image(ctx context.Context, r openai.ImageRequest) (openai.Image, error)
	// Why is the reason making is off, "" when it is on.
	Why() string
}

// Headliner is another text model that may write a turn's headlines
// (decision 0023): an *openai.Client made by openai.NewCompat.
type Headliner interface {
	Plan(ctx context.Context, r openai.PlanRequest) (openai.Plan, error)
}

// Library is what the worker needs of the library (*library.Client).
type Library interface {
	AddSet(ctx context.Context, s library.NewSet) (library.Set, error)
	AddCreative(ctx context.Context, meta library.CreativeMeta, filename string, data []byte) (library.Creative, error)
	AddHeadlines(ctx context.Context, hs []library.NewHeadline) error
	// Headlines are the library's headline texts of a vertical, newest first.
	Headlines(ctx context.Context, vertical string, limit int) ([]string, error)
}

// memoryLines is the most headlines of each kind (the session's, the
// library's) read for the headline memory; openai trims them to its budget.
const memoryLines = 400

// Worker runs Create's work: a turn (its headlines and the briefs of its
// pictures, in one text call), each picture (one call each, so one failing
// never stops the others), and saves into the library.
type Worker struct {
	st   *Store
	ai   AI
	lib  Library
	log  *slog.Logger
	kick chan struct{}

	// Workers is how many pieces of work run at once.
	Workers int
	// Poll is how often the queue is looked at without a kick (work from
	// create_api arrives that way).
	Poll time.Duration
	// WorkTimeout bounds one piece of work.
	WorkTimeout time.Duration
	// Done, when set, is told of each finished piece of work.
	Done func(kind string, start time.Time, err error)
	// Headliners are the other headline models that are on, by id.
	Headliners map[string]Headliner
}

// NewWorker returns a worker. Store.New's kick should be w.Kick.
func NewWorker(st *Store, ai AI, lib Library, log *slog.Logger) *Worker {
	return &Worker{st: st, ai: ai, lib: lib, log: log, kick: make(chan struct{}, 1), Workers: 3, Poll: 2 * time.Second,
		WorkTimeout: 10 * time.Minute}
}

// Kick wakes the worker; it never waits.
func (w *Worker) Kick() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// Run works until ctx ends. At its start, work left running by a stop is
// put right: a picture fails (it may have been paid for), anything else
// waits to run again.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.reconcile(ctx); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for i := 0; i < w.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.loop(ctx)
		}()
	}
	wg.Wait()
	return nil
}

func (w *Worker) loop(ctx context.Context) {
	t := time.NewTicker(w.Poll)
	defer t.Stop()
	for ctx.Err() == nil {
		ran, err := w.RunOne(ctx)
		if err != nil && ctx.Err() == nil {
			w.log.Error("create work", "err", err)
		}
		if ran {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.kick:
		}
	}
}

const interrupted = "interrompida: o Create reiniciou durante a imagem"

func (w *Worker) reconcile(ctx context.Context) error {
	return pgx.BeginFunc(ctx, w.st.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE create_app.work SET state = 'failed', error = $1, finished_at = now()
			WHERE state = 'running' AND kind = 'image' RETURNING item_id`, interrupted)
		if err != nil {
			return err
		}
		var items []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			items = append(items, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range items {
			var turn *int64
			if err := tx.QueryRow(ctx, `UPDATE create_app.item SET state = 'failed', error = $2, finished_at = now() WHERE id = $1
				RETURNING turn_id`, id, interrupted).Scan(&turn); err != nil {
				return err
			}
			if turn != nil {
				if err := settle(ctx, tx, *turn, interrupted); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec(ctx, `UPDATE create_app.work SET state = 'waiting', started_at = NULL WHERE state = 'running'`)
		return err
	})
}

// settle puts a turn's state right; a turn that ends failed says why.
func settle(ctx context.Context, tx pgx.Tx, turnID int64, why string) error {
	if _, err := tx.Exec(ctx, `SELECT create_app.settle_turn($1)`, turnID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE create_app.turn SET error = $2 WHERE id = $1 AND state = 'failed' AND error = ''`, turnID, why)
	return err
}

type work struct {
	id        int64
	sessionID int64
	kind      string
	turnID    *int64
	itemID    *int64
	saveID    *int64
}

// RunOne claims the next waiting piece of work and runs it; ran is false
// when there was none. Saves go first, then turns, then pictures.
func (w *Worker) RunOne(ctx context.Context) (ran bool, err error) {
	var j work
	err = w.st.db.QueryRow(ctx, `
		UPDATE create_app.work SET state = 'running', started_at = now()
		WHERE id = (SELECT id FROM create_app.work WHERE state = 'waiting'
		            ORDER BY CASE kind WHEN 'save' THEN 0 WHEN 'turn' THEN 1 ELSE 2 END, id
		            LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, session_id, kind, turn_id, item_id, save_id`).Scan(&j.id, &j.sessionID, &j.kind, &j.turnID, &j.itemID, &j.saveID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	start := time.Now()
	jctx, cancel := context.WithTimeout(ctx, w.WorkTimeout)
	var runErr error
	switch j.kind {
	case "turn":
		runErr = w.turn(jctx, j)
	case "image":
		runErr = w.image(jctx, j)
	case "save":
		runErr = w.save(jctx, j)
	default:
		runErr = fmt.Errorf("unknown work kind %q", j.kind)
	}
	cancel()
	if ctx.Err() != nil {
		// Stopping: it is picked up again at the next start (a picture is
		// failed there).
		return true, nil
	}
	if w.Done != nil {
		w.Done(j.kind, start, runErr)
	}
	return true, w.finish(context.WithoutCancel(ctx), j, runErr)
}

// finish records the end of a piece of work and puts its turn right.
func (w *Worker) finish(ctx context.Context, j work, runErr error) error {
	msg := ""
	state := "done"
	if runErr != nil {
		msg, state = say(runErr), "failed"
		w.log.Warn("create work failed", "work", j.id, "kind", j.kind, "session", j.sessionID, "err", runErr)
	}
	return pgx.BeginFunc(ctx, w.st.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE create_app.work SET state = $2, error = $3, finished_at = now() WHERE id = $1`, j.id, state, msg); err != nil {
			return err
		}
		turn := j.turnID
		if runErr != nil {
			switch {
			case j.itemID != nil:
				if err := tx.QueryRow(ctx, `UPDATE create_app.item SET state = 'failed', error = $2, finished_at = now() WHERE id = $1
					RETURNING turn_id`, *j.itemID, msg).Scan(&turn); err != nil {
					return err
				}
			case j.saveID != nil:
				if _, err := tx.Exec(ctx, `UPDATE create_app.session_save SET state = 'failed', error = $2, finished_at = now() WHERE id = $1`,
					*j.saveID, msg); err != nil {
					return err
				}
			}
		}
		if turn == nil && j.itemID != nil {
			if err := tx.QueryRow(ctx, `SELECT turn_id FROM create_app.item WHERE id = $1`, *j.itemID).Scan(&turn); err != nil {
				return err
			}
		}
		if turn != nil {
			return settle(ctx, tx, *turn, msg)
		}
		return nil
	})
}

// say is an error as one line for the person.
func say(err error) string {
	var oe *openai.Error
	var bad BadInput
	var le *library.Error
	switch {
	case errors.As(err, &oe):
		return oe.Message
	case errors.As(err, &bad):
		return string(bad)
	case errors.As(err, &le):
		return "a biblioteca recusou: " + le.Message
	case errors.Is(err, context.DeadlineExceeded):
		return "demorou demais"
	}
	s := err.Error()
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func (w *Worker) spent(ctx context.Context, sessionID int64, usd float64) {
	if usd <= 0 {
		return
	}
	if _, err := w.st.db.Exec(ctx, `UPDATE create_app.session SET cost_usd = cost_usd + $2 WHERE id = $1`, sessionID, usd); err != nil {
		w.log.Error("create cost", "session", sessionID, "err", err)
	}
}

func (w *Worker) aiOn() error {
	if why := w.ai.Why(); why != "" {
		return BadInput(why)
	}
	return nil
}

// ---- a turn ----------------------------------------------------------------

// picked splits a turn's picked items into its pictures (with their bytes)
// and its headlines.
func (w *Worker) picked(ctx context.Context, t Turn) ([]Item, []openai.Reference, []string, error) {
	items, err := w.st.items(ctx, `i.id = ANY ($1)`, t.Picked)
	if err != nil {
		return nil, nil, nil, err
	}
	byID := map[int64]Item{}
	for _, it := range items {
		byID[it.ID] = it
	}
	var pics []Item
	var refs []openai.Reference
	var headlines []string
	// In the order the person picked them, so a prompt can say "the first".
	for _, id := range t.Picked {
		it, ok := byID[id]
		if !ok {
			continue
		}
		if it.Kind == "headline" {
			headlines = append(headlines, it.Text)
			continue
		}
		b, err := w.st.itemBytes(ctx, it)
		if err != nil {
			return nil, nil, nil, err
		}
		pics = append(pics, it)
		refs = append(refs, openai.Reference{Data: b, MIME: it.MediaType})
	}
	return pics, refs, headlines, nil
}

// turn writes the turn's headlines and the briefs of its pictures, then
// queues one piece of work per picture.
func (w *Worker) turn(ctx context.Context, j work) error {
	if err := w.aiOn(); err != nil {
		return err
	}
	t, err := w.st.Turn(ctx, *j.turnID)
	if err != nil {
		return err
	}
	sess, err := w.st.Session(ctx, t.SessionID)
	if err != nil {
		return err
	}
	pics, refs, pickedHeadlines, err := w.picked(ctx, t)
	if err != nil {
		return err
	}

	// Pictures picked: each new one is that picture changed as the prompt
	// says. None: one picture is the prompt itself; several get a brief each
	// from the text call, so they differ.
	needBriefs := len(pics) == 0 && t.Images > 1
	var other Headliner
	if t.HeadlineModel != "" && t.Headlines > 0 {
		h, ok := w.Headliners[t.HeadlineModel]
		if !ok {
			return BadInput("o modelo " + t.HeadlineModel + " não está ligado no servidor")
		}
		other = h
	}
	var plan openai.Plan
	if t.Headlines > 0 || needBriefs {
		req := openai.PlanRequest{
			Prompt: t.Prompt, Vertical: sess.VerticalName, Language: "en",
			Headlines: t.Headlines, HeadlineExamples: pickedHeadlines,
		}
		if t.Headlines > 0 {
			// The headline memory: the session's own and the library's,
			// newest first, beside every team example (openai trims).
			req.LongMemory = true
			if req.Avoid, err = w.sessionHeadlines(ctx, t.SessionID); err != nil {
				return err
			}
			if req.Saved, err = w.lib.Headlines(ctx, sess.VerticalID, memoryLines); err != nil {
				w.log.Warn("library headlines not read for the memory", "session", t.SessionID, "err", err)
				req.Saved = nil
			}
		}
		if needBriefs {
			req.Images = t.Images
		}
		if len(refs) > 0 && t.Headlines > 0 {
			req.Winners, req.ForPictures = refs, true
		}
		if other != nil {
			// Headlines from the other model, briefs (if any) from OpenAI.
			hreq := req
			hreq.Images = 0
			hplan, herr := other.Plan(ctx, hreq)
			w.spent(ctx, t.SessionID, hplan.Cost)
			if herr != nil && t.Images == 0 {
				return herr
			}
			if herr != nil {
				// The pictures go on: a headline model never fails them.
				w.log.Warn("other headline model failed", "turn", t.ID, "model", t.HeadlineModel, "err", herr)
				if _, err := w.st.db.Exec(ctx, `UPDATE create_app.turn SET error = $2 WHERE id = $1`, t.ID, "headlines: "+say(herr)); err != nil {
					return err
				}
			}
			plan.Headlines = hplan.Headlines
			if needBriefs {
				breq := req
				breq.Headlines, breq.LongMemory, breq.Avoid, breq.Saved, breq.Winners, breq.ForPictures = 0, false, nil, nil, nil, false
				bplan, err := w.ai.Plan(ctx, breq)
				if err != nil {
					return err
				}
				w.spent(ctx, t.SessionID, bplan.Cost)
				plan.Briefs = bplan.Briefs
			}
		} else {
			if plan, err = w.ai.Plan(ctx, req); err != nil {
				return err
			}
			w.spent(ctx, t.SessionID, plan.Cost)
		}
		if t.Headlines > 0 && len(plan.Headlines) == 0 && t.Images == 0 {
			return BadInput("o modelo não devolveu headlines novas; tente outro pedido")
		}
	}
	var briefs []openai.Brief
	for k := 0; k < t.Images; k++ {
		switch {
		case len(pics) > 0:
			briefs = append(briefs, openai.Brief{Brief: variationBrief(t.Prompt, pickedHeadlines, k, t.Images)})
		case needBriefs && k < len(plan.Briefs):
			b := plan.Briefs[k]
			b.Brief = withHeadline(b.Brief, pickedHeadlines)
			briefs = append(briefs, b)
		default:
			briefs = append(briefs, openai.Brief{Brief: withHeadline(t.Prompt, pickedHeadlines)})
		}
	}
	fromIDs := t.Picked
	err = pgx.BeginFunc(ctx, w.st.db, func(tx pgx.Tx) error {
		// Interrupted while the text call ran: its headlines (paid for) are
		// kept, no picture is queued.
		var interrupted bool
		if err := tx.QueryRow(ctx, `SELECT interrupted_at IS NOT NULL FROM create_app.turn WHERE id = $1 FOR UPDATE`, t.ID).Scan(&interrupted); err != nil {
			return err
		}
		if interrupted {
			briefs = nil
		}
		for _, h := range plan.Headlines {
			if _, err := tx.Exec(ctx, `
				INSERT INTO create_app.item (session_id, turn_id, kind, origin, from_ids, text, state, finished_at)
				VALUES ($1, $2, 'headline', 'made', $3, $4, 'done', now())`, t.SessionID, t.ID, fromIDs, h); err != nil {
				return err
			}
		}
		for _, b := range briefs {
			var id int64
			if err := tx.QueryRow(ctx, `
				INSERT INTO create_app.item (session_id, turn_id, kind, origin, from_ids, brief, angle, state)
				VALUES ($1, $2, 'image', 'made', $3, $4, $5, 'waiting') RETURNING id`,
				t.SessionID, t.ID, fromIDs, b.Brief, b.Angle).Scan(&id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO create_app.work (session_id, kind, turn_id, item_id) VALUES ($1, 'image', $2, $3)`,
				t.SessionID, t.ID, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	w.Kick()
	return nil
}

// sessionHeadlines are the headlines the session already has, newest
// first, so new ones bring new ideas.
func (w *Worker) sessionHeadlines(ctx context.Context, sessionID int64) ([]string, error) {
	rows, err := w.st.db.Query(ctx, `SELECT text FROM create_app.item WHERE session_id = $1 AND kind = 'headline' ORDER BY id DESC LIMIT $2`,
		sessionID, memoryLines)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// variationBrief is what a picture made from picked pictures is told: the
// person's prompt as a change to them, or a close variation when they wrote
// nothing; several from one turn are told to differ from each other.
func variationBrief(prompt string, headlines []string, k, n int) string {
	b := strings.TrimSpace(prompt)
	if b == "" {
		b = "Make a new version of this picture: the same kind of person and the same product, in a slightly different moment, pose or framing, as real and candid as the original."
	}
	if n > 1 {
		b += fmt.Sprintf(" This is version %d of %d made from the same request: make it clearly different from the other versions (pose, framing, setting or moment) while doing what is asked.", k+1, n)
	}
	return withHeadline(b, headlines)
}

// withHeadline adds the picked headlines as the mood of the picture; they
// are never written in it.
func withHeadline(brief string, headlines []string) string {
	if len(headlines) == 0 {
		return brief
	}
	return brief + "\n\nThe ad's headline, for the mood only (never write it, or any text, in the picture): " + strings.Join(headlines, " / ")
}

// ---- a picture -------------------------------------------------------------

func (w *Worker) image(ctx context.Context, j work) error {
	if err := w.aiOn(); err != nil {
		return err
	}
	if j.itemID == nil {
		return errors.New("image work without an item")
	}
	it, err := w.st.Item(ctx, *j.itemID)
	if err != nil {
		return err
	}
	if _, err := w.st.db.Exec(ctx, `UPDATE create_app.item SET state = 'making' WHERE id = $1`, it.ID); err != nil {
		return err
	}
	var refs []openai.Reference
	if len(it.FromIDs) > 0 {
		if _, refs, _, err = w.picked(ctx, Turn{Picked: it.FromIDs}); err != nil {
			return err
		}
	}
	size := openai.Sizes[0]
	if it.TurnID != nil {
		t, err := w.st.Turn(ctx, *it.TurnID)
		if err != nil {
			return err
		}
		if s, ok := openai.SizeByID(t.Size); ok {
			size = s
		}
	}
	img, err := w.ai.Image(ctx, openai.ImageRequest{Brief: it.Brief, References: refs, Size: size})
	if err != nil {
		return err
	}
	w.spent(ctx, it.SessionID, img.Cost)
	key, err := files.PutBytes(ctx, w.st.files, img.Data, img.MIME)
	if err != nil {
		return err
	}
	_, err = w.st.db.Exec(ctx, `
		UPDATE create_app.item SET state = 'done', file_key = $2, media_type = $3, width = $4, height = $5, sha256 = $6,
		       cost_usd = $7, finished_at = now() WHERE id = $1`,
		it.ID, key, img.MIME, img.Width, img.Height, sha256hex(img.Data), img.Cost)
	return err
}

// ---- a save ----------------------------------------------------------------

func (w *Worker) save(ctx context.Context, j work) error {
	if j.saveID == nil {
		return errors.New("save work without a save")
	}
	v, err := w.st.SaveByID(ctx, *j.saveID)
	if err != nil {
		return err
	}
	sess, err := w.st.Session(ctx, v.SessionID)
	if err != nil {
		return err
	}
	if _, err := w.st.db.Exec(ctx, `UPDATE create_app.session_save SET state = 'saving' WHERE id = $1`, v.ID); err != nil {
		return err
	}
	madeBy := v.MadeBy
	if madeBy == "" {
		madeBy = sess.MadeBy
	}
	var setID int64
	if sess.LibrarySetID != nil {
		setID = *sess.LibrarySetID
	} else {
		set, err := w.lib.AddSet(ctx, library.NewSet{Name: sess.Name, VerticalID: sess.VerticalID, VerticalName: sess.VerticalName,
			Origin: "create", OriginRef: fmt.Sprintf("create:session:%d", sess.ID), MadeBy: madeBy, Platform: sess.Platform})
		if err != nil {
			return err
		}
		setID = set.ID
		// Kept at once, so a retry or the next save adds to this set.
		if _, err := w.st.db.Exec(ctx, `UPDATE create_app.session SET library_set_id = $2 WHERE id = $1`, sess.ID, setID); err != nil {
			return err
		}
	}
	items, err := w.st.items(ctx, `i.id = ANY ($1)`, v.ItemIDs)
	if err != nil {
		return err
	}
	var headlines []library.NewHeadline
	for _, it := range items {
		ref := fmt.Sprintf("create:session:%d:item:%d", sess.ID, it.ID)
		if it.Kind == "headline" {
			headlines = append(headlines, library.NewHeadline{Text: it.Text, VerticalID: sess.VerticalID, SetID: setID,
				Angle: it.Angle, Origin: "create", OriginRef: ref, AILabel: aiLabel(it, v.AILabel), MadeBy: madeBy})
			continue
		}
		data, err := w.st.itemBytes(ctx, it)
		if err != nil {
			return err
		}
		ext := ".jpg"
		if it.MediaType == "image/png" {
			ext = ".png"
		}
		c, err := w.lib.AddCreative(ctx, library.CreativeMeta{VerticalID: sess.VerticalID, VerticalName: sess.VerticalName,
			Name: fmt.Sprintf("create-%d", it.ID), SetID: setID, Angle: it.Angle, Idea: it.Brief, Origin: "create",
			OriginRef: ref, AILabel: aiLabel(it, v.AILabel), MadeBy: madeBy, Platform: sess.Platform}, fmt.Sprintf("item-%d%s", it.ID, ext), data)
		if err != nil {
			return err
		}
		if _, err := w.st.db.Exec(ctx, `UPDATE create_app.item SET library_id = $2 WHERE id = $1`, it.ID, c.ID); err != nil {
			return err
		}
	}
	if len(headlines) > 0 {
		if err := w.lib.AddHeadlines(ctx, headlines); err != nil {
			return err
		}
	}
	_, err = w.st.db.Exec(ctx, `UPDATE create_app.session_save SET state = 'done', error = '', finished_at = now() WHERE id = $1`, v.ID)
	return err
}

// aiLabel is the person's answer for made items; what came from a computer,
// the library or the keyboard was not made by Create, so it is left unset.
func aiLabel(it Item, label string) string {
	if it.Origin == "made" {
		return label
	}
	return "unset"
}
