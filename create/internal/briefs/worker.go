package briefs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// Library is what the worker needs of the library (*library.Client).
type Library interface {
	File(ctx context.Context, id string) ([]byte, error)
	AddSet(ctx context.Context, s library.NewSet) (library.Set, error)
	AddCreative(ctx context.Context, meta library.CreativeMeta, filename string, data []byte) (library.Creative, error)
	AddHeadlines(ctx context.Context, hs []library.NewHeadline) error
}

// Worker runs Create's jobs: reading performing ads, planning rounds,
// making pictures, saving into the library. Pictures run Workers at a time;
// each is its own call, so one failing never stops the others (as in
// auto-creative).
type Worker struct {
	st   *Store
	ai   AI
	lib  Library
	log  *slog.Logger
	kick chan struct{}

	// Workers is how many jobs run at once.
	Workers int
	// Poll is how often the queue is looked at without a kick (jobs from
	// create_api arrive that way).
	Poll time.Duration
	// Fetch downloads a Spy ad's picture from its address.
	Fetch func(ctx context.Context, url string) ([]byte, error)
	// JobTimeout bounds one job.
	JobTimeout time.Duration
	// Done, when set, is told of each finished job (the ops task count).
	Done func(kind string, start time.Time, err error)
}

// NewWorker returns a worker. Store.New's kick should be w.Kick.
func NewWorker(st *Store, ai AI, lib Library, log *slog.Logger) *Worker {
	return &Worker{st: st, ai: ai, lib: lib, log: log, kick: make(chan struct{}, 1), Workers: 3, Poll: 2 * time.Second,
		Fetch: fetch, JobTimeout: 10 * time.Minute}
}

// Kick wakes the worker; it never waits.
func (w *Worker) Kick() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// Run works until ctx ends. At its start, jobs left running by a stop are
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
			w.log.Error("create job", "err", err)
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

