package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/spy/verticals"
)

const (
	// perClass caps the sure creatives read per vertical for training.
	perClass = 600
	// retrainEvery is how old the newest model may get.
	retrainEvery = 24 * time.Hour
	// retryAfter waits this long after training failed or had too little data.
	retryAfter = time.Hour
	// keepModels is how many models spy.class_model keeps.
	keepModels = 7
	// minExamples is the least number of sure creatives worth training on.
	minExamples = 50
	// minAnswer is the least probability the model's best guess needs to be
	// stored. On the collector's 2026-09-25 audit (about 2,000 creatives read
	// by hand) its guesses below 0.8 were right less than half the time. Below
	// it the rules' answer stays, or none: no vertical is better than a wrong
	// one.
	minAnswer = 0.8
	// textBatch is how many creatives' text one query reads.
	textBatch = 2000
)

// Config tunes a Classifier. Zero values take the defaults.
type Config struct {
	PerRun int // creatives the rules read, and the model answers, per run (5,000)
}

// Classifier runs both passes.
type Classifier struct {
	db    *pgxpool.Pool
	log   *slog.Logger
	cfg   Config
	rules *Rules
	hash  string

	mu      sync.Mutex
	model   *Model
	modelID int32
	failed  time.Time // last failed or skipped training
}

// New makes a Classifier over the embedded vertical list.
func New(db *pgxpool.Pool, log *slog.Logger, cfg Config) (*Classifier, error) {
	l, err := verticals.Load()
	if err != nil {
		return nil, err
	}
	if cfg.PerRun <= 0 {
		cfg.PerRun = 5000
	}
	return &Classifier{db: db, log: log, cfg: cfg, rules: NewRules(l), hash: verticals.Hash()}, nil
}

// Result counts one run.
type Result struct {
	Read     int  // creatives the rules read
	Trained  bool // a new model was trained
	Answered int  // creatives the model answered
	Declined int  // unsure creatives the model left to the rules
}

// Run reads new and changed creatives with the rules, trains a model when
// one is due, and asks it about the creatives the rules are unsure of.
func (c *Classifier) Run(ctx context.Context) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var res Result
	n, err := c.rulesPass(ctx)
	res.Read = n
	if err != nil {
		return res, fmt.Errorf("rules: %w", err)
	}
	res.Trained, err = c.ensureModel(ctx)
	if err != nil {
		return res, err
	}
	if c.model == nil {
		return res, nil
	}
	res.Answered, res.Declined, err = c.modelPass(ctx)
	if err != nil {
		return res, fmt.Errorf("model: %w", err)
	}
	return res, nil
}

// pages says whether this login may read Raposa's evidence (landing page
// titles). Without it the rules read headlines and brands only.
func (c *Classifier) pages(ctx context.Context) bool {
	var ok bool
	err := c.db.QueryRow(ctx, `
		SELECT to_regclass('raposa_api.evidence_v1') IS NOT NULL
		   AND has_table_privilege('raposa_api.evidence_v1', 'SELECT')`).Scan(&ok)
	return err == nil && ok
}

type textRow struct {
	id         int32
	text       Text
	adID       int32
	evidenceID int64
}

// texts reads what the classifier reads about each creative: the headlines
// and descriptions of its 10 newest ads, their brands, and the titles of the
// landing pages Raposa reached from it.
func (c *Classifier) texts(ctx context.Context, ids []int32, pages bool) ([]textRow, error) {
	page := `SELECT ''::text AS txt, 0::bigint AS max_id`
	if pages {
		page = `SELECT string_agg(DISTINCT e.title, ' . ') AS txt, max(e.id) AS max_id
		        FROM raposa_api.evidence_v1 e WHERE e.creative_id = t.id AND COALESCE(e.title, '') <> ''`
	}
	var out []textRow
	for start := 0; start < len(ids); start += textBatch {
		batch := ids[start:min(start+textBatch, len(ids))]
		rows, err := c.db.Query(ctx, `
			SELECT t.id, COALESCE(ads.txt, ''), COALESCE(ads.brands, ''), COALESCE(ads.max_id, 0),
			       COALESCE(pg.txt, ''), COALESCE(pg.max_id, 0)
			FROM unnest($1::int[]) t(id)
			LEFT JOIN LATERAL (
			    SELECT string_agg(x.headline || COALESCE(' . ' || NULLIF(x.description, ''), ''), ' . ' ORDER BY x.id DESC) AS txt,
			           string_agg(DISTINCT b.name, ' . ') AS brands,
			           max(x.id) AS max_id
			    FROM (SELECT a.id, a.headline, a.description, a.brand_id FROM tracks_api.ad_v1 a
			          WHERE a.creative_id = t.id ORDER BY a.id DESC LIMIT 10) x
			    LEFT JOIN tracks_api.brand_v1 b ON b.id = x.brand_id
			) ads ON TRUE
			LEFT JOIN LATERAL (`+page+`) pg ON TRUE`, batch)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r textRow
			if err := rows.Scan(&r.id, &r.text.Ad, &r.text.Brand, &r.adID, &r.text.Page, &r.evidenceID); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func ids(ctx context.Context, db *pgxpool.Pool, query string, args ...any) ([]int32, error) {
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int32])
}

