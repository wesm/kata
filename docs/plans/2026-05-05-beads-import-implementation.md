# Beads Import Implementation Plan

> **For agentic workers:** REQUIRED: Use `/skill:orchestrator-implements` (in-session, orchestrator implements), `/skill:subagent-driven-development` (in-session, subagents implement), or `/skill:executing-plans` (parallel session) to implement this plan. Steps use checkbox syntax for tracking.

**Goal:** Build `kata import --format beads`, backed by a reusable daemon import endpoint and `import_mappings` source identity table.

**Architecture:** CLI Beads adapter shells out to `bd`, converts Beads issues/comments into a generic import request, and posts to `POST /api/v1/projects/{project_id}/imports`. Daemon validates and delegates to DB import code that upserts by `import_mappings`, preserves timestamps, emits events, and reconciles only source-owned labels/links. Existing kata JSONL import remains default and gains `import_mappings` export/import support.

**Tech Stack:** Go 1.26, Cobra CLI, Huma HTTP handlers, SQLite via `modernc.org/sqlite`, existing kata JSONL encoder/decoder, testify tests.

---

## File Map

- Create `internal/db/migrations/0005_import_mappings.sql`: schema for source identity mappings.
- Modify `internal/db/db.go`: bump `currentSchemaVersion` to 5.
- Create `internal/db/import_mappings.go`: typed mapping queries used by imports and JSONL.
- Create `internal/db/imports.go`: generic import transaction/upsert implementation.
- Create `internal/db/imports_test.go`: DB-level import behavior tests.
- Modify `internal/db/schema_completeness_test.go`: assert new table/indexes exist.
- Modify `internal/jsonl/types.go`: add `KindImportMapping` and rank.
- Modify `internal/jsonl/export.go`: export `import_mappings` after links and before events.
- Modify `internal/jsonl/import.go`: import `import_mapping` envelopes.
- Create or modify `internal/jsonl/import_mappings_test.go`: round-trip/export tests.
- Modify `internal/api/types.go`: add import endpoint request/response DTOs.
- Create `internal/daemon/handlers_imports.go`: Huma route for `POST /imports`.
- Modify `internal/daemon/server.go`: register import handlers.
- Create `internal/daemon/handlers_imports_test.go`: endpoint coverage.
- Modify `cmd/kata/import.go`: add `--format`, dispatch kata JSONL vs Beads.
- Create `cmd/kata/beads_import.go`: Beads shell-out, parsing, label/footer mapping, POST request.
- Create `cmd/kata/beads_import_test.go`: adapter + CLI tests with fake `bd`.

---

### Task 1: Schema and import mapping queries

**TDD scenario:** New feature — full TDD cycle.

**Files:**
- Create: `internal/db/migrations/0005_import_mappings.sql`
- Create: `internal/db/import_mappings.go`
- Modify: `internal/db/db.go`
- Modify: `internal/db/schema_completeness_test.go`
- Test: `internal/db/import_mappings_test.go`

- [ ] **Step 1: Write failing schema and mapping tests**

Add to `internal/db/schema_completeness_test.go` wanted objects:

```go
wanted := []string{
    "projects", "project_aliases", "issues", "comments",
    "links", "issue_labels", "events", "purge_log",
    "meta", "issues_fts", "import_mappings",
}
```

Add index checks in `TestSchemaUIDColumnsIndexesAndTriggers`:

```go
for _, name := range []string{
    "idx_links_from_uid",
    "idx_links_to_uid",
    "idx_events_issue_uid",
    "idx_events_related_issue_uid",
    "idx_events_origin_instance",
    "idx_purge_log_issue_uid",
    "idx_purge_log_project_uid",
    "idx_purge_log_origin_instance",
    "idx_import_mappings_issue",
    "idx_import_mappings_comment",
    "idx_import_mappings_link",
    "trg_links_uid_consistency_insert",
    "trg_links_uid_consistency_update",
    "trg_projects_uid_immutable",
    "trg_issues_uid_immutable",
} {
    assertSchemaObject(t, d, name)
}
```

Create `internal/db/import_mappings_test.go`:

```go
package db_test

import (
    "context"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "github.com/wesm/kata/internal/db"
)

func TestImportMapping_UpsertAndLookupIssue(t *testing.T) {
    d, ctx, p := setupTestProject(t)
    issue := makeIssue(t, ctx, d, p.ID, "imported", "tester")
    srcUpdated := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

    m, err := d.UpsertImportMapping(ctx, db.ImportMappingParams{
        Source:          "beads",
        ExternalID:      "beads-123",
        ObjectType:      "issue",
        ProjectID:       p.ID,
        IssueID:         &issue.ID,
        SourceUpdatedAt: &srcUpdated,
    })
    require.NoError(t, err)
    assert.Equal(t, "beads", m.Source)
    assert.Equal(t, "beads-123", m.ExternalID)
    require.NotNil(t, m.IssueID)
    assert.Equal(t, issue.ID, *m.IssueID)

    got, err := d.ImportMappingBySource(ctx, p.ID, "beads", "issue", "beads-123")
    require.NoError(t, err)
    assert.Equal(t, m.ID, got.ID)
    require.NotNil(t, got.SourceUpdatedAt)
    assert.True(t, got.SourceUpdatedAt.Equal(srcUpdated))
}

func TestImportMapping_ListByProjectSource(t *testing.T) {
    d, ctx, p := setupTestProject(t)
    issue := makeIssue(t, ctx, d, p.ID, "imported", "tester")
    comment, _, err := d.CreateComment(ctx, db.CreateCommentParams{IssueID: issue.ID, Author: "tester", Body: "hi"})
    require.NoError(t, err)

    _, err = d.UpsertImportMapping(ctx, db.ImportMappingParams{
        Source: "beads", ExternalID: "beads-123", ObjectType: "issue", ProjectID: p.ID, IssueID: &issue.ID,
    })
    require.NoError(t, err)
    _, err = d.UpsertImportMapping(ctx, db.ImportMappingParams{
        Source: "beads", ExternalID: "comment-1", ObjectType: "comment", ProjectID: p.ID, IssueID: &issue.ID, CommentID: &comment.ID,
    })
    require.NoError(t, err)

    got, err := d.ImportMappingsByProjectSource(ctx, p.ID, "beads")
    require.NoError(t, err)
    assert.Len(t, got, 2)
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/db -run 'TestAllSchemaTablesExist|TestSchemaUIDColumnsIndexesAndTriggers|TestImportMapping' -count=1
```

Expected: FAIL because `import_mappings` and methods are missing.

- [ ] **Step 3: Add migration**

Create `internal/db/migrations/0005_import_mappings.sql`:

```sql
CREATE TABLE import_mappings (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  source            TEXT NOT NULL,
  external_id       TEXT NOT NULL,
  object_type       TEXT NOT NULL CHECK(object_type IN ('issue','comment','label','link')),
  project_id        INTEGER NOT NULL REFERENCES projects(id),
  issue_id          INTEGER REFERENCES issues(id),
  comment_id        INTEGER REFERENCES comments(id),
  link_id           INTEGER REFERENCES links(id),
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
```

Modify `internal/db/db.go`:

```go
const currentSchemaVersion = 5
```

- [ ] **Step 4: Add mapping query code**

Create `internal/db/import_mappings.go`:

```go
package db

import (
    "context"
    "database/sql"
    "errors"
    "fmt"
    "time"
)

type ImportMapping struct {
    ID              int64      `json:"id"`
    Source          string     `json:"source"`
    ExternalID      string     `json:"external_id"`
    ObjectType      string     `json:"object_type"`
    ProjectID       int64      `json:"project_id"`
    IssueID         *int64     `json:"issue_id,omitempty"`
    CommentID       *int64     `json:"comment_id,omitempty"`
    LinkID          *int64     `json:"link_id,omitempty"`
    Label           *string    `json:"label,omitempty"`
    SourceUpdatedAt *time.Time `json:"source_updated_at,omitempty"`
    ImportedAt      time.Time  `json:"imported_at"`
}

type ImportMappingParams struct {
    Source          string
    ExternalID      string
    ObjectType      string
    ProjectID       int64
    IssueID         *int64
    CommentID       *int64
    LinkID          *int64
    Label           *string
    SourceUpdatedAt *time.Time
}

func (d *DB) UpsertImportMapping(ctx context.Context, p ImportMappingParams) (ImportMapping, error) {
    return upsertImportMapping(ctx, d.DB, p)
}

func upsertImportMapping(ctx context.Context, e execQuerier, p ImportMappingParams) (ImportMapping, error) {
    _, err := e.ExecContext(ctx, `INSERT INTO import_mappings(
        source, external_id, object_type, project_id, issue_id, comment_id, link_id, label, source_updated_at
    ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(source, external_id, object_type, project_id) DO UPDATE SET
        issue_id=excluded.issue_id,
        comment_id=excluded.comment_id,
        link_id=excluded.link_id,
        label=excluded.label,
        source_updated_at=excluded.source_updated_at,
        imported_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
        p.Source, p.ExternalID, p.ObjectType, p.ProjectID, p.IssueID, p.CommentID, p.LinkID, p.Label, p.SourceUpdatedAt)
    if err != nil {
        return ImportMapping{}, fmt.Errorf("upsert import mapping: %w", err)
    }
    return importMappingBySource(ctx, e, p.ProjectID, p.Source, p.ObjectType, p.ExternalID)
}

func (d *DB) ImportMappingBySource(ctx context.Context, projectID int64, source, objectType, externalID string) (ImportMapping, error) {
    return importMappingBySource(ctx, d.DB, projectID, source, objectType, externalID)
}

func importMappingBySource(ctx context.Context, q queryer, projectID int64, source, objectType, externalID string) (ImportMapping, error) {
    row := q.QueryRowContext(ctx, importMappingSelect+` WHERE project_id = ? AND source = ? AND object_type = ? AND external_id = ?`,
        projectID, source, objectType, externalID)
    return scanImportMapping(row)
}

func (d *DB) ImportMappingsByProjectSource(ctx context.Context, projectID int64, source string) ([]ImportMapping, error) {
    rows, err := d.QueryContext(ctx, importMappingSelect+` WHERE project_id = ? AND source = ? ORDER BY id ASC`, projectID, source)
    if err != nil {
        return nil, fmt.Errorf("list import mappings: %w", err)
    }
    defer func() { _ = rows.Close() }()
    var out []ImportMapping
    for rows.Next() {
        m, err := scanImportMapping(rows)
        if err != nil {
            return nil, err
        }
        out = append(out, m)
    }
    return out, rows.Err()
}

const importMappingSelect = `SELECT id, source, external_id, object_type, project_id, issue_id, comment_id, link_id, label, source_updated_at, imported_at FROM import_mappings`

type queryer interface {
    QueryRowContext(context.Context, string, ...any) *sql.Row
}

type execQuerier interface {
    queryer
    ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func scanImportMapping(r rowScanner) (ImportMapping, error) {
    var m ImportMapping
    var issueID, commentID, linkID sql.NullInt64
    var label sql.NullString
    var sourceUpdated sql.NullTime
    err := r.Scan(&m.ID, &m.Source, &m.ExternalID, &m.ObjectType, &m.ProjectID,
        &issueID, &commentID, &linkID, &label, &sourceUpdated, &m.ImportedAt)
    if errors.Is(err, sql.ErrNoRows) {
        return ImportMapping{}, ErrNotFound
    }
    if err != nil {
        return ImportMapping{}, fmt.Errorf("scan import mapping: %w", err)
    }
    if issueID.Valid { m.IssueID = &issueID.Int64 }
    if commentID.Valid { m.CommentID = &commentID.Int64 }
    if linkID.Valid { m.LinkID = &linkID.Int64 }
    if label.Valid { m.Label = &label.String }
    if sourceUpdated.Valid { m.SourceUpdatedAt = &sourceUpdated.Time }
    return m, nil
}
```

- [ ] **Step 5: Run tests and commit**

Run:

```bash
go test ./internal/db -run 'TestAllSchemaTablesExist|TestSchemaUIDColumnsIndexesAndTriggers|TestImportMapping' -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/db/db.go internal/db/migrations/0005_import_mappings.sql internal/db/import_mappings.go internal/db/import_mappings_test.go internal/db/schema_completeness_test.go
git commit -m "feat(db): add import mappings"
```

---

### Task 2: JSONL export/import preserves import mappings

**TDD scenario:** Modifying tested code — run focused existing + new tests.

**Files:**
- Modify: `internal/jsonl/types.go`
- Modify: `internal/jsonl/export.go`
- Modify: `internal/jsonl/import.go`
- Test: `internal/jsonl/import_mappings_test.go`

- [ ] **Step 1: Write failing JSONL tests**

Create `internal/jsonl/import_mappings_test.go`:

```go
package jsonl_test

import (
    "bytes"
    "context"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "github.com/wesm/kata/internal/db"
    "github.com/wesm/kata/internal/jsonl"
)

func TestExportImport_PreservesImportMappings(t *testing.T) {
    ctx := context.Background()
    src := openDB(t)
    p, err := src.CreateProject(ctx, "github.com/wesm/kata", "kata")
    require.NoError(t, err)
    issue, _, err := src.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "from beads", Author: "tester"})
    require.NoError(t, err)
    comment, _, err := src.CreateComment(ctx, db.CreateCommentParams{IssueID: issue.ID, Author: "tester", Body: "comment"})
    require.NoError(t, err)
    srcUpdated := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

    _, err = src.UpsertImportMapping(ctx, db.ImportMappingParams{Source: "beads", ExternalID: "issue-1", ObjectType: "issue", ProjectID: p.ID, IssueID: &issue.ID, SourceUpdatedAt: &srcUpdated})
    require.NoError(t, err)
    _, err = src.UpsertImportMapping(ctx, db.ImportMappingParams{Source: "beads", ExternalID: "comment-1", ObjectType: "comment", ProjectID: p.ID, IssueID: &issue.ID, CommentID: &comment.ID})
    require.NoError(t, err)

    var buf bytes.Buffer
    require.NoError(t, jsonl.Export(ctx, src, &buf, jsonl.ExportOptions{IncludeDeleted: true}))
    assert.Contains(t, buf.String(), `"kind":"import_mapping"`)

    dst := openDB(t)
    require.NoError(t, jsonl.Import(ctx, bytes.NewReader(buf.Bytes()), dst))

    got, err := dst.ImportMappingsByProjectSource(ctx, p.ID, "beads")
    require.NoError(t, err)
    require.Len(t, got, 2)
    assert.Equal(t, "issue-1", got[0].ExternalID)
    assert.Equal(t, "comment-1", got[1].ExternalID)
}
```

Use the existing `openDB(t)` helper from `internal/jsonl/fixtures_test.go`.

- [ ] **Step 2: Run test to verify failure**

Run:

```bash
go test ./internal/jsonl -run TestExportImport_PreservesImportMappings -count=1
```

Expected: FAIL because kind/export/import support is missing.

- [ ] **Step 3: Add JSONL kind and order**

Modify `internal/jsonl/types.go`:

```go
const (
    KindMeta           Kind = "meta"
    KindProject        Kind = "project"
    KindProjectAlias   Kind = "project_alias"
    KindIssue          Kind = "issue"
    KindComment        Kind = "comment"
    KindIssueLabel     Kind = "issue_label"
    KindLink           Kind = "link"
    KindImportMapping  Kind = "import_mapping"
    KindEvent          Kind = "event"
    KindPurgeLog       Kind = "purge_log"
    KindSQLiteSequence Kind = "sqlite_sequence"
)