func (w *Worker) reconcile(ctx context.Context) error {
	return pgx.BeginFunc(ctx, w.st.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE create_app.job SET state = 'failed', error = 'interrompida: o Create reiniciou durante a imagem', finished_at = now()
			WHERE state = 'running' AND kind = 'image' RETURNING brief_id, option_id`)
		if err != nil {
			return err
		}
		type stuck struct{ brief, option int64 }
		var failed []stuck
		for rows.Next() {
			var s stuck
			if err := rows.Scan(&s.brief, &s.option); err != nil {
				rows.Close()
				return err
			}
			failed = append(failed, s)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, s := range failed {
			if _, err := tx.Exec(ctx, `UPDATE create_app.option SET state = 'failed', error = 'interrompida: o Create reiniciou', finished_at = now() WHERE id = $1`, s.option); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO create_app.event (brief_id, line, failed) VALUES ($1, $2, true)`, s.brief,
				fmt.Sprintf("Imagem %d interrompida: o Create reiniciou. Use Refazer se quiser outra.", s.option)); err != nil {
				return err
			}
			if err := settleTx(ctx, tx, s.brief); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE create_app.job SET state = 'waiting', started_at = NULL WHERE state = 'running'`)
		return err
	})
}

type job struct {
	id       int64
	briefID  int64
	kind     string
	optionID *int64
	saveID   *int64
	input    []byte
}

// RunOne claims the next waiting job and runs it; ran is false when there
// was none.
func (w *Worker) RunOne(ctx context.Context) (ran bool, err error) {
	var j job
	err = w.st.db.QueryRow(ctx, `
		UPDATE create_app.job SET state = 'running', started_at = now()
		WHERE id = (SELECT id FROM create_app.job WHERE state = 'waiting'
		            ORDER BY CASE kind WHEN 'save' THEN 0 WHEN 'read' THEN 1 WHEN 'plan' THEN 2 ELSE 3 END, id
		            LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, brief_id, kind, option_id, save_id, input`).Scan(&j.id, &j.briefID, &j.kind, &j.optionID, &j.saveID, &j.input)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	start := time.Now()
	jctx, cancel := context.WithTimeout(ctx, w.JobTimeout)
	var runErr error
	switch j.kind {
	case "read":
		runErr = w.read(jctx, j)
	case "plan":
		runErr = w.plan(jctx, j)
	case "image":
		runErr = w.image(jctx, j)
	case "save":
		runErr = w.save(jctx, j)
	default:
		runErr = fmt.Errorf("unknown job kind %q", j.kind)
	}
	cancel()
	if ctx.Err() != nil {
		// Stopping: the job is picked up again at the next start (a picture
		// is failed there).
		return true, nil
	}
	if w.Done != nil {
		w.Done(j.kind, start, runErr)
	}
	return true, w.finish(context.WithoutCancel(ctx), j, runErr)
}

// finish records a job's end, and on failure its line in the log.
func (w *Worker) finish(ctx context.Context, j job, runErr error) error {
	msg := ""
	if runErr != nil {
		msg = say(runErr)
		w.log.Warn("create job failed", "job", j.id, "kind", j.kind, "brief", j.briefID, "err", runErr)
	}
	return pgx.BeginFunc(ctx, w.st.db, func(tx pgx.Tx) error {
		state := "done"
		if runErr != nil {
			state = "failed"
		}
		if _, err := tx.Exec(ctx, `UPDATE create_app.job SET state = $2, error = $3, finished_at = now() WHERE id = $1`, j.id, state, msg); err != nil {
			return err
		}
		if runErr != nil {
			line := map[string]string{"read": "A leitura dos anúncios falhou: ", "plan": "O plano da rodada falhou: ",
				"image": fmt.Sprintf("A imagem %d falhou: ", deref(j.optionID)), "save": "Salvar na biblioteca falhou: "}[j.kind] + msg
			if _, err := tx.Exec(ctx, `INSERT INTO create_app.event (brief_id, line, failed) VALUES ($1, $2, true)`, j.briefID, line); err != nil {
				return err
			}
			switch {
			case j.optionID != nil:
				if _, err := tx.Exec(ctx, `UPDATE create_app.option SET state = 'failed', error = $2, finished_at = now() WHERE id = $1`, *j.optionID, msg); err != nil {
					return err
				}
			case j.saveID != nil:
				if _, err := tx.Exec(ctx, `UPDATE create_app.save SET state = 'failed', error = $2, finished_at = now() WHERE id = $1`, *j.saveID, msg); err != nil {
					return err
				}
			}
		}
		return settleTx(ctx, tx, j.briefID)
	})
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
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

func (w *Worker) event(ctx context.Context, briefID int64, line string) {
	if _, err := w.st.db.Exec(ctx, `INSERT INTO create_app.event (brief_id, line) VALUES ($1, $2)`, briefID, line); err != nil {
		w.log.Error("create event", "brief", briefID, "err", err)
	}
}

func (w *Worker) spent(ctx context.Context, briefID int64, usd float64) {
	if usd <= 0 {
		return
	}
	if _, err := w.st.db.Exec(ctx, `UPDATE create_app.brief SET cost_usd = cost_usd + $2 WHERE id = $1`, briefID, usd); err != nil {
		w.log.Error("create cost", "brief", briefID, "err", err)
	}
}

func money(usd float64) string {
	return strings.Replace(fmt.Sprintf("US$ %.3f", usd), ".", ",", 1)
}

// ---- references ------------------------------------------------------------

// keptReferences fetches every reference still waiting, then returns the
// kept ones with their bytes, in order.
func (w *Worker) keptReferences(ctx context.Context, briefID int64) ([]Reference, []openai.Reference, error) {
	refs, err := w.st.references(ctx, briefID)
	if err != nil {
		return nil, nil, err
	}
	var kept []Reference
	var pics []openai.Reference
	for _, r := range refs {
		if r.State == "waiting" {
			r, err = w.fetchReference(ctx, r)
			if err != nil {
				return nil, nil, err
			}
		}
		if r.State != "kept" {
			continue
		}
		rc, err := w.st.files.Get(ctx, r.fileKey)
		if err != nil {
			return nil, nil, fmt.Errorf("reference %d: %w", r.ID, err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, nil, err
		}
		kept = append(kept, r)
		pics = append(pics, openai.Reference{Data: b, MIME: r.MediaType})
	}
	return kept, pics, nil
}

// fetchReference downloads one reference's picture and keeps it before
// anything reads it. A picture that cannot be had fails that reference only.
func (w *Worker) fetchReference(ctx context.Context, r Reference) (Reference, error) {
	var b []byte
	var headline string
	var ferr error
	switch r.Kind {
	case RefSpyAd:
		var imageURL string
		imageURL, headline, ferr = w.spyAd(ctx, r.RefID)
		if ferr == nil {
			b, ferr = w.Fetch(ctx, imageURL)
		}
	case RefLibraryCreative:
		b, ferr = w.lib.File(ctx, r.RefID)
		if errors.Is(ferr, library.ErrNotFound) {
			ferr = BadInput("esse criativo não está na biblioteca")
		}
	default:
		ferr = BadInput("referência sem imagem")
	}
	var pic picture
	if ferr == nil {
		pic, ferr = readPicture(b)
	}
	if ferr != nil {
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		msg := say(ferr)
		if _, err := w.st.db.Exec(ctx, `UPDATE create_app.reference SET state = 'failed', error = $2 WHERE id = $1`, r.ID, msg); err != nil {
			return r, err
		}
		w.event(ctx, r.BriefID, fmt.Sprintf("Não consegui a imagem da referência %s: %s", r.RefID, msg))
		r.State, r.Error = "failed", msg
		return r, nil
	}
	key, err := files.PutBytes(ctx, w.st.files, b, pic.mediaType)
	if err != nil {
		return r, err
	}
	if _, err := w.st.db.Exec(ctx, `
		UPDATE create_app.reference SET state = 'kept', file_key = $2, media_type = $3, width = $4, height = $5, sha256 = $6,
		       headline = $7 WHERE id = $1`, r.ID, key, pic.mediaType, pic.width, pic.height, pic.sha256, openai.CleanLine(headline)); err != nil {
		return r, err
	}
	return w.st.reference(ctx, r.ID)
}

// spyAd finds a Spy ad's picture address and headline in Tracks' published
// views (tracks_api), the only part of Tracks Create reads.
func (w *Worker) spyAd(ctx context.Context, id string) (imageURL, headline string, err error) {
	err = w.st.db.QueryRow(ctx, `
		SELECT COALESCE(c.image_url, ''), COALESCE(a.headline, '')
		FROM tracks_api.ad_v1 a JOIN tracks_api.creative_v1 c ON c.id = a.creative_id
		WHERE a.id::text = $1`, id).Scan(&imageURL, &headline)
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", "", BadInput("esse anúncio não está no Spy")
	case errors.As(err, &pe) && (pe.Code == "42P01" || pe.Code == "3F000" || pe.Code == "42501"):
		return "", "", BadInput("o Create não consegue ler o Spy neste servidor")
	case err != nil:
		return "", "", err
	case imageURL == "":
		return "", "", BadInput("esse anúncio não tem imagem no Spy")
	}
	return imageURL, headline, nil
}

var fetchClient = &http.Client{Timeout: time.Minute}

func fetch(ctx context.Context, url string) ([]byte, error) {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return nil, BadInput("endereço de imagem inválido")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := fetchClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("baixar a imagem: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, BadInput(fmt.Sprintf("a imagem não baixou (%d)", res.StatusCode))
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, MaxReference+1))
	if err != nil {
		return nil, err
	}
	return b, nil
}

// ---- jobs ------------------------------------------------------------------

func (w *Worker) aiOn() error {
	if why := w.ai.Why(); why != "" {
		return BadInput(why)
	}
	return nil
}

func (w *Worker) read(ctx context.Context, j job) error {
	if err := w.aiOn(); err != nil {
		return err
	}
	b, err := w.st.Brief(ctx, j.briefID)
	if err != nil {
		return err
	}
	refs, pics, err := w.keptReferences(ctx, j.briefID)
	if err != nil {
		return err
	}
	if len(pics) == 0 {
		return BadInput("nenhuma referência com imagem para ler")
	}
	w.event(ctx, j.briefID, fmt.Sprintf("Lendo %d anúncios…", len(pics)))
	plan, err := w.ai.Plan(ctx, openai.PlanRequest{
		Prompt: b.Extra, Vertical: verticalName(b), Ages: b.Ages, Winners: pics,
		HeadlineExamples: examples(b, refs), Language: "en",
	})
	if err != nil {
		return err
	}
	w.spent(ctx, j.briefID, plan.Cost)
	analysis, err := json.Marshal(plan.Analysis)
	if err != nil {
		return err
	}
	if _, err := w.st.db.Exec(ctx, `UPDATE create_app.brief SET analysis = $2, updated_at = now() WHERE id = $1`, j.briefID, analysis); err != nil {
		return err
	}
	w.event(ctx, j.briefID, fmt.Sprintf("Li %d anúncios: %d aspectos em comum (%s). Revise antes de criar.", len(pics), len(plan.Analysis), money(plan.Cost)))
	return nil
}

// verticalName is what the model reads as the vertical.
func verticalName(b Brief) string {
	if b.VerticalName != "" {
		return b.VerticalName
	}
	return strings.ReplaceAll(b.VerticalID, "-", " ")
}

// examples are the person's reference headlines: typed ones first, then
// those of the Spy ads.
func examples(b Brief, refs []Reference) []string {
	out := append([]string{}, b.OwnHeadlines...)
	for _, r := range refs {
		if r.Headline != "" {
			out = append(out, r.Headline)
		}
	}
	return cleanList(out)
}

type planInput struct {
	Round     int  `json:"round"`
	Images    int  `json:"images"`
	Headlines int  `json:"headlines"`
	NewAngle  bool `json:"new_angle"`
}

func (w *Worker) plan(ctx context.Context, j job) error {
	if err := w.aiOn(); err != nil {
		return err
	}
	var in planInput
	if err := json.Unmarshal(j.input, &in); err != nil {
		return err
	}
	b, err := w.st.Brief(ctx, j.briefID)
	if err != nil {
		return err
	}
	refs, pics, err := w.keptReferences(ctx, j.briefID)
	if err != nil {
		return err
	}
	opts, err := w.st.options(ctx, j.briefID)
	if err != nil {
		return err
	}
	var avoid, avoidAngles []string
	seenAngle := map[string]bool{}
	for _, o := range opts {
		switch {
		case o.Kind == "headline":
			avoid = append(avoid, o.Text)
		case o.ParentID == nil && o.Idea != "":
			avoid = append(avoid, o.Idea)
		}
		if o.Kind == "image" && o.Angle != "" && !seenAngle[o.Angle] {
			seenAngle[o.Angle] = true
			avoidAngles = append(avoidAngles, o.Angle)
		}
	}
	what := fmt.Sprintf("Planejando a rodada %d: %d imagens e %d títulos", in.Round, in.Images, in.Headlines)
	if in.NewAngle {
		what += ", num ângulo novo"
	}
	w.event(ctx, j.briefID, what+"…")
	plan, err := w.ai.Plan(ctx, openai.PlanRequest{
		Prompt: b.Extra, Vertical: verticalName(b), Ages: b.Ages, Language: "en",
		Headlines: in.Headlines, Images: in.Images, Winners: pics, HeadlineExamples: examples(b, refs),
		Avoid: avoid, Analysis: b.Analysis, Angles: b.Angles, NewAngle: in.NewAngle, AvoidAngles: avoidAngles,
	})
	if err != nil {
		return err
	}
	w.spent(ctx, j.briefID, plan.Cost)
	err = pgx.BeginFunc(ctx, w.st.db, func(tx pgx.Tx) error {
		if len(b.Analysis) == 0 && len(plan.Analysis) > 0 {
			a, err := json.Marshal(plan.Analysis)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE create_app.brief SET analysis = $2 WHERE id = $1`, j.briefID, a); err != nil {
				return err
			}
		}
		for _, h := range plan.Headlines {
			if _, err := tx.Exec(ctx, `
				INSERT INTO create_app.option (brief_id, round, kind, text, state, finished_at) VALUES ($1, $2, 'headline', $3, 'done', now())`,
				j.briefID, in.Round, h); err != nil {
				return err
			}
		}
		for _, br := range plan.Briefs {
			var id int64
			if err := tx.QueryRow(ctx, `
				INSERT INTO create_app.option (brief_id, round, kind, angle, idea, state) VALUES ($1, $2, 'image', $3, $4, 'waiting')
				RETURNING id`, j.briefID, in.Round, br.Angle, br.Brief).Scan(&id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO create_app.job (brief_id, kind, option_id) VALUES ($1, 'image', $2)`, j.briefID, id); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO create_app.event (brief_id, line) VALUES ($1, $2)`, j.briefID,
			fmt.Sprintf("Plano da rodada %d pronto: %d títulos e %d imagens para fazer (%s).", in.Round, len(plan.Headlines), len(plan.Briefs), money(plan.Cost)))
		return err
	})
	if err != nil {
		return err
	}
	w.Kick()
	return nil
}

