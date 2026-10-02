-- Watches go to the ops group "AdHunters operation" on Telegram, not Pushcut
-- (decision 0004, changed 2 Oct 2026). A watch no longer names a Pushcut
-- notification: new ones leave pushcut_notification empty. The column stays
-- until a contract step with its own decision drops it.
ALTER TABLE raposa.watch ALTER COLUMN pushcut_notification SET DEFAULT '';
COMMENT ON COLUMN raposa.watch.pushcut_notification IS
    'Not used since watches go to the ops group on Telegram (decision 0004). Empty on new watches.';