var kindOrder = map[Kind]int{
    KindMeta:           0,
    KindProject:        1,
    KindProjectAlias:   2,
    KindIssue:          3,
    KindComment:        4,
    KindIssueLabel:     5,
    KindLink:           6,
    KindImportMapping:  7,
    KindEvent:          8,
    KindPurgeLog:       9,
    KindSQLiteSequence: 10,
}
```

- [ ] **Step 4: Export mappings**

In `internal/jsonl/export.go`, call `exportImportMappings` after `exportLinks` and before `exportEvents`:

```go
if err := exportImportMappings(ctx, d, enc, opts); err != nil {
    return err
}
```

Add:

```go
func exportImportMappings(ctx context.Context, d *db.DB, enc *Encoder, opts ExportOptions) error {
    type record struct {
        ID              int64   `json:"id"`
        Source          string  `json:"source"`
        ExternalID      string  `json:"external_id"`
        ObjectType      string  `json:"object_type"`
        ProjectID       int64   `json:"project_id"`
        IssueID         *int64  `json:"issue_id,omitempty"`
        CommentID       *int64  `json:"comment_id,omitempty"`
        LinkID          *int64  `json:"link_id,omitempty"`
        Label           *string `json:"label,omitempty"`
        SourceUpdatedAt *string `json:"source_updated_at,omitempty"`
        ImportedAt      string  `json:"imported_at"`
    }
    query := `SELECT id, source, external_id, object_type, project_id, issue_id, comment_id, link_id, label,
                     CAST(source_updated_at AS TEXT), CAST(imported_at AS TEXT)
              FROM import_mappings`
    args := []any{}
    if opts.ProjectID > 0 {
        query += ` WHERE project_id = ?`
        args = append(args, opts.ProjectID)
    }
    query += ` ORDER BY id ASC`
    rows, err := d.QueryContext(ctx, query, args...)
    if err != nil {
        return fmt.Errorf("export import_mappings: %w", err)
    }
    return scanRecords(rows, KindImportMapping, enc, func(rows *sql.Rows) (record, error) {
        var rec record
        err := rows.Scan(&rec.ID, &rec.Source, &rec.ExternalID, &rec.ObjectType, &rec.ProjectID,
            &rec.IssueID, &rec.CommentID, &rec.LinkID, &rec.Label, &rec.SourceUpdatedAt, &rec.ImportedAt)
        return rec, err
    })
}
```

- [ ] **Step 5: Import mappings**

In `internal/jsonl/import.go`, add `importMappingRecord` near other record types:

```go
type importMappingRecord struct {
    ID              int64   `json:"id"`
    Source          string  `json:"source"`
    ExternalID      string  `json:"external_id"`
    ObjectType      string  `json:"object_type"`
    ProjectID       int64   `json:"project_id"`
    IssueID         *int64  `json:"issue_id,omitempty"`
    CommentID       *int64  `json:"comment_id,omitempty"`
    LinkID          *int64  `json:"link_id,omitempty"`
    Label           *string `json:"label,omitempty"`
    SourceUpdatedAt *string `json:"source_updated_at,omitempty"`
    ImportedAt      string  `json:"imported_at"`
}
```

Add switch case before `KindEvent`:

```go
case KindImportMapping:
    var rec importMappingRecord
    if err := decodeData(env, &rec); err != nil {
        return err
    }
    _, err := tx.ExecContext(ctx,
        `INSERT INTO import_mappings(id, source, external_id, object_type, project_id, issue_id, comment_id, link_id, label, source_updated_at, imported_at)
         VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        rec.ID, rec.Source, rec.ExternalID, rec.ObjectType, rec.ProjectID, rec.IssueID, rec.CommentID, rec.LinkID, rec.Label, rec.SourceUpdatedAt, rec.ImportedAt)
    return wrapImportErr(env.Kind, err)
```

- [ ] **Step 6: Run JSONL tests and commit**

Run:

```bash
go test ./internal/jsonl -run 'TestExportImport_PreservesImportMappings|TestRoundTrip|TestDecoder' -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/jsonl/types.go internal/jsonl/export.go internal/jsonl/import.go internal/jsonl/import_mappings_test.go
git commit -m "feat(jsonl): preserve import mappings"
```

---

### Task 3: DB import transaction and upsert semantics

**TDD scenario:** New feature — full TDD cycle.

**Files:**
- Create: `internal/db/imports.go`
- Test: `internal/db/imports_test.go`

- [ ] **Step 1: Write failing DB import tests**

Create `internal/db/imports_test.go`:

```go
package db_test

import (
    "context"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "github.com/wesm/kata/internal/db"
)

func TestImportBatch_CreatesIssueCommentsLabelsLinks(t *testing.T) {
    d, ctx, p := setupTestProject(t)
    t1 := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    t2 := time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC)

    res, events, err := d.ImportBatch(ctx, db.ImportBatchParams{
        ProjectID: p.ID,
        Source: "beads",
        Actor: "importer",
        Items: []db.ImportItem{
            {ExternalID: "blocker", Title: "Blocker", Body: "body", Author: "alice", Status: "open", CreatedAt: t1, UpdatedAt: t1, Labels: []string{"source:beads", "beads-id:blocker"}, Links: []db.ImportLink{{Type: "blocks", TargetExternalID: "blocked"}}},
            {ExternalID: "blocked", Title: "Blocked", Body: "body", Author: "bob", Status: "closed", ClosedReason: strPtr("done"), CreatedAt: t1, UpdatedAt: t2, ClosedAt: &t2, Labels: []string{"source:beads", "beads-id:blocked"}, Comments: []db.ImportComment{{ExternalID: "c1", Author: "bob", Body: "note", CreatedAt: t2}}},
        },
    })
    require.NoError(t, err)
    assert.Equal(t, 2, res.Created)
    assert.Equal(t, 1, res.Comments)
    assert.Equal(t, 1, res.Links)
    assert.NotEmpty(t, events)

    blockedMap, err := d.ImportMappingBySource(ctx, p.ID, "beads", "issue", "blocked")
    require.NoError(t, err)
    require.NotNil(t, blockedMap.IssueID)
    blocked, err := d.IssueByID(ctx, *blockedMap.IssueID)
    require.NoError(t, err)
    assert.Equal(t, "closed", blocked.Status)
    assert.True(t, blocked.UpdatedAt.Equal(t2))
    require.NotNil(t, blocked.ClosedAt)
    assert.True(t, blocked.ClosedAt.Equal(t2))

    comments := commentsForIssue(t, ctx, d, blocked.ID)
    require.Len(t, comments, 1)
    assert.Equal(t, "note", comments[0].Body)
    assert.True(t, comments[0].CreatedAt.Equal(t2))
}

func TestImportBatch_ReimportUsesNewerTimestamp(t *testing.T) {
    d, ctx, p := setupTestProject(t)
    older := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    newer := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)

    _, _, err := d.ImportBatch(ctx, db.ImportBatchParams{ProjectID: p.ID, Source: "beads", Actor: "importer", Items: []db.ImportItem{{ExternalID: "a", Title: "old", Body: "old body", Author: "alice", Status: "open", CreatedAt: older, UpdatedAt: older}}})
    require.NoError(t, err)
    _, _, err = d.ImportBatch(ctx, db.ImportBatchParams{ProjectID: p.ID, Source: "beads", Actor: "importer", Items: []db.ImportItem{{ExternalID: "a", Title: "new", Body: "new body", Author: "alice", Status: "open", CreatedAt: older, UpdatedAt: newer}}})
    require.NoError(t, err)

    m, err := d.ImportMappingBySource(ctx, p.ID, "beads", "issue", "a")
    require.NoError(t, err)
    issue, err := d.IssueByID(ctx, *m.IssueID)
    require.NoError(t, err)
    assert.Equal(t, "new", issue.Title)
    assert.True(t, issue.UpdatedAt.Equal(newer))

    localTitle := "local wins"
    _, _, _, err = d.EditIssue(ctx, db.EditIssueParams{IssueID: issue.ID, Title: &localTitle, Actor: "local"})
    require.NoError(t, err)
    _, _, err = d.ImportBatch(ctx, db.ImportBatchParams{ProjectID: p.ID, Source: "beads", Actor: "importer", Items: []db.ImportItem{{ExternalID: "a", Title: "stale", Body: "stale body", Author: "alice", Status: "open", CreatedAt: older, UpdatedAt: older}}})
    require.NoError(t, err)
    after, err := d.IssueByID(ctx, issue.ID)
    require.NoError(t, err)
    assert.Equal(t, "local wins", after.Title)
}

func TestImportBatch_ReimportAddsMissingCommentByMapping(t *testing.T) {
    d, ctx, p := setupTestProject(t)
    ts := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    item := db.ImportItem{ExternalID: "a", Title: "issue", Body: "body", Author: "alice", Status: "open", CreatedAt: ts, UpdatedAt: ts, Comments: []db.ImportComment{{ExternalID: "c1", Author: "alice", Body: "same", CreatedAt: ts}}}

    _, _, err := d.ImportBatch(ctx, db.ImportBatchParams{ProjectID: p.ID, Source: "beads", Actor: "importer", Items: []db.ImportItem{item}})
    require.NoError(t, err)
    _, _, err = d.ImportBatch(ctx, db.ImportBatchParams{ProjectID: p.ID, Source: "beads", Actor: "importer", Items: []db.ImportItem{item}})
    require.NoError(t, err)

    m, err := d.ImportMappingBySource(ctx, p.ID, "beads", "issue", "a")
    require.NoError(t, err)
    comments := commentsForIssue(t, ctx, d, *m.IssueID)
    assert.Len(t, comments, 1)
}

func commentsForIssue(t *testing.T, ctx context.Context, d *db.DB, issueID int64) []db.Comment {
    t.Helper()
    rows, err := d.QueryContext(ctx, `SELECT id, issue_id, author, body, created_at FROM comments WHERE issue_id = ? ORDER BY id ASC`, issueID)
    require.NoError(t, err)
    defer func() { _ = rows.Close() }()
    var out []db.Comment
    for rows.Next() {
        var c db.Comment
        require.NoError(t, rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Body, &c.CreatedAt))
        out = append(out, c)
    }
    require.NoError(t, rows.Err())
    return out
}

func strPtr(s string) *string { return &s }
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/db -run TestImportBatch -count=1
```

Expected: FAIL because `ImportBatch` types and methods do not exist.

- [ ] **Step 3: Implement import types**

Create `internal/db/imports.go` with public types:

```go
package db

import (
    "context"
    "database/sql"
    "encoding/json"
    "errors"
    "fmt"
    "time"

    katauid "github.com/wesm/kata/internal/uid"
)

type ImportBatchParams struct {
    ProjectID int64
    Source    string
    Actor     string
    Items     []ImportItem
}

type ImportItem struct {
    ExternalID   string
    Title        string
    Body         string
    Author       string
    Owner        *string
    Status       string
    ClosedReason *string
    CreatedAt    time.Time
    UpdatedAt    time.Time
    ClosedAt     *time.Time
    Labels       []string
    Comments     []ImportComment
    Links        []ImportLink
}

type ImportComment struct {
    ExternalID string
    Author     string
    Body       string
    CreatedAt  time.Time
}

type ImportLink struct {
    Type             string
    TargetExternalID string
}

type ImportBatchResult struct {
    Source    string             `json:"source"`
    Created   int                `json:"created"`
    Updated   int                `json:"updated"`
    Unchanged int                `json:"unchanged"`
    Comments  int                `json:"comments"`
    Links     int                `json:"links"`
    Items     []ImportItemResult `json:"items"`
}

type ImportItemResult struct {
    ExternalID  string `json:"external_id"`
    IssueNumber int64  `json:"issue_number"`
    Status      string `json:"status"`
    Reason      string `json:"reason,omitempty"`
}

var ErrImportValidation = errors.New("invalid import")
```

- [ ] **Step 4: Implement validation and transaction skeleton**

Add `ImportBatch`:

```go
func (d *DB) ImportBatch(ctx context.Context, p ImportBatchParams) (ImportBatchResult, []Event, error) {
    if p.Source == "" || p.Actor == "" {
        return ImportBatchResult{}, nil, fmt.Errorf("%w: source and actor are required", ErrImportValidation)
    }
    for _, item := range p.Items {
        if item.ExternalID == "" || item.Title == "" || item.Author == "" {
            return ImportBatchResult{}, nil, fmt.Errorf("%w: external_id, title, and author are required", ErrImportValidation)
        }
        if item.Status != "open" && item.Status != "closed" {
            return ImportBatchResult{}, nil, fmt.Errorf("%w: status must be open or closed", ErrImportValidation)
        }
        for _, l := range item.Links {
            if l.Type != "blocks" && l.Type != "parent" && l.Type != "related" {
                return ImportBatchResult{}, nil, fmt.Errorf("%w: link type must be parent|blocks|related", ErrImportValidation)
            }
        }
    }
    tx, err := d.BeginTx(ctx, nil)
    if err != nil { return ImportBatchResult{}, nil, fmt.Errorf("begin import: %w", err) }
    defer func() { _ = tx.Rollback() }()
    result := ImportBatchResult{Source: p.Source}
    events := []Event{}
    if err := tx.Commit(); err != nil { return ImportBatchResult{}, nil, fmt.Errorf("commit import: %w", err) }
    return result, events, nil
}
```

- [ ] **Step 5: Implement issue create/update with explicit timestamps**

Inside `ImportBatch`, allocate issue numbers with the existing `UPDATE projects SET next_issue_number = next_issue_number + 1 WHERE id = ? AND deleted_at IS NULL RETURNING next_issue_number - 1, identity, uid` pattern, insert with explicit timestamps, and update mapped issues only when `item.UpdatedAt.After(existing.UpdatedAt)`.

Core insert statement:

```go
issueUID, err := katauid.New()
if err != nil { return ImportBatchResult{}, nil, fmt.Errorf("generate issue uid: %w", err) }
res, err := tx.ExecContext(ctx, `INSERT INTO issues(uid, project_id, number, title, body, status, closed_reason, owner, author, created_at, updated_at, closed_at)
    VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    issueUID, p.ProjectID, nextNum, item.Title, item.Body, item.Status, item.ClosedReason, item.Owner, item.Author,
    item.CreatedAt, item.UpdatedAt, item.ClosedAt)
