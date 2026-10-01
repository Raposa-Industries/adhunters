-- decision: decisions/0021-retire-create-brief-api.md
-- The contract step 0019 left open: the brief views and functions go. Since
-- Create became a chat nothing reads create_app.job, so a call to
-- new_brief_v1 queued work that never ran. The brief tables and their rows
-- stay where they are, unread.
DROP FUNCTION IF EXISTS create_api.new_brief_v1(JSONB, TEXT, TEXT);
DROP FUNCTION IF EXISTS create_api.save_set_v1(BIGINT, BIGINT[], TEXT, TEXT, TEXT, TEXT);
DROP VIEW IF EXISTS create_api.save_v1;
DROP VIEW IF EXISTS create_api.option_v1;
DROP VIEW IF EXISTS create_api.brief_v1;
