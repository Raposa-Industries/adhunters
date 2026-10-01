-- A picture or headline can come from a Spy ad (decisions/0022): origin spy,
-- library_ref spy:creative:<tracks creative id>. The check only widens.
ALTER TABLE create_app.item DROP CONSTRAINT item_origin_check;
ALTER TABLE create_app.item ADD CONSTRAINT item_origin_check
    CHECK (origin IN ('made', 'upload', 'library', 'typed', 'spy'));