// rulesPass reads the creatives never read, with a newer ad or landing page,
// or read under another version of the list; the most recently seen first.
func (c *Classifier) rulesPass(ctx context.Context) (int, error) {
	pages := c.pages(ctx)
	newPage := ""
	if pages {
		newPage = `OR EXISTS (SELECT 1 FROM raposa_api.evidence_v1 e WHERE e.creative_id = c.id AND e.id > k.input_evidence_id)`
	}
	todo, err := ids(ctx, c.db, `
		SELECT c.id FROM tracks_api.creative_v1 c
		LEFT JOIN spy.creative_class k ON k.creative_id = c.id
		WHERE k.creative_id IS NULL OR k.rules_hash <> $1
		   OR EXISTS (SELECT 1 FROM tracks_api.ad_v1 a WHERE a.creative_id = c.id AND a.id > k.input_ad_id)
		   `+newPage+`
		ORDER BY c.last_seen_at DESC, c.id
		LIMIT $2`, c.hash, c.cfg.PerRun)
	if err != nil || len(todo) == 0 {
		return 0, err
	}
	texts, err := c.texts(ctx, todo, pages)
	if err != nil {
		return 0, err
	}
	n := len(texts)
	var (
		cid, adID            = make([]int32, n), make([]int32, n)
		evID                 = make([]int64, n)
		cat, vert, src, evid = make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		conf                 = make([]float64, n)
	)
	for i, t := range texts {
		a := c.rules.Classify(t.text)
		ev, _ := json.Marshal(map[string]any{"points": a.Points, "hits": a.Hits})
		cid[i], adID[i], evID[i] = t.id, t.adID, t.evidenceID
		cat[i], vert[i], src[i], evid[i], conf[i] = a.Category, a.Vertical, a.Source, string(ev), a.Confidence
	}
	// A model answer stays while the rules are still unsure; the model is
	// asked again, since the text changed.
	_, err = c.db.Exec(ctx, `
		INSERT INTO spy.creative_class AS k (creative_id, category_id, vertical_id, confidence, source, evidence,
		    rules_category_id, rules_vertical_id, rules_confidence, rules_source, rules_hash, input_ad_id,
		    input_evidence_id, classified_at, needs_model, model_id, model_at)
		SELECT u.id, NULLIF(u.cat, ''), NULLIF(u.vert, ''), u.conf, NULLIF(u.src, ''), u.ev::jsonb,
		       NULLIF(u.cat, ''), NULLIF(u.vert, ''), u.conf, NULLIF(u.src, ''), $1, u.ad, u.evid,
		       now(), u.conf < 0.6, NULL, NULL
		FROM unnest($2::int[], $3::text[], $4::text[], $5::numeric[], $6::text[], $7::text[], $8::int[], $9::bigint[])
		     AS u(id, cat, vert, conf, src, ev, ad, evid)
		ON CONFLICT (creative_id) DO UPDATE SET
		    category_id = CASE WHEN k.source = 'model' AND EXCLUDED.needs_model THEN k.category_id ELSE EXCLUDED.category_id END,
		    vertical_id = CASE WHEN k.source = 'model' AND EXCLUDED.needs_model THEN k.vertical_id ELSE EXCLUDED.vertical_id END,
		    confidence = CASE WHEN k.source = 'model' AND EXCLUDED.needs_model THEN k.confidence ELSE EXCLUDED.confidence END,
		    source = CASE WHEN k.source = 'model' AND EXCLUDED.needs_model THEN k.source ELSE EXCLUDED.source END,
		    evidence = EXCLUDED.evidence,
		    rules_category_id = EXCLUDED.rules_category_id,
		    rules_vertical_id = EXCLUDED.rules_vertical_id,
		    rules_confidence = EXCLUDED.rules_confidence,
		    rules_source = EXCLUDED.rules_source,
		    rules_hash = EXCLUDED.rules_hash,
		    input_ad_id = EXCLUDED.input_ad_id,
		    input_evidence_id = EXCLUDED.input_evidence_id,
		    classified_at = EXCLUDED.classified_at,
		    needs_model = EXCLUDED.needs_model,
		    model_at = NULL`,
		c.hash, cid, cat, vert, conf, src, evid, adID, evID)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ensureModel trains a new model when there is none, the newest is a day
// old, or the list changed and every creative has been read under it; and
// loads the newest one when another process stored it.
func (c *Classifier) ensureModel(ctx context.Context) (bool, error) {
	var (
		id        int32
		trainedAt time.Time
		hash      string
	)
	err := c.db.QueryRow(ctx, `SELECT id, trained_at, rules_hash FROM spy.class_model ORDER BY id DESC LIMIT 1`).
		Scan(&id, &trainedAt, &hash)
	none := err == pgx.ErrNoRows
	if err != nil && !none {
		return false, fmt.Errorf("read newest model: %w", err)
	}
	due := none || time.Since(trainedAt) >= retrainEvery
	if !due && hash != c.hash {
		var pending bool
		if err := c.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM spy.creative_class WHERE rules_hash <> $1)`, c.hash).
			Scan(&pending); err != nil {
			return false, err
		}
		due = !pending
	}
	if due && time.Since(c.failed) >= retryAfter {
		if err := c.Train(ctx); err != nil {
			c.failed = time.Now()
			c.log.Warn("classifier training skipped", "err", err)
		} else {
			return true, nil
		}
	}
	if none || id == c.modelID {
		return false, nil
	}
	var blob []byte
	if err := c.db.QueryRow(ctx, `SELECT model FROM spy.class_model WHERE id = $1`, id).Scan(&blob); err != nil {
		return false, err
	}
	m, err := Decode(blob)
	if err != nil {
		return false, fmt.Errorf("model %d: %w", id, err)
	}
	c.model, c.modelID = m, id
	return false, nil
}

// Train learns from the creatives the rules are sure about (catch-alls and
// junk left out, at most perClass per vertical, the most seen first), stores
// the model and makes it the current one.
func (c *Classifier) Train(ctx context.Context) error {
	start := time.Now()
	rows, err := c.db.Query(ctx, `
		SELECT creative_id, rules_vertical_id FROM (
		    SELECT k.creative_id, k.rules_vertical_id,
		           row_number() OVER (PARTITION BY k.rules_vertical_id
		                              ORDER BY COALESCE(cs.sightings_7d, 0) DESC, k.creative_id) AS rn
		    FROM spy.creative_class k
		    LEFT JOIN spy.creative_stats cs USING (creative_id)
		    WHERE k.rules_confidence >= 0.6 AND k.rules_vertical_id IS NOT NULL AND k.rules_hash = $1
		      AND NOT COALESCE(cs.is_junk, FALSE)
		) s WHERE rn <= $2`, c.hash, perClass)
	if err != nil {
		return err
	}
	label := map[int32]string{}
	var todo []int32
	for rows.Next() {
		var id int32
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			rows.Close()
			return err
		}
		if vv, ok := c.rules.Vertical(v); !ok || vv.CatchAll {
			continue
		}
		label[id] = v
		todo = append(todo, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(todo) < minExamples {
		return fmt.Errorf("only %d sure creatives, need %d", len(todo), minExamples)
	}
	texts, err := c.texts(ctx, todo, c.pages(ctx))
	if err != nil {
		return err
	}
	examples := make([]Example, len(texts))
	for i, t := range texts {
		examples[i] = Example{CreativeID: t.id, Doc: t.text.Doc(), Label: label[t.id]}
	}
	read := time.Since(start)

	start = time.Now()
	m, ev, err := Train(examples, c.rules.Keywords(), c.rules.CategoryOf(), TrainOptions{})
	if err != nil {
		return err
	}
	blob, err := m.Encode()
	if err != nil {
		return err
	}
	took := time.Since(start)
	evJSON, _ := json.Marshal(ev)
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id int32
	if err := tx.QueryRow(ctx, `
		INSERT INTO spy.class_model (rules_hash, examples, verticals, terms, train_ms, eval, model)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		c.hash, ev.Train, len(m.Labels), len(m.Terms), took.Milliseconds(), evJSON, blob).Scan(&id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM spy.class_model WHERE id NOT IN (SELECT id FROM spy.class_model ORDER BY id DESC LIMIT $1)`,
		keepModels); err != nil {
		return err
	}
	// Every creative the old model answered or looked at is asked again.
	if _, err := tx.Exec(ctx, `
		UPDATE spy.creative_class SET model_at = NULL
		WHERE (needs_model OR source = 'model') AND model_at IS NOT NULL`); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	c.model, c.modelID = m, id
	c.log.Info("classifier trained", "model", id, "examples", ev.Train, "verticals", len(m.Labels), "terms", len(m.Terms),
		"kb", len(blob)/1024, "train_ms", took.Milliseconds(), "read_ms", read.Milliseconds(),
		"accuracy", round3(ev.Accuracy), "masked_accuracy", round3(ev.MaskedAccuracy),
		"category_accuracy", round3(ev.CategoryAccuracy))
	return nil
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

// modelPass asks the model about unsure creatives it has not seen since
// their last read, the most seen first. It answers only when its best guess
// reaches minAnswer and beats the rules' own confidence; otherwise the
// rules' answer stands (or comes back, when the model had answered before).
func (c *Classifier) modelPass(ctx context.Context) (answered, declined int, err error) {
	rows, err := c.db.Query(ctx, `
		SELECT k.creative_id, COALESCE(k.rules_vertical_id, ''), k.rules_confidence::float8
		FROM spy.creative_class k
		LEFT JOIN spy.creative_stats cs USING (creative_id)
		WHERE (k.needs_model OR k.source = 'model') AND (k.model_at IS NULL OR k.model_at < k.classified_at)
		  AND NOT COALESCE(cs.is_junk, FALSE)
		ORDER BY COALESCE(cs.sightings_7d, 0) DESC, k.creative_id
		LIMIT $1`, c.cfg.PerRun)
	if err != nil {
		return 0, 0, err
	}
	type ask struct {
		rulesVertical string
		rulesConf     float64
	}
	asks := map[int32]ask{}
	var todo []int32
	for rows.Next() {
		var id int32
		var a ask
		if err := rows.Scan(&id, &a.rulesVertical, &a.rulesConf); err != nil {
			rows.Close()
			return 0, 0, err
		}
		asks[id] = a
		todo = append(todo, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(todo) == 0 {
		return 0, 0, err
	}
	texts, err := c.texts(ctx, todo, c.pages(ctx))
	if err != nil {
		return 0, 0, err
	}
	var (
		ansID, keepID          []int32
		ansCat, ansVert, ansEv []string
		ansConf                []float64
	)
	for _, t := range texts {
		a := asks[t.id]
		guesses, ok := c.model.Predict(t.text.Doc())
		if !ok || guesses[0].P < minAnswer || (a.rulesVertical != "" && guesses[0].P <= a.rulesConf) {
			keepID = append(keepID, t.id)
			continue
		}
		v, _ := c.rules.Vertical(guesses[0].Vertical)
		top := make([]map[string]any, 0, 3)
		for _, g := range guesses[:min(3, len(guesses))] {
			top = append(top, map[string]any{"vertical": g.Vertical, "p": round3(g.P)})
		}
		ev, _ := json.Marshal(map[string]any{"model": c.modelID, "top": top})
		ansID = append(ansID, t.id)
		ansCat = append(ansCat, v.Category)
		ansVert = append(ansVert, v.ID)
		ansConf = append(ansConf, math.Floor(guesses[0].P*100)/100)
		ansEv = append(ansEv, string(ev))
	}
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE spy.creative_class k SET category_id = u.cat, vertical_id = u.vert, confidence = u.conf,
		       source = 'model', model_top = u.ev::jsonb, model_id = $1, model_at = now()
		FROM unnest($2::int[], $3::text[], $4::text[], $5::numeric[], $6::text[]) AS u(id, cat, vert, conf, ev)
		WHERE k.creative_id = u.id`, c.modelID, ansID, ansCat, ansVert, ansConf, ansEv); err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE spy.creative_class SET category_id = rules_category_id, vertical_id = rules_vertical_id,
		       confidence = rules_confidence, source = rules_source, model_id = $1, model_at = now()
		WHERE creative_id = ANY($2::int[])`, c.modelID, keepID); err != nil {
		return 0, 0, err
	}
	return len(ansID), len(keepID), tx.Commit(ctx)
}
