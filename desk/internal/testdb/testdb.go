// Package testdb gives a test its own throwaway database with the desk
// migrations applied. It needs PG_TEST_URL (any database on a server the test
// may create databases on); without it the test is skipped.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/contract/actions"
	"github.com/Raposa-Industries/adhunters/desk/migrations"
	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/kit/pg"
)

// New creates a database, migrates it, and drops it when the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := Empty(t)
	migs, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Up(context.Background(), pool, slog.New(slog.NewTextHandler(io.Discard, nil)), migrations.Schema, migs); err != nil {
		t.Fatal(err)
	}
	return pool
}

// Empty creates a database with nothing in it, and drops it when the test
// ends.
func Empty(t testing.TB) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("PG_TEST_URL")
	if base == "" {
		t.Skip("PG_TEST_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "desk_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	q := u.Query()
	q.Set("timezone", "UTC")
	u.RawQuery = q.Encode()
	pool, err := pg.Open(ctx, pg.Config{URL: u.String(), AppName: "desk-test", StatementTimeout: time.Minute, MaxConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	return pool
}

// Demo creates demo_api, a stand-in app for the tests' catalog (Catalog in
// this package): a request function in the style of Launch's, the view to
// follow it, a read of options with images and texts, and a change.
func Demo(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), demoFixture); err != nil {
		t.Fatal(err)
	}
}

const demoFixture = `
CREATE SCHEMA demo;
CREATE SCHEMA demo_api;
CREATE TABLE demo.request (id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, kind TEXT NOT NULL, input JSONB NOT NULL,
    requested_by TEXT NOT NULL, origin TEXT NOT NULL UNIQUE, state TEXT NOT NULL DEFAULT 'waiting', result JSONB);
CREATE VIEW demo_api.request_v1 AS SELECT id, kind, input, requested_by, origin, state, result FROM demo.request;
CREATE FUNCTION demo_api.new_request_v1(p_kind TEXT, p_input JSONB, p_requested_by TEXT, p_origin TEXT)
RETURNS BIGINT LANGUAGE plpgsql AS $$
DECLARE v_id BIGINT;
BEGIN
    SELECT id INTO v_id FROM demo.request WHERE origin = p_origin;
    IF FOUND THEN RETURN v_id; END IF;
    INSERT INTO demo.request (kind, input, requested_by, origin) VALUES (p_kind, p_input, p_requested_by, p_origin)
    RETURNING id INTO v_id;
    RETURN v_id;
END;
$$;
CREATE TABLE demo.brief (id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, vertical TEXT NOT NULL, images INTEGER NOT NULL,
    requested_by TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'making');
CREATE VIEW demo_api.brief_v1 AS SELECT id, vertical, images, requested_by, state FROM demo.brief;
CREATE FUNCTION demo_api.new_brief_v1(p_vertical TEXT, p_images INTEGER, p_requested_by TEXT)
RETURNS BIGINT LANGUAGE plpgsql AS $$
DECLARE v_id BIGINT;
BEGIN
    IF p_images > 12 THEN RAISE EXCEPTION 'at most 12 images in a brief'; END IF;
    INSERT INTO demo.brief (vertical, images, requested_by) VALUES (p_vertical, p_images, p_requested_by) RETURNING id INTO v_id;
    RETURN v_id;
END;
$$;
CREATE TABLE demo.option (id BIGINT PRIMARY KEY, brief_id BIGINT NOT NULL, image_url TEXT, headline TEXT);
CREATE VIEW demo_api.option_v1 AS SELECT id, brief_id, image_url, headline FROM demo.option;
`

// Catalog is the demo app's actions, over the tables Demo creates.
func Catalog(t testing.TB) *actions.Catalog {
	t.Helper()
	c, err := actions.LoadFS(fstest.MapFS{"demo.json": {Data: []byte(demoCatalog)}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const demoCatalog = `{"app": "demo", "actions": [
{"name": "demo.new_pair", "version": 1, "kind": "ask", "says": "Ask for a desktop and mobile pair from some options, for a person to confirm.",
 "call": "demo_api.new_request_v1",
 "args": [{"name": "kind", "const": "new_pair"},
          {"name": "input", "type": "object", "says": "the options and the account",
           "schema": {"type": "object", "additionalProperties": false, "required": ["options", "account"],
                      "properties": {"options": {"type": "array", "items": {"type": "integer"}}, "account": {"type": "string"},
                                     "note": {"type": "string"}}}},
          {"name": "requested_by", "from": "person"}, {"name": "origin", "from": "origin"}],
 "returns": "the request's id",
 "follow": {"view": "demo_api.request_v1", "id": "id", "state": "state", "done": ["sent"], "failed": ["refused"]},
 "link": "/demo/requests/{returned}", "per_day": 5},
{"name": "demo.new_brief", "version": 1, "kind": "change", "says": "Make a brief: images and headlines for a vertical.",
 "call": "demo_api.new_brief_v1",
 "args": [{"name": "vertical", "type": "string", "enum": ["Memory Loss", "Tinnitus"], "says": "the vertical"},
          {"name": "images", "type": "integer", "says": "how many images"},
          {"name": "requested_by", "from": "person"}],
 "returns": "the brief's id",
 "follow": {"view": "demo_api.brief_v1", "id": "id", "state": "state", "done": ["ready"], "failed": ["failed"]},
 "link": "/demo/briefs/{returned}", "per_day": 3},
{"name": "demo.options", "version": 1, "kind": "read", "says": "The options a brief made: an image and a headline each.",
 "view": "demo_api.option_v1", "columns": ["id", "brief_id", "image_url", "headline"], "outside": ["headline"],
 "filters": [{"column": "brief_id", "op": "=", "type": "integer", "says": "the brief"},
             {"column": "headline", "op": "has", "type": "string", "says": "words in the headline"},
             {"column": "id", "op": "in", "type": "integer", "says": "these options"}],
 "order": "id", "limit": 20, "show": {"id": "id", "image": "image_url", "text": "headline"}},
{"name": "demo.briefs", "version": 1, "kind": "read", "says": "Briefs, newest first.",
 "view": "demo_api.brief_v1", "columns": ["id", "vertical", "images", "requested_by", "state"],
 "filters": [{"column": "state", "op": "in", "type": "string", "enum": ["making", "ready", "failed"], "says": "these states"},
             {"column": "id", "op": ">=", "type": "integer", "says": "from this id"}],
 "order": "id DESC", "limit": 5}
]}`
