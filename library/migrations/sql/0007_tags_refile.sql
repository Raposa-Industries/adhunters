-- lint: new-table
-- Create's library pages (2 Oct 2026): tags on creatives and headlines, and
-- a record of every refile (Mover: a creative or headline put in another
-- set). Every change only adds.

-- A tag a person put on a creative or headline ("cozinha"), in lower case.
-- Several per item; the same tag once per item.
CREATE TABLE library.creative_tag (
    creative_id BIGINT NOT NULL REFERENCES library.creative (id),
    tag TEXT NOT NULL CHECK (tag = lower(tag) AND btrim(tag) <> '' AND char_length(tag) <= 40),
    added_by TEXT NOT NULL DEFAULT '',
    added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (creative_id, tag)
);
CREATE INDEX creative_tag_tag ON library.creative_tag (tag);

CREATE TABLE library.headline_tag (
    headline_id BIGINT NOT NULL REFERENCES library.headline (id),
    tag TEXT NOT NULL CHECK (tag = lower(tag) AND btrim(tag) <> '' AND char_length(tag) <= 40),
    added_by TEXT NOT NULL DEFAULT '',
    added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (headline_id, tag)
);
CREATE INDEX headline_tag_tag ON library.headline_tag (tag);

-- One refile: the sets a creative or headline was in before (from_sets) and
-- the one it is in after. The set rows it left are deleted, so this is
-- where they are kept. Drive files stay where they are.
CREATE TABLE library.refile (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('creative', 'headline')),
    item_id BIGINT NOT NULL,
    from_sets BIGINT[] NOT NULL DEFAULT '{}',
    from_positions INTEGER[] NOT NULL DEFAULT '{}',
    to_set BIGINT NOT NULL REFERENCES library.set (id),
    refiled_by TEXT NOT NULL DEFAULT '',
    refiled_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX refile_item ON library.refile (kind, item_id);
