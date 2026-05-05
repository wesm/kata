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