// againBrief is what a picture made again is told: the note, as a change to
// the attached picture.
func againBrief(note string) string {
	return "Change this picture as the person asks, and keep everything else as it is: " + note
}

func (w *Worker) image(ctx context.Context, j job) error {
	if err := w.aiOn(); err != nil {
		return err
	}
	if j.optionID == nil {
		return errors.New("image job without an option")
	}
	o, err := w.st.Option(ctx, *j.optionID)
	if err != nil {
		return err
	}
	if _, err := w.st.db.Exec(ctx, `UPDATE create_app.option SET state = 'making' WHERE id = $1`, o.ID); err != nil {
		return err
	}
	req := openai.ImageRequest{Brief: o.Idea}
	if o.ParentID != nil {
		parent, err := w.st.Option(ctx, *o.ParentID)
		if err != nil {
			return err
		}
		rc, err := w.st.files.Get(ctx, parent.fileKey)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return err
		}
		req = openai.ImageRequest{Brief: againBrief(o.Note), References: []openai.Reference{{Data: data, MIME: parent.MediaType}}}
	}
	img, err := w.ai.Image(ctx, req)
	if err != nil {
		return err
	}
	w.spent(ctx, j.briefID, img.Cost)
	key, err := files.PutBytes(ctx, w.st.files, img.Data, img.MIME)
	if err != nil {
		return err
	}
	if _, err := w.st.db.Exec(ctx, `
		UPDATE create_app.option SET state = 'done', file_key = $2, media_type = $3, width = $4, height = $5, sha256 = $6,
		       cost_usd = $7, finished_at = now() WHERE id = $1`,
		o.ID, key, img.MIME, img.Width, img.Height, sha256hex(img.Data), img.Cost); err != nil {
		return err
	}
	w.event(ctx, j.briefID, fmt.Sprintf("Imagem %d pronta (%s, %s).", o.ID, o.Angle, money(img.Cost)))
	return nil
}