```

Core update statement:

```go
_, err = tx.ExecContext(ctx, `UPDATE issues
    SET title=?, body=?, status=?, closed_reason=?, owner=?, updated_at=?, closed_at=?
    WHERE id=?`,
    item.Title, item.Body, item.Status, item.ClosedReason, item.Owner, item.UpdatedAt, item.ClosedAt, existing.ID)
```

Create/update events with `insertEventTx` and payload built from the current import item:

```go
payload, err := json.Marshal(map[string]string{"source": p.Source, "external_id": item.ExternalID})
if err != nil { return ImportBatchResult{}, nil, fmt.Errorf("marshal import event payload: %w", err) }
```

Event `created_at` can be current DB time for normal event ordering; issue/comment/label/link rows must preserve source timestamps.

- [ ] **Step 6: Implement comment merge**

For each comment, look up mapping `(source, comment, comment.ExternalID)`. If absent:

```go
res, err := tx.ExecContext(ctx, `INSERT INTO comments(issue_id, author, body, created_at) VALUES(?, ?, ?, ?)`, issueID, c.Author, c.Body, c.CreatedAt)
```

Then write mapping with `ObjectType: "comment"`. Do not duplicate comments when mapping exists.

- [ ] **Step 7: Implement labels and links with source-owned mappings**

Labels:

- Insert each label with `INSERT OR IGNORE INTO issue_labels(issue_id, label, author, created_at) VALUES(?, ?, ?, ?)`.
- Mapping external ID: `item.ExternalID + ":label:" + label`.
- For source-newer updates, delete source-owned label rows whose mapping exists but label no longer appears in item, then delete those mappings. Leave labels without matching mapping untouched.

Links:

- Resolve `item.Links[].TargetExternalID` through issue mappings.
- DB treats each `ImportItem.Links` entry as `item.ExternalID --type--> TargetExternalID`. For Beads, adapter inverts dependencies before building items so `A depends on B` becomes link on item `B` targeting `A`.
- Insert links with explicit `created_at` equal to item `UpdatedAt` or `CreatedAt`.
- Mapping external ID: `item.ExternalID + ":" + link.Type + ":" + link.TargetExternalID`.
- For source-newer updates, delete source-owned link rows missing from current import item. Leave links without matching mapping untouched.

- [ ] **Step 8: Run DB tests and commit**

Run:

```bash
go test ./internal/db -run 'TestImportBatch|TestImportMapping' -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/db/imports.go internal/db/imports_test.go
git commit -m "feat(db): import external issues"
```

---

### Task 4: Generic daemon import endpoint

**TDD scenario:** New feature — full TDD cycle.

**Files:**
- Modify: `internal/api/types.go`
- Create: `internal/daemon/handlers_imports.go`
- Modify: `internal/daemon/server.go`
- Test: `internal/daemon/handlers_imports_test.go`

- [ ] **Step 1: Write failing endpoint tests**

Create `internal/daemon/handlers_imports_test.go`:

```go
package daemon_test

