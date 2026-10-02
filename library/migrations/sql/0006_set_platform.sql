-- A set can say which platform (ad network) its creatives are for, when the
-- app that made it knows (Create's session, 2 Oct 2026): taboola or
-- newsbreak. A set with one goes in Drive under <vertical>/<platform>/<set>,
-- and the creatives saved into it are minted with that platform's network
-- letter (T, N). Sets made before have none and keep their folder.
ALTER TABLE library.set ADD COLUMN platform TEXT CHECK (platform IN ('taboola', 'newsbreak'));