func (w *Worker) save(ctx context.Context, j job) error {
	if j.saveID == nil {
		return errors.New("save job without a save")
	}
	v, err := w.st.SaveByID(ctx, *j.saveID)
	if err != nil {
		return err
	}
	b, err := w.st.Brief(ctx, j.briefID)
	if err != nil {
		return err
	}
	if _, err := w.st.db.Exec(ctx, `UPDATE create_app.save SET state = 'saving' WHERE id = $1`, v.ID); err != nil {
		return err
	}
	madeBy := v.RequestedBy
	if madeBy == "" {
		madeBy = b.RequestedBy
	}
	setID := deref(v.LibrarySetID)
	if setID == 0 {
		set, err := w.lib.AddSet(ctx, library.NewSet{Name: v.Name, VerticalID: b.VerticalID, VerticalName: b.VerticalName,
			Origin: "create", OriginRef: fmt.Sprintf("create:brief:%d", b.ID), MadeBy: madeBy})
		if err != nil {
			return err
		}
		setID = set.ID
		// Kept at once, so a retry adds to this set instead of making another.
		if _, err := w.st.db.Exec(ctx, `UPDATE create_app.save SET library_set_id = $2 WHERE id = $1`, v.ID, setID); err != nil {
			return err
		}
	}
	var headlines []library.NewHeadline
	images := 0
	for _, id := range v.OptionIDs {
		o, err := w.st.Option(ctx, id)
		if err != nil {
			return err
		}
		ref := fmt.Sprintf("create:brief:%d:option:%d", b.ID, o.ID)
		if o.Kind == "headline" {
			headlines = append(headlines, library.NewHeadline{Text: o.Text, VerticalID: b.VerticalID, SetID: setID,
				Origin: "create", OriginRef: ref, AILabel: v.AILabel, MadeBy: madeBy})
			continue
		}
		rc, err := w.st.files.Get(ctx, o.fileKey)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return err
		}
		ext := ".jpg"
		if o.MediaType == "image/png" {
			ext = ".png"
		}
		if _, err := w.lib.AddCreative(ctx, library.CreativeMeta{VerticalID: b.VerticalID, VerticalName: b.VerticalName,
			Name: fmt.Sprintf("create-%d", o.ID), SetID: setID, Angle: o.Angle, Idea: o.Idea, Origin: "create",
			OriginRef: ref, AILabel: v.AILabel, MadeBy: madeBy}, fmt.Sprintf("option-%d%s", o.ID, ext), data); err != nil {
			return err
		}
		images++
	}
	if len(headlines) > 0 {
		if err := w.lib.AddHeadlines(ctx, headlines); err != nil {
			return err
		}
	}
	if _, err := w.st.db.Exec(ctx, `UPDATE create_app.save SET state = 'done', error = '', finished_at = now() WHERE id = $1`, v.ID); err != nil {
		return err
	}
	w.event(ctx, j.briefID, fmt.Sprintf("Salvo na biblioteca como “%s”: %d imagens e %d títulos.", v.Name, images, len(headlines)))
	return nil
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