import (
    "net/http"
    "strconv"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "github.com/wesm/kata/internal/testenv"
)

func TestImportEndpoint_CreatesAndReimports(t *testing.T) {
    env := testenv.New(t)
    pid := createImportTestProject(t, env, "github.com/wesm/kata", "kata").ID
    body := map[string]any{
        "actor": "importer",
        "source": "beads",
        "items": []map[string]any{{
            "external_id": "beads-1",
            "title": "Imported",
            "body": "body",
            "author": "alice",
            "status": "open",
            "created_at": "2026-05-01T10:00:00Z",
            "updated_at": "2026-05-01T10:00:00Z",
            "labels": []string{"source:beads", "beads-id:beads-1"},
            "comments": []map[string]any{{"external_id": "c1", "author": "alice", "body": "comment", "created_at": "2026-05-01T10:01:00Z"}},
        }},
    }
    var out struct { Source string `json:"source"`; Created int `json:"created"`; Comments int `json:"comments"` }
    envPostJSON(t, env, "/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/imports", body, &out)
    assert.Equal(t, "beads", out.Source)
    assert.Equal(t, 1, out.Created)
    assert.Equal(t, 1, out.Comments)

    var second struct { Created int `json:"created"`; Unchanged int `json:"unchanged"`; Comments int `json:"comments"` }
    envPostJSON(t, env, "/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/imports", body, &second)
    assert.Equal(t, 0, second.Created)
    assert.Equal(t, 1, second.Unchanged)
    assert.Equal(t, 0, second.Comments)
}

func TestImportEndpoint_RejectsBlankActor(t *testing.T) {
    env := testenv.New(t)
    pid := createImportTestProject(t, env, "github.com/wesm/kata", "kata").ID
    resp, body := envDoRaw(t, env, http.MethodPost, "/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/imports", map[string]any{"source": "beads"}, nil)
    assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
    assert.Contains(t, string(body), "actor")
}

func createImportTestProject(t *testing.T, env *testenv.Env, identity, name string) db.Project {
    t.Helper()
    p, err := env.DB.CreateProject(context.Background(), identity, name)
    require.NoError(t, err)
    return p
}
```

Add `context` and `github.com/wesm/kata/internal/db` imports to the test file.

- [ ] **Step 2: Run endpoint tests to verify failure**

Run:

```bash
go test ./internal/daemon -run TestImportEndpoint -count=1
```

Expected: FAIL because route/types missing.

- [ ] **Step 3: Add API DTOs**

Append to `internal/api/types.go`:

```go
type ImportRequest struct {
    ProjectID int64 `path:"project_id" required:"true"`
    Body      struct {
        Actor  string             `json:"actor" required:"true"`
        Source string             `json:"source" required:"true"`
        Items  []ImportIssueInput `json:"items"`
    }
}

type ImportIssueInput struct {
    ExternalID   string               `json:"external_id" required:"true"`
    Title        string               `json:"title" required:"true"`
    Body         string               `json:"body,omitempty"`
    Author       string               `json:"author" required:"true"`
    Owner        *string              `json:"owner,omitempty"`
    Status       string               `json:"status" enum:"open,closed"`
    ClosedReason *string              `json:"closed_reason,omitempty" enum:"done,wontfix,duplicate,"`
    CreatedAt    time.Time            `json:"created_at" required:"true"`
    UpdatedAt    time.Time            `json:"updated_at" required:"true"`
    ClosedAt     *time.Time           `json:"closed_at,omitempty"`
    Labels       []string             `json:"labels,omitempty"`
    Comments     []ImportCommentInput `json:"comments,omitempty"`
    Links        []ImportLinkInput    `json:"links,omitempty"`
}

type ImportCommentInput struct {
    ExternalID string    `json:"external_id" required:"true"`
    Author     string    `json:"author" required:"true"`
    Body       string    `json:"body" required:"true"`
    CreatedAt  time.Time `json:"created_at" required:"true"`
}

type ImportLinkInput struct {
    Type             string `json:"type" required:"true" enum:"parent,blocks,related"`
    TargetExternalID string `json:"target_external_id" required:"true"`
}

type ImportResponse struct {
    Body db.ImportBatchResult
}
```

- [ ] **Step 4: Add handler and register**

Create `internal/daemon/handlers_imports.go`:

```go
package daemon

import (
    "context"
    "errors"

    "github.com/danielgtaylor/huma/v2"
    "github.com/wesm/kata/internal/api"
    "github.com/wesm/kata/internal/db"
)

