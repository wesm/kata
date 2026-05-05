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
-- v2 events: no uid, no origin_instance_uid (the v2->v3 cutover backfills them).
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
