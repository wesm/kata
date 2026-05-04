package jsonl_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
	"github.com/wesm/kata/internal/uid"
	_ "modernc.org/sqlite"
)

func TestAutoCutoverNoopsAtCurrentSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, d.Close())

	require.NoError(t, jsonl.AutoCutover(ctx, path))

	ver, err := db.PeekSchemaVersion(ctx, path)
	require.NoError(t, err)
	assert.Equal(t, db.CurrentSchemaVersion(), ver)
	assertNoCutoverTemps(t, path)
}

func TestAutoCutoverRefusesExistingTempFiles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, d.Close())
	require.NoError(t, os.WriteFile(path+".import.tmp.jsonl", []byte("partial"), 0o600))

	err = jsonl.AutoCutover(ctx, path)

	require.Error(t, err)
	assert.True(t, errors.Is(err, jsonl.ErrCutoverInProgress))
}

func TestAutoCutoverFailureLeavesSourceAndRemovesTemps(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	writeVersionZeroDB(t, path)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	err = jsonl.AutoCutover(ctx, path)

	require.Error(t, err)
	after, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, before, after)
	assertNoCutoverTemps(t, path)
}

func TestAutoCutoverUpgradesLegacyV1DB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	writeLegacyV1DB(t, path)

	require.NoError(t, jsonl.AutoCutover(ctx, path))

	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	ver, err := db.PeekSchemaVersion(ctx, path)
	require.NoError(t, err)
	assert.Equal(t, db.CurrentSchemaVersion(), ver)
	var projectUID, issueUID, eventIssueUID string
	require.NoError(t, d.QueryRow(`SELECT uid FROM projects WHERE id = 1`).Scan(&projectUID))
	require.NoError(t, d.QueryRow(`SELECT uid FROM issues WHERE id = 1`).Scan(&issueUID))
	require.NoError(t, d.QueryRow(`SELECT issue_uid FROM events WHERE id = 1`).Scan(&eventIssueUID))
	assert.True(t, uid.Valid(projectUID))
	assert.True(t, uid.Valid(issueUID))
	assert.Equal(t, issueUID, eventIssueUID)
	assertNoCutoverTemps(t, path)
}

func TestPeekSchemaVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, d.Close())

	ver, err := db.PeekSchemaVersion(ctx, path)
	require.NoError(t, err)
	assert.Equal(t, db.CurrentSchemaVersion(), ver)

	noMeta := filepath.Join(t.TempDir(), "empty.db")
	raw, err := sql.Open("sqlite", noMeta)
	require.NoError(t, err)
	require.NoError(t, raw.PingContext(ctx))
	require.NoError(t, raw.Close())
	ver, err = db.PeekSchemaVersion(ctx, noMeta)
	require.NoError(t, err)
	assert.Equal(t, 0, ver)
}

func writeLegacyV1DB(t *testing.T, path string) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	schema := `
CREATE TABLE projects (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  identity TEXT UNIQUE NOT NULL,
  name TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  next_issue_number INTEGER NOT NULL DEFAULT 2
);
CREATE TABLE project_aliases (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL REFERENCES projects(id),
  alias_identity TEXT UNIQUE NOT NULL,
  alias_kind TEXT NOT NULL,
  root_path TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  last_seen_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE issues (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL REFERENCES projects(id),
  number INTEGER NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open',
  closed_reason TEXT,
  owner TEXT,
  author TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  closed_at DATETIME,
  deleted_at DATETIME
);
CREATE TABLE comments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  issue_id INTEGER NOT NULL REFERENCES issues(id),
  author TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE links (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL REFERENCES projects(id),
  from_issue_id INTEGER NOT NULL REFERENCES issues(id),
  to_issue_id INTEGER NOT NULL REFERENCES issues(id),
  type TEXT NOT NULL,
  author TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE issue_labels (
  issue_id INTEGER NOT NULL REFERENCES issues(id),
  label TEXT NOT NULL,
  author TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY(issue_id, label)
);
CREATE TABLE events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL REFERENCES projects(id),
  project_identity TEXT NOT NULL,
  issue_id INTEGER REFERENCES issues(id),
  issue_number INTEGER,
  related_issue_id INTEGER REFERENCES issues(id),
  type TEXT NOT NULL,
  actor TEXT NOT NULL,
  payload TEXT NOT NULL DEFAULT '{}',
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE purge_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL,
  purged_issue_id INTEGER NOT NULL,
  project_identity TEXT NOT NULL,
  issue_number INTEGER NOT NULL,
  issue_title TEXT NOT NULL,
  issue_author TEXT NOT NULL,
  comment_count INTEGER NOT NULL,
  link_count INTEGER NOT NULL,
  label_count INTEGER NOT NULL,
  event_count INTEGER NOT NULL,
  events_deleted_min_id INTEGER,
  events_deleted_max_id INTEGER,
  purge_reset_after_event_id INTEGER,
  actor TEXT NOT NULL,
  reason TEXT,
  purged_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO meta(key, value) VALUES('schema_version', '1');
INSERT INTO meta(key, value) VALUES('created_by_version', '0.1.0');
INSERT INTO projects(id, identity, name, created_at, next_issue_number)
VALUES(1, 'github.com/wesm/kata', 'kata', '2026-05-03T00:00:00.000Z', 2);
INSERT INTO project_aliases(id, project_id, alias_identity, alias_kind, root_path, created_at, last_seen_at)
VALUES(1, 1, 'github.com/wesm/kata', 'git', '/tmp/kata', '2026-05-03T00:00:00.000Z', '2026-05-03T00:00:00.000Z');
INSERT INTO issues(id, project_id, number, title, body, status, closed_reason, owner, author, created_at, updated_at, closed_at, deleted_at)
VALUES(1, 1, 1, 'legacy issue', '', 'open', NULL, NULL, 'tester', '2026-05-03T00:00:01.000Z', '2026-05-03T00:00:01.000Z', NULL, NULL);
INSERT INTO comments(id, issue_id, author, body, created_at)
VALUES(1, 1, 'tester', 'legacy comment', '2026-05-03T00:00:02.000Z');
INSERT INTO issue_labels(issue_id, label, author, created_at)
VALUES(1, 'bug', 'tester', '2026-05-03T00:00:02.000Z');
INSERT INTO events(id, project_id, project_identity, issue_id, issue_number, related_issue_id, type, actor, payload, created_at)
VALUES(1, 1, 'github.com/wesm/kata', 1, 1, NULL, 'issue.created', 'tester', '{}', '2026-05-03T00:00:01.000Z');
`
	_, err = raw.Exec(schema)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
}