func registerImportsHandlers(humaAPI huma.API, cfg ServerConfig) {
    huma.Register(humaAPI, huma.Operation{OperationID: "importIssues", Method: "POST", Path: "/api/v1/projects/{project_id}/imports"},
        func(ctx context.Context, in *api.ImportRequest) (*api.ImportResponse, error) {
            if err := validateActor(in.Body.Actor); err != nil { return nil, err }
            if _, err := activeProjectByID(ctx, cfg.DB, in.ProjectID); err != nil { return nil, err }
            items := make([]db.ImportItem, 0, len(in.Body.Items))
            for _, src := range in.Body.Items {
                item := db.ImportItem{ExternalID: src.ExternalID, Title: src.Title, Body: src.Body, Author: src.Author, Owner: src.Owner, Status: src.Status, ClosedReason: src.ClosedReason, CreatedAt: src.CreatedAt, UpdatedAt: src.UpdatedAt, ClosedAt: src.ClosedAt, Labels: src.Labels}
                for _, c := range src.Comments { item.Comments = append(item.Comments, db.ImportComment{ExternalID: c.ExternalID, Author: c.Author, Body: c.Body, CreatedAt: c.CreatedAt}) }
                for _, l := range src.Links { item.Links = append(item.Links, db.ImportLink{Type: l.Type, TargetExternalID: l.TargetExternalID}) }
                items = append(items, item)
            }
            res, events, err := cfg.DB.ImportBatch(ctx, db.ImportBatchParams{ProjectID: in.ProjectID, Source: in.Body.Source, Actor: in.Body.Actor, Items: items})
            switch {
            case errors.Is(err, db.ErrImportValidation):
                return nil, api.NewError(400, "validation", err.Error(), "", nil)
            case errors.Is(err, db.ErrNotFound):
                return nil, api.NewError(404, "issue_not_found", err.Error(), "", nil)
            case err != nil:
                return nil, api.NewError(500, "internal", err.Error(), "", nil)
            }
            for _, evt := range events {
                cfg.Broadcaster.Broadcast(StreamMsg{Kind: "event", Event: &evt, ProjectID: in.ProjectID})
                cfg.Hooks.Enqueue(evt)
            }
            out := &api.ImportResponse{}
            out.Body = res
            return out, nil
        })
}
```

Modify `internal/daemon/server.go`:

```go
registerImportsHandlers(humaAPI, cfg)
```

Place it after `registerIssues(humaAPI, cfg)` or after relationship handlers; route path is independent.

- [ ] **Step 5: Run endpoint tests and commit**

Run:

```bash
go test ./internal/daemon -run TestImportEndpoint -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/api/types.go internal/daemon/handlers_imports.go internal/daemon/handlers_imports_test.go internal/daemon/server.go
git commit -m "feat(daemon): add import endpoint"
```

---

### Task 5: Beads adapter parser and mapper

**TDD scenario:** New feature — full TDD cycle.

**Files:**
- Create: `cmd/kata/beads_import.go`
- Test: `cmd/kata/beads_import_test.go`

- [ ] **Step 1: Write adapter tests**

Create `cmd/kata/beads_import_test.go`:

```go
package main

