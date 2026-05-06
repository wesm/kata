-- Canonical kata schema. Older databases reach this shape via JSONL cutover
-- (internal/jsonl/cutover.go); this file is the single source of truth applied
-- to fresh databases by db.Open's bootstrap path.

CREATE TABLE projects (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  uid               TEXT NOT NULL UNIQUE,
  identity          TEXT UNIQUE NOT NULL,
  name              TEXT NOT NULL,
  created_at        DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  next_issue_number INTEGER NOT NULL DEFAULT 1,
  deleted_at        DATETIME,
  CHECK (length(uid) = 26),
  CHECK (length(trim(identity)) > 0),
  CHECK (length(trim(name)) > 0)
);
CREATE INDEX idx_projects_active ON projects(id) WHERE deleted_at IS NULL;

CREATE TABLE project_aliases (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id      INTEGER NOT NULL REFERENCES projects(id),
  alias_identity  TEXT UNIQUE NOT NULL,    -- normalized git remote, or 'local://<abs path>'
  alias_kind      TEXT NOT NULL CHECK(alias_kind IN ('git','local')),
  root_path       TEXT NOT NULL,           -- last seen absolute workspace root for this alias
  created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  last_seen_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  CHECK (length(trim(alias_identity)) > 0),
  CHECK (length(trim(root_path)) > 0)
);
CREATE INDEX idx_project_aliases_project ON project_aliases(project_id);

