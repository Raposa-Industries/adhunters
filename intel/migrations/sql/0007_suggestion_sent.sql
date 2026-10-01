-- Each new suggestion is sent once to "AdHunters alerts" with its Launch
-- link; sent_at says when. The ones already there count as sent, so the
-- first round after this sends only new ones.
ALTER TABLE intel.suggestion ADD COLUMN sent_at TIMESTAMPTZ;
UPDATE intel.suggestion SET sent_at = created_at;

INSERT INTO intel.setting (name, value, note) VALUES
    ('suggestion_alert_max_age_hours', 6, 'Open suggestions older than this are not sent to Telegram (Telegram was off when they opened).');

-- Views added to intel_api later (a v2) are readable by intel_api_read
-- without another grant. Migrations run as the intel login, which creates
-- every intel_api view.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'intel_api_read') THEN
        ALTER DEFAULT PRIVILEGES IN SCHEMA intel_api GRANT SELECT ON TABLES TO intel_api_read;
    END IF;
END;
$$;
