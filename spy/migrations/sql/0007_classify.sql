-- lint: new-table
-- decision: decisions/0014-spy-classifies-into-our-verticals.md
-- Spy classifies creatives itself, into its own fixed list of verticals
-- (spy/verticals/verticals.yaml), with the collector's two passes ported:
-- keyword rules, then a model trained on what the rules are sure about
-- (spy/classify). From here every number cut by vertical (Direction's
-- market, Size, ranges, lifespan, the read model) uses those ids.
--
-- This is the switch step of the decision: the collector's labels, copied
-- by import-old, move to spy.creative_vertical_old and stay there to
-- compare with; spy.creative_vertical becomes a view over Spy's own answer,
-- so every function reading it now reads ours. Nothing reads spy_api in
-- service yet (spy-numbers is not deployed), so no reader sees the change
-- happen. The contract step (dropping the copy) waits until the collector
-- is gone.

ALTER TABLE spy.creative_vertical RENAME TO creative_vertical_old;

-- One row per creative the classifier has read. vertical_id and category_id
-- are ids of verticals.yaml, or NULL when nothing matched.
CREATE TABLE spy.creative_class (
    creative_id INTEGER PRIMARY KEY,
    category_id TEXT,
    vertical_id TEXT,
    confidence NUMERIC(3, 2) NOT NULL DEFAULT 0,
    unsure BOOLEAN GENERATED ALWAYS AS (confidence < 0.6) STORED,
    source TEXT,                         -- ad, brand, page (the rules' part with most points) or model
    evidence JSONB NOT NULL DEFAULT '{}', -- the rules': points per vertical and the words found
    model_top JSONB,                     -- the model's last look: its top 3 verticals and their probabilities
    -- The rules' own answer, kept so a model answer can be taken back.
    rules_category_id TEXT,
    rules_vertical_id TEXT,
    rules_confidence NUMERIC(3, 2) NOT NULL DEFAULT 0,
    rules_source TEXT,
    rules_hash TEXT NOT NULL,            -- verticals.yaml the rules read; a new file reads every creative again
    input_ad_id INTEGER NOT NULL,        -- the newest ad read; a newer one reads the creative again
    input_evidence_id BIGINT NOT NULL DEFAULT 0,  -- the newest Raposa evidence read
    classified_at TIMESTAMPTZ NOT NULL,  -- when the rules last read it
    needs_model BOOLEAN NOT NULL,        -- the rules are unsure: ask the model
    model_id INTEGER,                    -- the model that answered or declined
    model_at TIMESTAMPTZ                 -- when the model last looked; NULL: not yet, or a new model
);
CREATE INDEX creative_class_vertical_idx ON spy.creative_class (vertical_id);
CREATE INDEX creative_class_model_idx ON spy.creative_class (creative_id) WHERE needs_model OR source = 'model';

-- The trained models, newest kept (7). The model is int8 weights, gob, gzip.
CREATE TABLE spy.class_model (
    id INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    trained_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    rules_hash TEXT NOT NULL,
    examples INTEGER NOT NULL,
    verticals INTEGER NOT NULL,
    terms INTEGER NOT NULL,
    train_ms INTEGER NOT NULL,
    eval JSONB NOT NULL,                 -- held-out accuracy, with keywords and without (classify.Eval)
    model BYTEA NOT NULL
);

-- The name every number reads: the vertical of each creative, now Spy's
-- own. Columns as the table it replaces had them: vertical, subvertical and
-- shown_vertical are all the vertical id; the category is in
-- spy.creative_class. A creative with no vertical has no row.
CREATE VIEW spy.creative_vertical AS
SELECT creative_id, vertical_id AS vertical, vertical_id AS subvertical, vertical_id AS shown_vertical,
       confidence, unsure, source, FALSE AS health_from_funnel
FROM spy.creative_class
WHERE vertical_id IS NOT NULL;

-- Published: each creative's category and vertical (ids of verticals.yaml),
-- how sure, and from what.
CREATE VIEW spy_api.creative_class_v1 AS
SELECT creative_id, category_id, vertical_id, confidence, unsure, source, classified_at
FROM spy.creative_class;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'spy_api_read') THEN
        GRANT SELECT ON spy_api.creative_class_v1 TO spy_api_read;
    END IF;
END;
$$;