func writeLegacyV2DB(t *testing.T, path string) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	schema := `
CREATE TABLE projects (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  uid TEXT NOT NULL UNIQUE,
  identity TEXT UNIQUE NOT NULL,
  name TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  next_issue_number INTEGER NOT NULL DEFAULT 1,
  CHECK (length(uid) = 26)
);
CREATE TABLE project_aliases (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL REFERENCES projects(id),
  alias_identity TEXT UNIQUE NOT NULL,
  alias_kind TEXT NOT NULL,
  root_path TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  last_seen_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE issues (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  uid TEXT NOT NULL UNIQUE,
  project_id INTEGER NOT NULL REFERENCES projects(id),
  number INTEGER NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open',
  closed_reason TEXT,
  owner TEXT,
  author TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  closed_at DATETIME,
  deleted_at DATETIME,
  UNIQUE(project_id, number),
  CHECK (length(uid) = 26)
);
CREATE TABLE comments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  issue_id INTEGER NOT NULL REFERENCES issues(id),
  author TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE links (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL REFERENCES projects(id),
  from_issue_id INTEGER NOT NULL REFERENCES issues(id),
  to_issue_id INTEGER NOT NULL REFERENCES issues(id),
  from_issue_uid TEXT NOT NULL,
  to_issue_uid TEXT NOT NULL,
  type TEXT NOT NULL,
  author TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE issue_labels (
  issue_id INTEGER NOT NULL REFERENCES issues(id),
  label TEXT NOT NULL,
  author TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY(issue_id, label)
);
-- v2 events: no uid, no origin_instance_uid (the v2→v3 cutover backfills them).
CREATE TABLE events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL REFERENCES projects(id),
  project_identity TEXT NOT NULL,
  issue_id INTEGER REFERENCES issues(id),
  issue_uid TEXT,
  issue_number INTEGER,
  related_issue_id INTEGER REFERENCES issues(id),
  related_issue_uid TEXT,
  type TEXT NOT NULL,
  actor TEXT NOT NULL,
  payload TEXT NOT NULL DEFAULT '{}',
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
-- v2 purge_log: no uid, no origin_instance_uid.
CREATE TABLE purge_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL,
  purged_issue_id INTEGER NOT NULL,
  issue_uid TEXT,
  project_uid TEXT,
  project_identity TEXT NOT NULL,
  issue_number INTEGER NOT NULL,
  issue_title TEXT NOT NULL,
  issue_author TEXT NOT NULL,
  comment_count INTEGER NOT NULL,
  link_count INTEGER NOT NULL,
  label_count INTEGER NOT NULL,
  event_count INTEGER NOT NULL,
  events_deleted_min_id INTEGER,
  events_deleted_max_id INTEGER,
  purge_reset_after_event_id INTEGER,
  actor TEXT NOT NULL,
  reason TEXT,
  purged_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO meta(key, value) VALUES('schema_version', '2');
INSERT INTO meta(key, value) VALUES('created_by_version', '0.1.0');

INSERT INTO projects(id, uid, identity, name, created_at, next_issue_number)
VALUES(1, '01HZZZZZZZZZZZZZZZZZZZZZZZ', 'github.com/wesm/kata', 'kata', '2026-05-03T00:00:00.000Z', 2);
INSERT INTO issues(id, uid, project_id, number, title, body, status, author, created_at, updated_at)
VALUES(1, '01HZZZZZZZZZZZZZZZZZZZZZZ1', 1, 1, 'v2 issue', '', 'open', 'tester',
       '2026-05-03T00:00:01.000Z', '2026-05-03T00:00:01.000Z');
-- Two real events on issue #1: created (carrying idempotency_key) + commented.
INSERT INTO events(id, project_id, project_identity, issue_id, issue_uid, issue_number, type, actor, payload, created_at)
VALUES(1, 1, 'github.com/wesm/kata', 1, '01HZZZZZZZZZZZZZZZZZZZZZZ1', 1, 'issue.created', 'tester',
       '{"idempotency_key":"K1","idempotency_fingerprint":"fp"}', '2026-05-03T00:00:01.000Z');
INSERT INTO events(id, project_id, project_identity, issue_id, issue_uid, issue_number, type, actor, payload, created_at)
VALUES(2, 1, 'github.com/wesm/kata', 1, '01HZZZZZZZZZZZZZZZZZZZZZZ1', 1, 'issue.commented', 'tester',
       '{"comment_id":42}', '2026-05-03T00:00:02.000Z');
-- A purge_log row referring to a long-gone issue #99 with the synthetic SSE
-- reset cursor reserved at 99 (a value greater than any current events.id).
INSERT INTO purge_log(id, project_id, purged_issue_id, issue_uid, project_uid, project_identity,
                      issue_number, issue_title, issue_author, comment_count, link_count, label_count,
                      event_count, events_deleted_min_id, events_deleted_max_id,
                      purge_reset_after_event_id, actor, reason, purged_at)
VALUES(1, 1, 99, '01HZZZZZZZZZZZZZZZZZZZZZ99', '01HZZZZZZZZZZZZZZZZZZZZZZZ', 'github.com/wesm/kata',
       99, 'gone', 'tester', 0, 0, 0, 1, 50, 50, 99, 'tester', 'fixture',
       '2026-05-03T00:00:03.000Z');
INSERT INTO sqlite_sequence(name, seq) VALUES('events', 99);
INSERT INTO sqlite_sequence(name, seq) VALUES('issues', 1);
INSERT INTO sqlite_sequence(name, seq) VALUES('projects', 1);
INSERT INTO sqlite_sequence(name, seq) VALUES('purge_log', 1);
`
	_, err = raw.Exec(schema)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
}

