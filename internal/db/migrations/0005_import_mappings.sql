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