CREATE TABLE issues (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  uid           TEXT NOT NULL UNIQUE,
  project_id    INTEGER NOT NULL REFERENCES projects(id),
  number        INTEGER NOT NULL,
  title         TEXT NOT NULL,
  body          TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL CHECK(status IN ('open','closed')) DEFAULT 'open',
  closed_reason TEXT CHECK(closed_reason IN ('done','wontfix','duplicate')),
  owner         TEXT,
  priority      INTEGER,                       -- 0 = highest, 4 = lowest; NULL = unset
  author        TEXT NOT NULL,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  closed_at     DATETIME,
  deleted_at    DATETIME,
  UNIQUE(project_id, number),
  CHECK (length(uid) = 26),
  CHECK (length(trim(title))  > 0),
  CHECK (length(trim(author)) > 0),
  CHECK (status = 'closed' OR (closed_at IS NULL AND closed_reason IS NULL)),
  CHECK (priority IS NULL OR priority BETWEEN 0 AND 4)
);
CREATE INDEX idx_issues_project_status_updated
  ON issues(project_id, status, updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_issues_project_updated
  ON issues(project_id, updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_issues_owner
  ON issues(owner) WHERE owner IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE comments (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  issue_id   INTEGER NOT NULL REFERENCES issues(id),
  author     TEXT NOT NULL,
  body       TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  CHECK (length(trim(author)) > 0),
  CHECK (length(trim(body))   > 0)
);
CREATE INDEX idx_comments_issue ON comments(issue_id, created_at);

CREATE TABLE links (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id    INTEGER NOT NULL REFERENCES projects(id),
  from_issue_id INTEGER NOT NULL REFERENCES issues(id),
  to_issue_id   INTEGER NOT NULL REFERENCES issues(id),
  from_issue_uid TEXT NOT NULL,
  to_issue_uid   TEXT NOT NULL,
  type          TEXT NOT NULL CHECK(type IN ('parent','blocks','related')),
  author        TEXT NOT NULL,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(from_issue_id, to_issue_id, type),
  CHECK (from_issue_id <> to_issue_id),
  CHECK (length(from_issue_uid) = 26),
  CHECK (length(to_issue_uid) = 26),
  CHECK (length(trim(author)) > 0),
  CHECK (type <> 'related' OR from_issue_id < to_issue_id)
);
CREATE UNIQUE INDEX uniq_one_parent_per_child
  ON links(from_issue_id) WHERE type = 'parent';
CREATE INDEX idx_links_from    ON links(from_issue_id, type);
CREATE INDEX idx_links_to      ON links(to_issue_id, type);
CREATE INDEX idx_links_project ON links(project_id);
CREATE INDEX idx_links_from_uid ON links(from_issue_uid);
CREATE INDEX idx_links_to_uid   ON links(to_issue_uid);

-- Enforce same-project: both endpoints must belong to links.project_id.
CREATE TRIGGER trg_links_same_project_insert
BEFORE INSERT ON links
FOR EACH ROW BEGIN
  SELECT RAISE(ABORT, 'cross-project links are not allowed')
  WHERE (SELECT project_id FROM issues WHERE id = NEW.from_issue_id) <> NEW.project_id
     OR (SELECT project_id FROM issues WHERE id = NEW.to_issue_id)   <> NEW.project_id;
END;
CREATE TRIGGER trg_links_same_project_update
BEFORE UPDATE ON links
FOR EACH ROW BEGIN
  SELECT RAISE(ABORT, 'cross-project links are not allowed')
  WHERE (SELECT project_id FROM issues WHERE id = NEW.from_issue_id) <> NEW.project_id
     OR (SELECT project_id FROM issues WHERE id = NEW.to_issue_id)   <> NEW.project_id;
END;

CREATE TRIGGER trg_links_uid_consistency_insert
BEFORE INSERT ON links
FOR EACH ROW BEGIN
  SELECT RAISE(ABORT, 'from_issue_uid does not match from_issue_id')
  WHERE NEW.from_issue_uid <> (SELECT uid FROM issues WHERE id = NEW.from_issue_id);
  SELECT RAISE(ABORT, 'to_issue_uid does not match to_issue_id')
  WHERE NEW.to_issue_uid <> (SELECT uid FROM issues WHERE id = NEW.to_issue_id);
END;
CREATE TRIGGER trg_links_uid_consistency_update
BEFORE UPDATE ON links
FOR EACH ROW BEGIN
  SELECT RAISE(ABORT, 'from_issue_uid does not match from_issue_id')
  WHERE NEW.from_issue_uid <> (SELECT uid FROM issues WHERE id = NEW.from_issue_id);
  SELECT RAISE(ABORT, 'to_issue_uid does not match to_issue_id')
  WHERE NEW.to_issue_uid <> (SELECT uid FROM issues WHERE id = NEW.to_issue_id);
END;

CREATE TRIGGER trg_projects_uid_immutable
BEFORE UPDATE OF uid ON projects
FOR EACH ROW BEGIN
  SELECT RAISE(ABORT, 'projects.uid is immutable')
  WHERE NEW.uid <> OLD.uid;
END;
CREATE TRIGGER trg_issues_uid_immutable
BEFORE UPDATE OF uid ON issues
FOR EACH ROW BEGIN
  SELECT RAISE(ABORT, 'issues.uid is immutable')
  WHERE NEW.uid <> OLD.uid;
END;

CREATE TABLE issue_labels (
  issue_id   INTEGER NOT NULL REFERENCES issues(id),
  label      TEXT NOT NULL,
  author     TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY(issue_id, label),
  CHECK (length(label) BETWEEN 1 AND 64),
  CHECK (label NOT GLOB '*[^a-z0-9._:-]*'),
  CHECK (length(trim(author)) > 0)
);
CREATE INDEX idx_issue_labels_label ON issue_labels(label);

CREATE TABLE events (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  uid                 TEXT NOT NULL UNIQUE,
  origin_instance_uid TEXT NOT NULL,
  project_id          INTEGER NOT NULL REFERENCES projects(id),
  project_identity    TEXT NOT NULL,
  issue_id            INTEGER REFERENCES issues(id),
  issue_uid           TEXT,
  issue_number        INTEGER,
  related_issue_id    INTEGER REFERENCES issues(id),
  related_issue_uid   TEXT,
  type                TEXT NOT NULL,
  actor               TEXT NOT NULL,
  payload             TEXT NOT NULL DEFAULT '{}',
  created_at          DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  CHECK (length(trim(actor)) > 0),
  CHECK (json_valid(payload)),
  CHECK (length(uid) = 26),
  CHECK (length(origin_instance_uid) = 26)
);
CREATE INDEX idx_events_project ON events(project_id, id);
CREATE INDEX idx_events_issue   ON events(issue_id, id) WHERE issue_id IS NOT NULL;
CREATE INDEX idx_events_related ON events(related_issue_id, id) WHERE related_issue_id IS NOT NULL;
CREATE INDEX idx_events_issue_uid ON events(issue_uid) WHERE issue_uid IS NOT NULL;
CREATE INDEX idx_events_related_issue_uid ON events(related_issue_uid) WHERE related_issue_uid IS NOT NULL;
CREATE INDEX idx_events_origin_instance ON events(origin_instance_uid);
CREATE INDEX idx_events_idempotency
  ON events(project_id, json_extract(payload, '$.idempotency_key'), created_at)
  WHERE type = 'issue.created' AND json_extract(payload, '$.idempotency_key') IS NOT NULL;

CREATE TABLE purge_log (
  id                          INTEGER PRIMARY KEY AUTOINCREMENT,
  uid                         TEXT NOT NULL UNIQUE,
  origin_instance_uid         TEXT NOT NULL,
  project_id                  INTEGER NOT NULL,   -- snapshot; no FK so audit survives any future project cleanup
  purged_issue_id             INTEGER NOT NULL,   -- the deleted issues.id; no FK (the row is gone)
  issue_uid                   TEXT,
  project_uid                 TEXT,
  project_identity            TEXT NOT NULL,      -- snapshot of projects.identity at purge time
  issue_number                INTEGER NOT NULL,
  issue_title                 TEXT NOT NULL,
  issue_author                TEXT NOT NULL,
  comment_count               INTEGER NOT NULL,
  link_count                  INTEGER NOT NULL,
  label_count                 INTEGER NOT NULL,
  event_count                 INTEGER NOT NULL,
  events_deleted_min_id       INTEGER,            -- audit (min events.id deleted; NULL if none)
  events_deleted_max_id       INTEGER,            -- audit (max events.id deleted; NULL if none)
  purge_reset_after_event_id  INTEGER,            -- SSE reset cursor; subscribers with cursor < this must reset
  actor                       TEXT NOT NULL,
  reason                      TEXT,
  purged_at                   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  CHECK (length(trim(actor)) > 0),
  CHECK (length(uid) = 26),
  CHECK (length(origin_instance_uid) = 26)
);
CREATE INDEX idx_purge_log_reset
  ON purge_log(purge_reset_after_event_id) WHERE purge_reset_after_event_id IS NOT NULL;
CREATE INDEX idx_purge_log_project_reset
  ON purge_log(project_id, purge_reset_after_event_id) WHERE purge_reset_after_event_id IS NOT NULL;
CREATE INDEX idx_purge_log_issue  ON purge_log(purged_issue_id);
CREATE INDEX idx_purge_log_lookup ON purge_log(project_identity, issue_number);
CREATE INDEX idx_purge_log_issue_uid ON purge_log(issue_uid) WHERE issue_uid IS NOT NULL;
CREATE INDEX idx_purge_log_project_uid ON purge_log(project_uid) WHERE project_uid IS NOT NULL;
CREATE INDEX idx_purge_log_origin_instance ON purge_log(origin_instance_uid);

CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
INSERT INTO meta(key, value) VALUES ('created_by_version', '0.1.0');

-- FTS5 virtual table over issue title+body+comments, kept in sync via triggers below.
CREATE VIRTUAL TABLE issues_fts USING fts5(
  title, body, comments,
  content='', tokenize='unicode61 remove_diacritics 2'
);

-- FTS5 sync triggers. The issues_fts table uses content='' so each delete must
-- provide the previously indexed column values; we stay in sync by routing every
-- title/body/comments mutation through one of the five triggers below. comments
-- is stored as a single space-separated aggregate built from the comments table
-- at trigger time.
--
-- Soft-delete (issues.deleted_at IS NOT NULL) does NOT remove rows from FTS --
-- look-alike checks and search filter deleted rows at query time so soft-deleted
-- issues remain reachable for `kata search --include-deleted` later.

CREATE TRIGGER issues_ai_fts AFTER INSERT ON issues BEGIN
  INSERT INTO issues_fts(rowid, title, body, comments)
  VALUES (NEW.id, NEW.title, NEW.body, '');
END;

-- All GROUP_CONCAT operations below wrap their source in a subquery with
-- ORDER BY id so the aggregate is deterministic. SQLite does not guarantee
-- input order to GROUP_CONCAT without ORDER BY, and the FTS5 'delete' command
-- on a contentless table requires the exact bytes that were last inserted —
-- any drift between the insert form and the delete form leaves stale tokens
-- in the index.

CREATE TRIGGER issues_au_fts AFTER UPDATE OF title, body ON issues BEGIN
  INSERT INTO issues_fts(issues_fts, rowid, title, body, comments) VALUES (
    'delete', OLD.id, OLD.title, OLD.body,
    COALESCE((SELECT GROUP_CONCAT(body, ' ') FROM (
      SELECT body FROM comments WHERE issue_id = OLD.id ORDER BY id
    )), '')
  );
  INSERT INTO issues_fts(rowid, title, body, comments) VALUES (
    NEW.id, NEW.title, NEW.body,
    COALESCE((SELECT GROUP_CONCAT(body, ' ') FROM (
      SELECT body FROM comments WHERE issue_id = NEW.id ORDER BY id
    )), '')
  );
END;

CREATE TRIGGER issues_ad_fts AFTER DELETE ON issues BEGIN
  -- Purge cascade deletes comments before issues, so the GROUP_CONCAT here is
  -- always '' at trigger time. We still pass it explicitly so the FTS delete
  -- command sees the same column shape we last inserted.
  INSERT INTO issues_fts(issues_fts, rowid, title, body, comments) VALUES (
    'delete', OLD.id, OLD.title, OLD.body,
    COALESCE((SELECT GROUP_CONCAT(body, ' ') FROM (
      SELECT body FROM comments WHERE issue_id = OLD.id ORDER BY id
    )), '')
  );
END;

CREATE TRIGGER comments_ai_fts AFTER INSERT ON comments BEGIN
  -- Pre-insert state (what FTS currently holds) excludes the just-inserted row.
  INSERT INTO issues_fts(issues_fts, rowid, title, body, comments) VALUES (
    'delete',
    NEW.issue_id,
    (SELECT title FROM issues WHERE id = NEW.issue_id),
    (SELECT body  FROM issues WHERE id = NEW.issue_id),
    COALESCE((SELECT GROUP_CONCAT(body, ' ') FROM (
      SELECT body FROM comments WHERE issue_id = NEW.issue_id AND id <> NEW.id ORDER BY id
    )), '')
  );
  -- Post-insert state (what FTS should hold) includes it.
  INSERT INTO issues_fts(rowid, title, body, comments) VALUES (
    NEW.issue_id,
    (SELECT title FROM issues WHERE id = NEW.issue_id),
    (SELECT body  FROM issues WHERE id = NEW.issue_id),
    COALESCE((SELECT GROUP_CONCAT(body, ' ') FROM (
      SELECT body FROM comments WHERE issue_id = NEW.issue_id ORDER BY id
    )), '')
  );
END;

CREATE TRIGGER comments_ad_fts AFTER DELETE ON comments BEGIN
  -- Pre-delete state (what FTS currently holds) included the deleted row at
  -- its id-ordered position. Reconstruct by unioning OLD back in with its id
  -- and ORDER BY id so the aggregate matches the form last inserted into FTS.
  INSERT INTO issues_fts(issues_fts, rowid, title, body, comments) VALUES (
    'delete',
    OLD.issue_id,
    (SELECT title FROM issues WHERE id = OLD.issue_id),
    (SELECT body  FROM issues WHERE id = OLD.issue_id),
    COALESCE((SELECT GROUP_CONCAT(body, ' ') FROM (
      SELECT id, body FROM comments WHERE issue_id = OLD.issue_id
      UNION ALL
      SELECT OLD.id, OLD.body
      ORDER BY id
    )), '')
  );
  -- Post-delete state (what FTS should hold) excludes it.
  INSERT INTO issues_fts(rowid, title, body, comments) VALUES (
    OLD.issue_id,
    (SELECT title FROM issues WHERE id = OLD.issue_id),
    (SELECT body  FROM issues WHERE id = OLD.issue_id),
    COALESCE((SELECT GROUP_CONCAT(body, ' ') FROM (
      SELECT body FROM comments WHERE issue_id = OLD.issue_id ORDER BY id
    )), '')
  );
END;

CREATE TABLE import_mappings (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  source            TEXT NOT NULL,
  external_id       TEXT NOT NULL,
  object_type       TEXT NOT NULL CHECK(object_type IN ('issue','comment','label','link')),
  project_id        INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  issue_id          INTEGER REFERENCES issues(id) ON DELETE CASCADE,
  comment_id        INTEGER REFERENCES comments(id) ON DELETE CASCADE,
  link_id           INTEGER REFERENCES links(id) ON DELETE CASCADE,
  label             TEXT,
  source_updated_at DATETIME,
  imported_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(source, external_id, object_type, project_id),
  CHECK (length(trim(source)) > 0),
  CHECK (length(trim(external_id)) > 0),
  CHECK (object_type != 'issue' OR issue_id IS NOT NULL),
  CHECK (object_type != 'comment' OR (issue_id IS NOT NULL AND comment_id IS NOT NULL)),
  CHECK (object_type != 'label' OR (issue_id IS NOT NULL AND label IS NOT NULL)),
  CHECK (object_type != 'link' OR (issue_id IS NOT NULL AND link_id IS NOT NULL))
);
CREATE INDEX idx_import_mappings_issue ON import_mappings(issue_id);
CREATE INDEX idx_import_mappings_comment ON import_mappings(comment_id);
CREATE INDEX idx_import_mappings_link ON import_mappings(link_id);