import (
    "strings"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestParseBeadsExportAndBuildImportRequest(t *testing.T) {
    export := strings.NewReader(`{"id":"b1","title":"Blocker","description":"blocker body","status":"open","priority":1,"issue_type":"task","owner":"alice","created_at":"2026-05-01T10:00:00Z","created_by":"Alice","updated_at":"2026-05-01T10:00:00Z","labels":["Needs Review","bad label!"],"dependency_count":0,"dependent_count":1,"comment_count":0}
{"id":"b2","title":"Blocked","description":"blocked body","status":"closed","priority":2,"issue_type":"bug","owner":"bob","created_at":"2026-05-01T11:00:00Z","created_by":"Bob","updated_at":"2026-05-01T12:00:00Z","closed_at":"2026-05-01T12:00:00Z","close_reason":"fixed elsewhere","dependencies":[{"issue_id":"b2","depends_on_id":"b1","type":"blocks","created_at":"2026-05-01T11:30:00Z","created_by":"Bob","metadata":"{}"}],"comment_count":1}
`)
    comments := map[string][]beadsComment{
        "b2": {{ID: "c1", IssueID: "b2", Author: "Bob", Text: "comment", CreatedAt: mustParseTime(t, "2026-05-01T11:45:00Z")}},
    }

    req, err := buildBeadsImportRequest(export, comments, "importer")
    require.NoError(t, err)
    require.Len(t, req.Items, 2)
    assert.Equal(t, "beads", req.Source)
    blocked := req.Items[1]
    assert.Equal(t, "b2", blocked.ExternalID)
    assert.Equal(t, "done", *blocked.ClosedReason)
    assert.Contains(t, blocked.Body, "beads_close_reason: fixed elsewhere")
    assert.Contains(t, blocked.Labels, "source:beads")
    assert.Contains(t, blocked.Labels, "needs-review")
    require.Len(t, blocked.Comments, 1)
    assert.Equal(t, "c1", blocked.Comments[0].ExternalID)
    blocker := req.Items[0]
    require.Len(t, blocker.Links, 1)
    assert.Equal(t, "blocks", blocker.Links[0].Type)
    assert.Equal(t, "b2", blocker.Links[0].TargetExternalID, "Beads A depends on B imports as B blocks A")
}

func TestNormalizeKataLabel(t *testing.T) {
    got := normalizeKataLabel("Needs Review!")
    assert.Equal(t, "needs-review", got)
    long := normalizeKataLabel(strings.Repeat("x", 100))
    assert.LessOrEqual(t, len(long), 64)
}

func mustParseTime(t *testing.T, s string) time.Time {
    t.Helper()
    ts, err := time.Parse(time.RFC3339Nano, s)
    require.NoError(t, err)
    return ts
}
```

Add `time` to the test imports.

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./cmd/kata -run 'TestParseBeadsExportAndBuildImportRequest|TestNormalizeKataLabel' -count=1
```

Expected: FAIL because adapter code is missing.

- [ ] **Step 3: Implement Beads structs and parser**

Create `cmd/kata/beads_import.go` with parser types:

```go
package main

import (
    "bufio"
    "crypto/sha1"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "io"
    "regexp"
    "strings"
    "time"
)

type beadsIssue struct {
    ID           string            `json:"id"`
    Title        string            `json:"title"`
    Description  string            `json:"description"`
    Status       string            `json:"status"`
    Priority     int               `json:"priority"`
    IssueType    string            `json:"issue_type"`
    Owner        string            `json:"owner"`
    CreatedAt    time.Time         `json:"created_at"`
    CreatedBy    string            `json:"created_by"`
    UpdatedAt    time.Time         `json:"updated_at"`
    ClosedAt     *time.Time        `json:"closed_at"`
    CloseReason  string            `json:"close_reason"`
    Labels       []string          `json:"labels"`
    Dependencies []beadsDependency `json:"dependencies"`
    CommentCount int               `json:"comment_count"`
}

type beadsDependency struct {
    IssueID     string `json:"issue_id"`
    DependsOnID string `json:"depends_on_id"`
    Type        string `json:"type"`
}

type beadsComment struct {
    ID        string    `json:"id"`
    IssueID   string    `json:"issue_id"`
    Author    string    `json:"author"`
    Text      string    `json:"text"`
    CreatedAt time.Time `json:"created_at"`
}
```

Add normalized import request structs local to CLI matching API JSON:

```go
type beadsImportRequest struct {
    Actor  string                  `json:"actor"`
    Source string                  `json:"source"`
    Items  []beadsImportIssueInput `json:"items"`
}

type beadsImportIssueInput struct {
    ExternalID   string                    `json:"external_id"`
    Title        string                    `json:"title"`
    Body         string                    `json:"body"`
    Author       string                    `json:"author"`
    Owner        *string                   `json:"owner,omitempty"`
    Status       string                    `json:"status"`
    ClosedReason *string                   `json:"closed_reason,omitempty"`
    CreatedAt    time.Time                 `json:"created_at"`
    UpdatedAt    time.Time                 `json:"updated_at"`
    ClosedAt     *time.Time                `json:"closed_at,omitempty"`
    Labels       []string                  `json:"labels,omitempty"`
    Comments     []beadsImportCommentInput `json:"comments,omitempty"`
    Links        []beadsImportLinkInput    `json:"links,omitempty"`
}

type beadsImportCommentInput struct {
    ExternalID string    `json:"external_id"`
    Author     string    `json:"author"`
    Body       string    `json:"body"`
    CreatedAt  time.Time `json:"created_at"`
}

type beadsImportLinkInput struct {
    Type             string `json:"type"`
    TargetExternalID string `json:"target_external_id"`
}
```

- [ ] **Step 4: Implement mapping helpers**

Add label/footer helpers:

```go
var invalidLabelChar = regexp.MustCompile(`[^a-z0-9._:-]+`)
var repeatedDash = regexp.MustCompile(`-+`)

func normalizeKataLabel(s string) string {
    s = strings.ToLower(strings.TrimSpace(s))
    s = strings.Join(strings.Fields(s), "-")
    s = invalidLabelChar.ReplaceAllString(s, "-")
    s = repeatedDash.ReplaceAllString(s, "-")
    s = strings.Trim(s, "-._:")
    if s == "" { s = "imported" }
    if len(s) <= 64 { return s }
    sum := sha1.Sum([]byte(s))
    suffix := hex.EncodeToString(sum[:])[:8]
    return strings.TrimRight(s[:55], "-._:") + "-" + suffix
}

func beadsIDLabel(id string) string { return "beads-id:" + normalizeKataLabel(id) }

func mapCloseReason(reason string) (mapped string, original string) {
    switch reason {
    case "done", "wontfix", "duplicate":
        return reason, reason
    default:
        return "done", reason
    }
}

func beadsFooter(b beadsIssue) string {
    labels, _ := json.Marshal(b.Labels)
    closedAt := ""
    if b.ClosedAt != nil { closedAt = b.ClosedAt.Format(time.RFC3339Nano) }
    return fmt.Sprintf("\n---\nImported from Beads\nbeads_id: %s\nbeads_type: %s\nbeads_priority: %d\nbeads_original_labels: %s\nbeads_created_at: %s\nbeads_updated_at: %s\nbeads_closed_at: %s\nbeads_close_reason: %s\nbeads_comment_count: %d\n",
        b.ID, b.IssueType, b.Priority, labels, b.CreatedAt.Format(time.RFC3339Nano), b.UpdatedAt.Format(time.RFC3339Nano), closedAt, b.CloseReason, b.CommentCount)
}
```

Add parser and request builder:

```go
func parseBeadsExport(r io.Reader) ([]beadsIssue, error) {
    scanner := bufio.NewScanner(r)
    scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
    var out []beadsIssue
    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        if line == "" { continue }
        var issue beadsIssue
        if err := json.Unmarshal([]byte(line), &issue); err != nil { return nil, fmt.Errorf("decode beads export: %w", err) }
        out = append(out, issue)
    }
    if err := scanner.Err(); err != nil { return nil, fmt.Errorf("scan beads export: %w", err) }
    return out, nil
}

func buildBeadsImportRequest(r io.Reader, comments map[string][]beadsComment, actor string) (beadsImportRequest, error) {
    issues, err := parseBeadsExport(r)
    if err != nil { return beadsImportRequest{}, err }
    req := beadsImportRequest{Actor: actor, Source: "beads"}
    indexByID := map[string]int{}
    for _, b := range issues {
        status := b.Status
        if status == "" { status = "open" }
        if status != "open" && status != "closed" { return beadsImportRequest{}, fmt.Errorf("unsupported beads status %q for %s", status, b.ID) }
        labels := []string{"source:beads", beadsIDLabel(b.ID)}
        for _, l := range b.Labels { labels = append(labels, normalizeKataLabel(l)) }
        var owner *string
        if strings.TrimSpace(b.Owner) != "" { owner = &b.Owner }
        author := b.CreatedBy
        if strings.TrimSpace(author) == "" { author = actor }
        var closedReason *string
        if status == "closed" { mapped, _ := mapCloseReason(b.CloseReason); closedReason = &mapped }
        item := beadsImportIssueInput{ExternalID: b.ID, Title: b.Title, Body: strings.TrimRight(b.Description, "\n") + beadsFooter(b), Author: author, Owner: owner, Status: status, ClosedReason: closedReason, CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt, ClosedAt: b.ClosedAt, Labels: labels}
        for _, c := range comments[b.ID] { item.Comments = append(item.Comments, beadsImportCommentInput{ExternalID: c.ID, Author: c.Author, Body: c.Text, CreatedAt: c.CreatedAt}) }
        req.Items = append(req.Items, item)
        indexByID[b.ID] = len(req.Items) - 1
    }
    for _, b := range issues {
        for _, dep := range b.Dependencies {
            if dep.DependsOnID == "" { continue }
            idx, ok := indexByID[dep.DependsOnID]
            if !ok { return beadsImportRequest{}, fmt.Errorf("beads dependency target %q for %s not found in export", dep.DependsOnID, b.ID) }
            req.Items[idx].Links = append(req.Items[idx].Links, beadsImportLinkInput{Type: "blocks", TargetExternalID: b.ID})
        }
    }
    return req, nil
}
```

- [ ] **Step 5: Run adapter tests and commit**

Run:

```bash
go test ./cmd/kata -run 'TestParseBeadsExportAndBuildImportRequest|TestNormalizeKataLabel' -count=1
```

Expected: PASS.

Commit:

```bash
git add cmd/kata/beads_import.go cmd/kata/beads_import_test.go
git commit -m "feat(cli): map beads export to import request"
```

---

### Task 6: CLI Beads import command integration

**TDD scenario:** New feature — full TDD cycle.

**Files:**
- Modify: `cmd/kata/import.go`
- Modify: `cmd/kata/beads_import.go`
- Test: `cmd/kata/beads_import_test.go`

- [ ] **Step 1: Write CLI tests for flag compatibility and fake bd flow**

Append to `cmd/kata/beads_import_test.go`:

```go
func TestImportBeadsRejectsInputAndTarget(t *testing.T) {
    _, err := runCmdOutput(t, nil, "import", "--format", "beads", "--input", "x.jsonl")
    ce := requireCLIError(t, err, ExitValidation)
    assert.Contains(t, ce.Message, "--input is not supported with --format beads")

    _, err = runCmdOutput(t, nil, "import", "--format", "beads", "--target", "target.db")
    ce = requireCLIError(t, err, ExitValidation)
    assert.Contains(t, ce.Message, "--target is not supported with --format beads")
}

func TestImportBeadsRequiresInitInUnattendedMode(t *testing.T) {
    env := testenv.New(t)
    dir := t.TempDir()
    out, err := runCLICapture(t, env, dir, "--json", "import", "--format", "beads")
    assert.Empty(t, out)
    ce := requireCLIError(t, err, ExitValidation)
    assert.Contains(t, ce.Message, "run kata init first")
}

func TestImportBeadsFakeBD(t *testing.T) {
    env := testenv.New(t)
    dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
    fakeDir := t.TempDir()
    writeFakeBD(t, fakeDir)
    t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

    out := runCLI(t, env, dir, "import", "--format", "beads")
    assert.Contains(t, out, "imported beads: created 1")
    show := runCLI(t, env, dir, "--json", "show", "1")
    assert.Contains(t, show, "Imported from Beads")
    assert.Contains(t, show, "beads comment")
}
```

Add fake bd helper:

```go
func writeFakeBD(t *testing.T, dir string) {
    t.Helper()
    path := filepath.Join(dir, "bd")
    script := `#!/bin/sh
set -eu
if [ "$1" = "export" ]; then
  printf '%s\n' '{"id":"b1","title":"Fake Bead","description":"body","status":"open","priority":1,"issue_type":"task","owner":"alice","created_at":"2026-05-01T10:00:00Z","created_by":"Alice","updated_at":"2026-05-01T10:00:00Z","labels":["migration"],"comment_count":1}'
  exit 0
fi
if [ "$1" = "comments" ]; then
  printf '%s\n' '[{"id":"c1","issue_id":"b1","author":"Alice","text":"beads comment","created_at":"2026-05-01T10:05:00Z"}]'
  exit 0
fi
echo "unexpected bd args: $*" >&2
exit 2
`
    require.NoError(t, os.WriteFile(path, []byte(script), 0o700))
}
```

- [ ] **Step 2: Run CLI tests to verify failure**

Run:

```bash
go test ./cmd/kata -run 'TestImportBeadsRejects|TestImportBeadsRequiresInit|TestImportBeadsFakeBD' -count=1
```

Expected: FAIL because flags/runner not implemented.

- [ ] **Step 3: Add `--format` dispatch without breaking kata JSONL**

Modify `cmd/kata/import.go`:

```go
var format string
```

At start of `RunE`:

```go
if format == "" || format == "kata" {
    return runKataJSONLImport(cmd, input, target, force, newInstance)
}
if format == "beads" {
    return runBeadsImport(cmd, input, target, force, newInstance)
}
return &cliError{Message: "unsupported import format " + format, Kind: kindValidation, ExitCode: ExitValidation}
```

Move the existing JSONL import `RunE` body unchanged into `runKataJSONLImport`. Keep the current validations: require `--input`, require `--target`, refuse running daemon, protect existing target unless `--force`, open input, create target DB, call `jsonl.ImportWithOptions`, and print `imported <target>`.

Use this signature:

```go
func runKataJSONLImport(cmd *cobra.Command, input, target string, force, newInstance bool) error
```

Add flag:

```go
cmd.Flags().StringVar(&format, "format", "kata", "import format: kata|beads")
```

- [ ] **Step 4: Implement shell-out and POST**

In `cmd/kata/beads_import.go`, add:

```go
func runBeadsImport(cmd *cobra.Command, input, target string, force, newInstance bool) error {
    if input != "" { return &cliError{Message: "--input is not supported with --format beads", Kind: kindValidation, ExitCode: ExitValidation} }
    if target != "" { return &cliError{Message: "--target is not supported with --format beads", Kind: kindValidation, ExitCode: ExitValidation} }
    if force { return &cliError{Message: "--force is not supported with --format beads", Kind: kindValidation, ExitCode: ExitValidation} }
    if newInstance { return &cliError{Message: "--new-instance is not supported with --format beads", Kind: kindValidation, ExitCode: ExitValidation} }
    start, err := resolveStartPath(flags.Workspace)
    if err != nil { return err }
    baseURL, err := ensureDaemon(cmd.Context())
    if err != nil { return err }
    pid, err := resolveProjectID(cmd.Context(), baseURL, start)
    if err != nil { return maybePromptInitForBeads(cmd, baseURL, start, err) }
    actor, _ := resolveActor(flags.As, nil)
    req, err := collectBeadsImport(cmd.Context(), start, actor)
    if err != nil { return err }
    client, err := httpClientFor(cmd.Context(), baseURL)
    if err != nil { return err }
    status, bs, err := httpDoJSON(cmd.Context(), client, http.MethodPost, fmt.Sprintf("%s/api/v1/projects/%d/imports", baseURL, pid), req)
    if err != nil { return err }
    if status >= 400 { return apiErrFromBody(status, bs) }
    if flags.JSON { _, err = fmt.Fprintln(cmd.OutOrStdout(), string(bs)); return err }
    var res struct {
        Created   int `json:"created"`
        Updated   int `json:"updated"`
        Unchanged int `json:"unchanged"`
        Comments  int `json:"comments"`
        Links     int `json:"links"`
    }
    if err := json.Unmarshal(bs, &res); err != nil { return err }
    if !flags.Quiet { _, err = fmt.Fprintf(cmd.OutOrStdout(), "imported beads: created %d, updated %d, unchanged %d, comments %d, links %d\n", res.Created, res.Updated, res.Unchanged, res.Comments, res.Links) }
    return err
}
```

Implement `collectBeadsImport` with `exec.CommandContext`:

```go
func collectBeadsImport(ctx context.Context, dir, actor string) (beadsImportRequest, error) {
    if _, err := exec.LookPath("bd"); err != nil {
        return beadsImportRequest{}, &cliError{Message: "bd not found; install Beads or add bd to PATH", Kind: kindValidation, ExitCode: ExitValidation}
    }
    exportCmd := exec.CommandContext(ctx, "bd", "export", "--no-memories")
    exportCmd.Dir = dir
    exportOut, err := exportCmd.Output()
    if err != nil { return beadsImportRequest{}, beadsCommandError("bd export --no-memories", err) }
    issues, err := parseBeadsExport(bytes.NewReader(exportOut))
    if err != nil { return beadsImportRequest{}, err }
    comments := map[string][]beadsComment{}
    for _, issue := range issues {
        ccmd := exec.CommandContext(ctx, "bd", "comments", issue.ID, "--json")
        ccmd.Dir = dir
        out, err := ccmd.Output()
        if err != nil { return beadsImportRequest{}, beadsCommandError("bd comments "+issue.ID+" --json", err) }
        var got []beadsComment
        if err := json.Unmarshal(out, &got); err != nil { return beadsImportRequest{}, fmt.Errorf("decode bd comments %s: %w", issue.ID, err) }
        comments[issue.ID] = got
    }
    return buildBeadsImportRequest(bytes.NewReader(exportOut), comments, actor)
}
```

Implement `maybePromptInitForBeads` minimally:

- If `flags.JSON || flags.Quiet || !isTTY(os.Stdin) || !isTTY(os.Stdout)`, return `run kata init first` validation.
- Otherwise prompt `No kata project found. Run kata init now? [y/N]`.
- On yes, call `callInit(cmd.Context(), baseURL, start, callInitOpts{})`, print init output unless quiet, then return sentinel retry error or rerun Beads import by calling `runBeadsImport(cmd, "", "", false, false)`.

- [ ] **Step 5: Run CLI tests and commit**

Run:

```bash
go test ./cmd/kata -run 'TestImportBeads|TestImportCreatesTargetDB|TestImportRejectsExistingTargetWithoutForce' -count=1
```

Expected: PASS.

Commit:

```bash
git add cmd/kata/import.go cmd/kata/beads_import.go cmd/kata/beads_import_test.go
git commit -m "feat(cli): import live beads workspace"
```

---

### Task 7: Full verification and cleanup

**TDD scenario:** Verification/cleanup.

**Files:**
- Modify tests only if failures reveal real gaps.

- [ ] **Step 1: Run focused package suites**

Run:

```bash
go test ./internal/db ./internal/jsonl ./internal/daemon ./cmd/kata -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full test suite**

Run:

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Inspect git diff for accidental scope creep**

Run:

```bash
git status --short
git diff --stat origin/main...HEAD
git diff --check
```

Expected:
- Only import-related files changed.
- `git diff --check` has no output.

- [ ] **Step 4: Commit final fixes if needed**

If Step 1 or Step 2 required fixes, commit them:

```bash
git add <fixed-files>
git commit -m "test: cover beads import integration"
```

If no fixes were needed, do not create an empty commit.

---

## Self-Review Notes

Spec coverage:

- CLI shape and existing kata JSONL compatibility: Task 6.
- Live Beads shell-out and no `--input`: Task 6.
- Generic daemon endpoint: Task 4.
- `import_mappings`: Tasks 1 and 2.
- Upsert by timestamps: Task 3.
- Comments by Beads comment ID: Tasks 3, 5, 6.
- Dependency direction `B --blocks--> A`: Task 5 adapter mapping and Task 3 DB link insertion.
- Label normalization and metadata labels/footer: Task 5.
- Close reason mapping: Task 5.
- JSONL export/import preserves mappings: Task 2.
- Tests at adapter/daemon/CLI/DB layers: Tasks 1 through 7.

No placeholder steps remain; each task has file paths, test commands, expected result, and commit command.