// TestRoundtripV4PreservesDeletedAt covers #24's projects.deleted_at column:
// an archived project in the source DB round-trips through JSONL with the
// timestamp intact, so kata-instance backups don't silently un-archive
// projects on restore.
func TestRoundtripV4PreservesDeletedAt(t *testing.T) {
	ctx := context.Background()
	src := openExportTestDB(t)
	p, err := src.CreateProject(ctx, "github.com/wesm/proj-archived", "archived")
	require.NoError(t, err)
	_, err = src.ExecContext(ctx,
		`UPDATE projects SET deleted_at = '2026-05-04T12:00:00.000Z' WHERE id = ?`, p.ID)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, src, &buf, jsonl.ExportOptions{IncludeDeleted: true}))

	dstPath := filepath.Join(t.TempDir(), "dst.db")
	dst, err := db.Open(ctx, dstPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dst.Close() })
	require.NoError(t, jsonl.Import(ctx, bytes.NewReader(buf.Bytes()), dst))

	var got sql.NullTime
	require.NoError(t, dst.QueryRowContext(ctx,
		`SELECT deleted_at FROM projects WHERE id = ?`, p.ID).Scan(&got))
	require.True(t, got.Valid, "deleted_at must round-trip as a non-null timestamp")
	assert.Equal(t, "2026-05-04T12:00:00Z", got.Time.UTC().Format("2006-01-02T15:04:05Z"))
}

func writeVersionZeroDB(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	_, err = d.ExecContext(ctx, `UPDATE meta SET value='0' WHERE key='schema_version'`)
	require.NoError(t, err)
	require.NoError(t, d.Close())
}

func assertNoCutoverTemps(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{".import.tmp.jsonl", ".import.tmp.db"} {
		_, err := os.Stat(path + suffix)
		assert.True(t, os.IsNotExist(err), path+suffix)
	}
}
