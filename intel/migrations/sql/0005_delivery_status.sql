-- lint: new-table
-- Delivery status: each status a campaign had (Taboola's campaign status,
-- the "Delivery Status" column in Realize), and each change between two of
-- them, sent once to "AdHunters alerts". DELETED is ours: the campaign left
-- Taboola's list.

INSERT INTO intel.setting (name, value, note) VALUES
    ('status_alert_max_age_hours', 6, 'Delivery status changes older than this are recorded but not sent (a reload or a first run).');

CREATE TABLE intel.tb_campaign_status (
    campaign_id BIGINT NOT NULL,
    valid_from TIMESTAMPTZ NOT NULL,
    account TEXT NOT NULL,
    status TEXT NOT NULL,
    PRIMARY KEY (campaign_id, valid_from)
);

CREATE TABLE intel.status_change (
    id BIGSERIAL PRIMARY KEY,
    campaign_id BIGINT NOT NULL,
    account TEXT NOT NULL,
    -- Empty for a campaign first seen after its account was already read:
    -- a new campaign, with the status it started in.
    old_status TEXT NOT NULL,
    new_status TEXT NOT NULL,
    changed_at TIMESTAMPTZ NOT NULL,
    found_at TIMESTAMPTZ NOT NULL,
    sent_at TIMESTAMPTZ,
    -- skipped: older than status_alert_max_age_hours when found, so not sent.
    skipped BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (campaign_id, changed_at)
);
CREATE INDEX ON intel.status_change (changed_at);
