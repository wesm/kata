# Cutover wedge on pre-existing FK orphans (issue #43) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop `kata daemon` cutover from wedging on pre-existing
foreign-key orphans. Add a source-DB classifier, scrub orphan
events at export, surface counts on success, and produce
actionable detail on the rare hard-fail path.

**Architecture:** Three-layer fix in `internal/jsonl/`. (1) New
`preflightSourceFKs` classifies source-DB FK violations into known
classes (events/comments/links/issue_labels orphans on issues) and
unknown classes (everything else); halts cutover on unknown. (2)
`exportEvents` (and V1/V2/V3 variants) scrubs orphan `events.issue_id`
rows at export, broadens the existing `related_issue_id` NULL-scrub
to all event types whose peer is missing entirely. (3) Importer's
`validateBeforeCommit` rewrites its FK error to per-row detail with
column resolution, as defense-in-depth.

**Tech Stack:** Go, `database/sql` with `modernc.org/sqlite`,
existing `internal/jsonl` package. Tests use `testify/require` +
`testify/assert`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-05-15-issue-43-cutover-fk-orphans-design.md`

---

## File structure

| File                                          | Status | Responsibility                                                                          |
|-----------------------------------------------|--------|------------------------------------------------------------------------------------------|
| `internal/jsonl/fkmeta.go`                    | NEW    | `fkColumnResolver` — caches `PRAGMA foreign_key_list` lookups so both preflight and importer can map fkid → column name |
| `internal/jsonl/preflight.go`                 | NEW    | `OrphanReport`, `FKViolation`, `preflightSourceFKs(ctx, path)` — read-only classifier  |
| `internal/jsonl/cutover.go`                   | EDIT   | `AutoCutover` calls `preflightSourceFKs`, halts on `UnknownViolations`, prints stderr summary on success |
| `internal/jsonl/export.go`                    | EDIT   | All four `exportEvents` variants (V1/V2/V3/V8+) gain `subject_issue` join + filter + broadened `related_issue_id` scrub. `eventExportWhere` refactored to return `[]string` clauses |
| `internal/jsonl/import.go`                    | EDIT   | `validateBeforeCommit` rewritten to scan all FK violations, resolve columns, format per-row with cap |
| `internal/jsonl/import_test.go`               | EDIT   | Existing edit at line 168 already asserts the new format. Add multi-row test |
| `internal/jsonl/preflight_test.go`            | NEW    | `TestPreflightSourceFKs_DeduplicatesPerRow`                                              |
| `internal/jsonl/cutover_test.go`              | EDIT   | Three new cutover-level tests (drops-all-classes, halts-on-unknown, prints-summary)     |
| `internal/jsonl/fixtures_test.go`             | EDIT   | Add `seedV3DBWithOrphans(t, path, orphanSpec)` helper used by the new cutover tests     |

**File-size sanity check.** `internal/jsonl/cutover.go` today is
~125 lines. Adding the preflight call, halt path, and stderr
summary adds maybe 25 lines — still well under any limit. The new
`preflight.go` and `fkmeta.go` are kept separate from `cutover.go`
because the column resolver is shared with `import.go` and the
preflight types may grow independently.

---

## Task ordering

```
Task 1 (importer diagnostic)  ─┐
                               ├─→ Task 4 (AutoCutover wiring)
Task 2 (preflight)  ───────────┤
                               │
Task 3 (events export scrub)  ─┘
                                    └─→ Task 5 (stderr summary + integration)
                                              └─→ Task 6 (final verification)
```

Tasks 1-3 are mutually independent and can be implemented in any
order. Task 4 depends on Task 2. Task 5 depends on Tasks 2, 3, 4.
Task 6 is final verification and depends on everything.

---

## Task 1: Rewrite `validateBeforeCommit` with detailed FK violation output

**Files:**
- Create: `internal/jsonl/fkmeta.go`
- Modify: `internal/jsonl/import.go:704-732`
- Test: `internal/jsonl/import_test.go` — existing edit at line 168 + new multi-row test

The existing test edit at `internal/jsonl/import_test.go:168` is
already a failing test asserting the substring
`"project_aliases rowid=1 parent=projects"`. We make it pass and
add a multi-row sibling. (The spec's working name for this multi-
row test is `TestValidateBeforeCommit_GroupsAndFormats`; the
implementation uses
`TestImportRejectsForeignKeyViolationsAcrossMultipleTables` to
match the existing `TestImportRejects*` codebase convention. Same
intent, different name.)

- [ ] **Step 1: Read current `validateBeforeCommit` for reference**

Read `internal/jsonl/import.go` lines 704-732 to confirm the
existing shape (single-row error, `defer rows.Close()`,
integrity_check follow-up). The integrity_check section stays
unchanged.

- [ ] **Step 2: Add the multi-row test BEFORE implementation**

Append this test in `internal/jsonl/import_test.go` immediately
after `TestImportRejectsForeignKeyViolationBeforeCommit`:

```go
func TestImportRejectsForeignKeyViolationsAcrossMultipleTables(t *testing.T) {
	ctx := context.Background()
	target := openImportTargetDB(t)

	// Two violations on different tables: a project_alias pointing
	// at a missing project, and a project_alias whose project_id
	// references a different missing project. Both rows are
	// rejected and the error must group/list both.
	err := importJSONL(ctx, target,
		validExportVersion,
		`{"kind":"project_alias","data":{"id":1,"project_id":777,"alias_identity":"missing-a","alias_kind":"git","root_path":"/tmp/a","created_at":"2026-05-03T00:00:00.000Z","last_seen_at":"2026-05-03T00:00:00.000Z"}}`,
		`{"kind":"project_alias","data":{"id":2,"project_id":888,"alias_identity":"missing-b","alias_kind":"git","root_path":"/tmp/b","created_at":"2026-05-03T00:00:00.000Z","last_seen_at":"2026-05-03T00:00:00.000Z"}}`,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "foreign_key_check")
	assert.Contains(t, err.Error(), "project_aliases rowid=1 parent=projects column=project_id")
	assert.Contains(t, err.Error(), "project_aliases rowid=2 parent=projects column=project_id")
	var count int
	require.NoError(t, target.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_aliases`).Scan(&count))
	assert.Equal(t, 0, count)
}
```

- [ ] **Step 3: Run both tests to verify they fail**

Run: `go test ./internal/jsonl/ -run 'TestImportRejectsForeignKeyViolation' -v`

Expected: both fail. The existing test fails on the
`"project_aliases rowid=1 parent=projects"` substring; the new
test fails on the same substring + the multi-row substring.

- [ ] **Step 4: Create `internal/jsonl/fkmeta.go`**

Create the file with the column resolver. Both preflight (Task 2)
and the importer (this task) consume it.

```go
package jsonl

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
)

// fkColumnQuerier abstracts what fkColumnResolver needs from either
// a *sql.DB or a *sql.Tx so the resolver can be reused at both
// import-time (transaction) and preflight-time (read-only DB).
type fkColumnQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// fkColumnResolver caches PRAGMA foreign_key_list lookups so each
// child table is queried at most once per resolver lifetime.
// foreign_key_check returns one row per violation with an `fkid`
// column that is the index into foreign_key_list(<table>) — the
// resolver maps that pair to the human column name.
type fkColumnResolver struct {
	q     fkColumnQuerier
	cache map[string]map[int]string
}

func newFKColumnResolver(q fkColumnQuerier) *fkColumnResolver {
	return &fkColumnResolver{q: q, cache: map[string]map[int]string{}}
}

// SQLite identifier names from foreign_key_check are sourced from
// schema metadata, but we still validate before interpolating into
// the PRAGMA call to avoid relying on caller hygiene.
var safeIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// resolve returns the FK column name in `table` for the constraint
// at index `fkid` (the value foreign_key_check returns in its 4th
// column). Returns "" + nil if the FK has no seq=0 entry — caller
// should treat that as "unknown column" and fall back to "?".
func (r *fkColumnResolver) resolve(ctx context.Context, table string, fkid int) (string, error) {
	if cached, ok := r.cache[table]; ok {
		col, ok := cached[fkid]
		if !ok {
			return "", nil
		}
		return col, nil
	}
	if !safeIdent.MatchString(table) {
		return "", fmt.Errorf("fkColumnResolver: unsafe table name %q", table)
	}
	rows, err := r.q.QueryContext(ctx, fmt.Sprintf(`PRAGMA foreign_key_list(%q)`, table))
	if err != nil {
		return "", fmt.Errorf("foreign_key_list(%s): %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	perTable := map[int]string{}
	for rows.Next() {
		var (
			id, seq                                                        int
			parentTable, fromCol, toCol, onUpdate, onDelete, matchType     string
		)
		if err := rows.Scan(&id, &seq, &parentTable, &fromCol, &toCol, &onUpdate, &onDelete, &matchType); err != nil {
			return "", fmt.Errorf("scan foreign_key_list(%s): %w", table, err)
		}
		// Only record seq=0 — composite FKs (multi-column) would
		// have additional rows with the same id, but our schema
		// has no composite FKs and surfacing only the first column
		// is the right behavior even if one ever appears.
		if seq == 0 {
			perTable[id] = fromCol
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("foreign_key_list(%s) rows: %w", table, err)
	}
	r.cache[table] = perTable
	if col, ok := perTable[fkid]; ok {
		return col, nil
	}
	return "", nil
}
```

- [ ] **Step 5: Rewrite `validateBeforeCommit` in `import.go`**

Replace the function body at `internal/jsonl/import.go:704-732`
with the version below. The integrity_check section stays the same;
only the FK section changes.

```go
func validateBeforeCommit(ctx context.Context, tx *sql.Tx) error {
	if err := checkForeignKeyViolations(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			return fmt.Errorf("integrity_check scan: %w", err)
		}
		if !strings.EqualFold(msg, "ok") {
			return fmt.Errorf("integrity_check: %s", msg)
		}
	}
	return rows.Err()
}

// checkForeignKeyViolations runs PRAGMA foreign_key_check, scans every
// returned row, resolves each violated FK to its column name, and
// returns a single error grouping per-row detail when at least one
// violation exists. Output is capped at 20 rows per child table to
// bound log size on widely-corrupted DBs.
func checkForeignKeyViolations(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	type viol struct {
		Table       string
		RowID       sql.NullInt64
		ParentTable string
		FKID        int
	}
	var all []viol
	for rows.Next() {
		var v viol
		if err := rows.Scan(&v.Table, &v.RowID, &v.ParentTable, &v.FKID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("foreign_key_check scan: %w", err)
		}
		all = append(all, v)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("foreign_key_check rows: %w", err)
	}
	_ = rows.Close()
	if len(all) == 0 {
		return nil
	}
	resolver := newFKColumnResolver(tx)
	var sb strings.Builder
	fmt.Fprintf(&sb, "foreign_key_check: %d violations:", len(all))
	perTable := map[string]int{}
	for _, v := range all {
		if perTable[v.Table] >= 20 {
			continue
		}
		perTable[v.Table]++
		col, _ := resolver.resolve(ctx, v.Table, v.FKID)
		if col == "" {
			col = "?"
		}
		rowidStr := "?"
		if v.RowID.Valid {
			rowidStr = fmt.Sprintf("%d", v.RowID.Int64)
		}
		fmt.Fprintf(&sb, "\n  %s rowid=%s parent=%s column=%s", v.Table, rowidStr, v.ParentTable, col)
	}
	return errors.New(sb.String())
}
```

- [ ] **Step 6: Verify import.go imports**

Confirm `import.go` already imports `errors`, `fmt`, `strings`,
`database/sql`. If `errors` is missing, add it.

Run: `goimports -w internal/jsonl/import.go internal/jsonl/fkmeta.go`

- [ ] **Step 7: Run both tests to verify they pass**

Run: `go test ./internal/jsonl/ -run 'TestImportRejectsForeignKeyViolation' -v`

Expected: both pass.

- [ ] **Step 8: Run the full jsonl package to check nothing regressed**

Run: `go test ./internal/jsonl/ -count=1`

Expected: all tests pass.

- [ ] **Step 9: Commit**

```bash
git add internal/jsonl/fkmeta.go internal/jsonl/import.go internal/jsonl/import_test.go
git commit -m "$(cat <<'EOF'
jsonl: detail per-row FK violations in importer error

PRAGMA foreign_key_check returns table/rowid/parent/fkid for every
violation; surface them all instead of a single generic line.
Resolve fkid to the column name via PRAGMA foreign_key_list with
caching so widely-corrupted DBs don't hammer the metadata. Cap
per-table output at 20 rows.
EOF
)"
```

---

## Task 2: Add `OrphanReport` types and `preflightSourceFKs`

**Files:**
- Create: `internal/jsonl/preflight.go`
- Create: `internal/jsonl/preflight_test.go`
- Modify: `internal/jsonl/fixtures_test.go` (add `seedV3DBWithOrphans` helper)

Implements the spec's classifier. Read-only against the source DB.
Drops/scrubs are recorded as `(table → set of rowids)` so a single
child row with multiple violated FKs counts once.

- [ ] **Step 1: Add the `seedV3DBWithOrphans` helper to `fixtures_test.go`**

Append at the end of `internal/jsonl/fixtures_test.go`:

```go
// orphanSpec describes the orphan rows seedV3DBWithOrphans should
// inject after the valid baseline rows. All counts default to 0.
type orphanSpec struct {
	OrphanComments      int  // comments referencing missing issue_id
	OrphanLinks         int  // links with one valid endpoint and one missing
	OrphanLinkBothEnds  int  // links with BOTH endpoints missing (dedup test)
	OrphanIssueLabels   int  // issue_labels referencing missing issue_id
	OrphanEventIssueID  int  // events with missing issue_id (valid related)
	OrphanEventRelated  int  // events with valid issue_id, missing related
	OrphanEventBoth     int  // events with BOTH columns missing (drop-precedence)
	OrphanProjectAlias  bool // single project_aliases row with missing project_id
}

// seedV3DBWithOrphans writes a v3-schema DB at path containing 1
// project, 3 valid issues, plus the orphan rows requested by spec.
// Orphans reference the placeholder issue ID 999 (or 998/997 for
// the second/third missing endpoint), which is never inserted.
// PRAGMA foreign_keys=OFF is used so the inserts succeed and the
// post-cutover preflight then sees them.
func seedV3DBWithOrphans(t *testing.T, path string, spec orphanSpec) {
	t.Helper()
	writeLegacyV3DB(t, path)
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()

	// Baseline: 1 project, 3 issues. Use deterministic ULID-style
	// uids so test assertions can name them if needed.
	_, err = raw.Exec(`PRAGMA foreign_keys = OFF`)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO projects (id, uid, identity, name) VALUES
		(1, '01HZZZZZZZZZZZZZZZZZZZZZ01', 'github.com/wesm/kata', 'kata')`)
	require.NoError(t, err)
	for i := 1; i <= 3; i++ {
		_, err = raw.Exec(`INSERT INTO issues (id, uid, project_id, number, title, author)
			VALUES (?, ?, 1, ?, ?, 'tester')`,
			i,
			fmt.Sprintf("01HZZZZZZZZZZZZZZZZZZZZI%02d", i),
			i,
			fmt.Sprintf("issue %d", i),
		)
		require.NoError(t, err)
	}

	for i := 0; i < spec.OrphanComments; i++ {
		_, err = raw.Exec(`INSERT INTO comments (issue_id, author, body) VALUES (999, 'tester', ?)`,
			fmt.Sprintf("orphan comment %d", i))
		require.NoError(t, err)
	}
	for i := 0; i < spec.OrphanLinks; i++ {
		_, err = raw.Exec(`INSERT INTO links (project_id, from_issue_id, to_issue_id, from_issue_uid, to_issue_uid, type, author)
			VALUES (1, 1, 999, '01HZZZZZZZZZZZZZZZZZZZZI01', '01HZZZZZZZZZZZZZZZZZZZZI99', 'related', 'tester')`)
		require.NoError(t, err)
	}
	for i := 0; i < spec.OrphanLinkBothEnds; i++ {
		_, err = raw.Exec(`INSERT INTO links (project_id, from_issue_id, to_issue_id, from_issue_uid, to_issue_uid, type, author)
			VALUES (1, 998, 999, '01HZZZZZZZZZZZZZZZZZZZZI98', '01HZZZZZZZZZZZZZZZZZZZZI99', 'related', 'tester')`)
		require.NoError(t, err)
	}
	for i := 0; i < spec.OrphanIssueLabels; i++ {
		_, err = raw.Exec(`INSERT INTO issue_labels (issue_id, label, author) VALUES (999, ?, 'tester')`,
			fmt.Sprintf("orphan-%d", i))
		require.NoError(t, err)
	}
	for i := 0; i < spec.OrphanEventIssueID; i++ {
		_, err = raw.Exec(`INSERT INTO events (uid, origin_instance_uid, project_id, project_identity, issue_id, type, actor)
			VALUES (?, '01HZZZZZZZZZZZZZZZZZZZZZ00', 1, 'github.com/wesm/kata', 999, 'issue.created', 'tester')`,
			fmt.Sprintf("01HZZZZZZZZZZZZZZZZEVOI%02d", i))
		require.NoError(t, err)
	}
	for i := 0; i < spec.OrphanEventRelated; i++ {
		_, err = raw.Exec(`INSERT INTO events (uid, origin_instance_uid, project_id, project_identity, issue_id, related_issue_id, type, actor)
			VALUES (?, '01HZZZZZZZZZZZZZZZZZZZZZ00', 1, 'github.com/wesm/kata', 1, 999, 'issue.linked', 'tester')`,
			fmt.Sprintf("01HZZZZZZZZZZZZZZZZEVRE%02d", i))
		require.NoError(t, err)
	}
	for i := 0; i < spec.OrphanEventBoth; i++ {
		_, err = raw.Exec(`INSERT INTO events (uid, origin_instance_uid, project_id, project_identity, issue_id, related_issue_id, type, actor)
			VALUES (?, '01HZZZZZZZZZZZZZZZZZZZZZ00', 1, 'github.com/wesm/kata', 998, 999, 'issue.linked', 'tester')`,
			fmt.Sprintf("01HZZZZZZZZZZZZZZZZEVBO%02d", i))
		require.NoError(t, err)
	}
	if spec.OrphanProjectAlias {
		_, err = raw.Exec(`INSERT INTO project_aliases (project_id, alias_identity, alias_kind, root_path)
			VALUES (777, 'github.com/wesm/missing', 'git', '/tmp/missing')`)
		require.NoError(t, err)
	}
	_, err = raw.Exec(`PRAGMA foreign_keys = ON`)
	require.NoError(t, err)
}
```

- [ ] **Step 2: Write the failing preflight test BEFORE implementing**

Create `internal/jsonl/preflight_test.go`:

```go
package jsonl_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/jsonl"
)

// TestPreflightSourceFKs_DeduplicatesPerRow verifies the rowid-set
// semantics: a links row with both endpoints missing shows up
// twice in foreign_key_check but counts as one drop, and an events
// row with both issue_id and related_issue_id missing counts as
// one drop with no scrub (drop precedence).
func TestPreflightSourceFKs_DeduplicatesPerRow(t *testing.T) {
	ctx := context.Background()

	t.Run("links both endpoints orphan + events both columns orphan", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "kata.db")
		seedV3DBWithOrphans(t, path, orphanSpec{
			OrphanLinkBothEnds: 1,
			OrphanEventBoth:    1,
		})

		report, err := jsonl.PreflightSourceFKs(ctx, path)
		require.NoError(t, err)
		assert.Equal(t, 1, len(report.DroppedRowsByTable["links"]),
			"links row with two violated FKs should count once")
		assert.Equal(t, 1, len(report.DroppedRowsByTable["events"]),
			"events row with both columns orphaned should count as one drop")
		assert.Equal(t, 0, len(report.ScrubbedRowsByTable["events"]),
			"drop precedence: same events rowid must NOT also appear in scrub bucket")
		assert.Empty(t, report.UnknownViolations)
	})

	t.Run("events related-only orphan goes to scrub bucket", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "kata.db")
		seedV3DBWithOrphans(t, path, orphanSpec{
			OrphanEventRelated: 1,
		})

		report, err := jsonl.PreflightSourceFKs(ctx, path)
		require.NoError(t, err)
		assert.Equal(t, 0, len(report.DroppedRowsByTable["events"]),
			"events with valid issue_id but orphan related must NOT be dropped")
		assert.Equal(t, 1, len(report.ScrubbedRowsByTable["events"]),
			"events with orphan related_issue_id should be scrubbed (preserved with NULL related)")
		assert.Empty(t, report.UnknownViolations)
	})

	t.Run("unknown class returns UnknownViolations", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "kata.db")
		seedV3DBWithOrphans(t, path, orphanSpec{
			OrphanProjectAlias: true,
		})

		report, err := jsonl.PreflightSourceFKs(ctx, path)
		require.NoError(t, err)
		require.Len(t, report.UnknownViolations, 1)
		assert.Equal(t, "project_aliases", report.UnknownViolations[0].Table)
		assert.Equal(t, "projects", report.UnknownViolations[0].ParentTable)
		assert.Equal(t, "project_id", report.UnknownViolations[0].Column)
	})

	t.Run("clean DB returns empty report", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "kata.db")
		seedV3DBWithOrphans(t, path, orphanSpec{})

		report, err := jsonl.PreflightSourceFKs(ctx, path)
		require.NoError(t, err)
		assert.Empty(t, report.DroppedRowsByTable)
		assert.Empty(t, report.ScrubbedRowsByTable)
		assert.Empty(t, report.UnknownViolations)
	})
}
```

- [ ] **Step 3: Run the test and confirm it fails**

Run: `go test ./internal/jsonl/ -run TestPreflightSourceFKs -v`

Expected: build error — `jsonl.PreflightSourceFKs` undefined.

- [ ] **Step 4: Create `internal/jsonl/preflight.go`**

```go
package jsonl

import (
	"context"
	"fmt"

	"github.com/wesm/kata/internal/db"
)

// OrphanReport is the result of preflighting a source DB before
// cutover. DroppedRowsByTable and ScrubbedRowsByTable are keyed by
// child-table name; values are the set of rowids in each
// disposition. UnknownViolations is everything that doesn't match
// a known orphan class — a non-empty list halts the cutover.
type OrphanReport struct {
	DroppedRowsByTable  map[string]map[int64]struct{}
	ScrubbedRowsByTable map[string]map[int64]struct{}
	UnknownViolations   []FKViolation
}

// FKViolation is a single PRAGMA foreign_key_check row with the
// fkid resolved to a column name.
type FKViolation struct {
	Table       string
	RowID       int64
	ParentTable string
	Column      string
}

// orphanDisposition captures whether a known-class violation
// causes the row to be dropped at export or merely scrubbed.
type orphanDisposition int

const (
	dispositionUnknown orphanDisposition = iota
	dispositionDrop
	dispositionScrub
)

// classifyKnownOrphan returns dispositionDrop or dispositionScrub
// for known issue-child orphan classes, or dispositionUnknown
// otherwise. Keep this in sync with the disposition table in the
// design doc and with the export-side scrub logic in export.go.
func classifyKnownOrphan(table, parent, column string) orphanDisposition {
	if parent != "issues" {
		return dispositionUnknown
	}
	switch table {
	case "comments":
		if column == "issue_id" {
			return dispositionDrop
		}
	case "links":
		if column == "from_issue_id" || column == "to_issue_id" {
			return dispositionDrop
		}
	case "issue_labels":
		if column == "issue_id" {
			return dispositionDrop
		}
	case "events":
		if column == "issue_id" {
			return dispositionDrop
		}
		if column == "related_issue_id" {
			return dispositionScrub
		}
	}
	return dispositionUnknown
}

// PreflightSourceFKs opens path read-only, runs PRAGMA
// foreign_key_check, classifies each violation against the
// known-orphan-class table, and returns a structured report.
// Drop precedence: when the same rowid appears in both buckets
// during the scan, drop wins (the scrub entry is removed and any
// later scrub entry for that rowid is skipped). The source DB is
// not modified.
func PreflightSourceFKs(ctx context.Context, path string) (OrphanReport, error) {
	source, err := db.OpenReadOnly(ctx, path)
	if err != nil {
		return OrphanReport{}, fmt.Errorf("preflight open: %w", err)
	}
	defer func() { _ = source.Close() }()

	rows, err := source.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return OrphanReport{}, fmt.Errorf("preflight foreign_key_check: %w", err)
	}
	type rawViol struct {
		Table       string
		RowID       int64
		ParentTable string
		FKID        int
	}
	var raws []rawViol
	for rows.Next() {
		var r rawViol
		if err := rows.Scan(&r.Table, &r.RowID, &r.ParentTable, &r.FKID); err != nil {
			_ = rows.Close()
			return OrphanReport{}, fmt.Errorf("preflight scan: %w", err)
		}
		raws = append(raws, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return OrphanReport{}, fmt.Errorf("preflight rows: %w", err)
	}
	_ = rows.Close()

	report := OrphanReport{
		DroppedRowsByTable:  map[string]map[int64]struct{}{},
		ScrubbedRowsByTable: map[string]map[int64]struct{}{},
	}
	resolver := newFKColumnResolver(source)
	for _, r := range raws {
		col, err := resolver.resolve(ctx, r.Table, r.FKID)
		if err != nil {
			return OrphanReport{}, err
		}
		switch classifyKnownOrphan(r.Table, r.ParentTable, col) {
		case dispositionDrop:
			ensureRowSet(report.DroppedRowsByTable, r.Table)[r.RowID] = struct{}{}
			// Drop precedence: remove any earlier scrub entry for
			// this rowid in the same table.
			if scrubs, ok := report.ScrubbedRowsByTable[r.Table]; ok {
				delete(scrubs, r.RowID)
			}
		case dispositionScrub:
			// Drop precedence: skip if this rowid is already in
			// the drop bucket for the same table.
			if drops, ok := report.DroppedRowsByTable[r.Table]; ok {
				if _, present := drops[r.RowID]; present {
					continue
				}
			}
			ensureRowSet(report.ScrubbedRowsByTable, r.Table)[r.RowID] = struct{}{}
		default:
			report.UnknownViolations = append(report.UnknownViolations, FKViolation{
				Table:       r.Table,
				RowID:       r.RowID,
				ParentTable: r.ParentTable,
				Column:      col,
			})
		}
	}
	// Trim empty per-table maps so callers can use len() to gate.
	for tbl, set := range report.DroppedRowsByTable {
		if len(set) == 0 {
			delete(report.DroppedRowsByTable, tbl)
		}
	}
	for tbl, set := range report.ScrubbedRowsByTable {
		if len(set) == 0 {
			delete(report.ScrubbedRowsByTable, tbl)
		}
	}
	return report, nil
}

func ensureRowSet(m map[string]map[int64]struct{}, key string) map[int64]struct{} {
	if existing, ok := m[key]; ok {
		return existing
	}
	fresh := map[int64]struct{}{}
	m[key] = fresh
	return fresh
}
```

- [ ] **Step 5: Run the test and confirm it passes**

Run: `go test ./internal/jsonl/ -run TestPreflightSourceFKs -v`

Expected: all four subtests pass.

- [ ] **Step 6: Run the full jsonl package**

Run: `go test ./internal/jsonl/ -count=1`

Expected: all tests pass (we haven't touched cutover yet).

- [ ] **Step 7: Commit**

```bash
git add internal/jsonl/preflight.go internal/jsonl/preflight_test.go internal/jsonl/fixtures_test.go
git commit -m "$(cat <<'EOF'
jsonl: add PreflightSourceFKs read-only classifier

Classifies source-DB FK violations into known orphan classes
(comments, links, issue_labels, events vs issues) versus unknown.
Tracks rowid sets per table so a single child row with multiple
violated FKs counts once. Applies drop-precedence: if a rowid is
ever marked Dropped, any prior or later Scrubbed entry for that
rowid is removed/skipped.

Source DB is opened read-only and not mutated.
EOF
)"
```

---

## Task 3: Refactor `eventExportWhere` and add events orphan scrub

**Files:**
- Modify: `internal/jsonl/export.go` — refactor `eventExportWhere` into clauses; modify `exportEvents`, `exportEventsV3`, `exportEventsV2`, `exportEventsV1`

The export change is what actually makes cutover succeed. Tests
are at the cutover level (Task 5); this task only verifies the
existing test suite still passes.

- [ ] **Step 1: Refactor `eventExportWhere` to return clauses + args**

Replace `eventExportWhere` at `internal/jsonl/export.go:1142-1171`
with:

```go
func eventExportWhereClauses(opts ExportOptions) ([]string, []any) {
	clauses := []string{}
	args := []any{}
	if opts.ProjectID > 0 {
		clauses = append(clauses, `events.project_id = ?`)
		args = append(args, opts.ProjectID)
	}
	if !opts.IncludeDeleted {
		// See exportEventsV2 / exportEvents commentary for the
		// kata#1 design call: aggregated issue.links_changed events
		// retain related_issue_id pointing at a soft-deleted peer
		// so historical context survives. Per-link issue.linked /
		// issue.unlinked events still drop via related_issue_id.
		clauses = append(clauses,
			`(events.issue_id IS NULL OR EXISTS (SELECT 1 FROM issues WHERE issues.id = events.issue_id AND issues.deleted_at IS NULL))`,
			`(events.related_issue_id IS NULL OR events.type = 'issue.links_changed' OR EXISTS (SELECT 1 FROM issues WHERE issues.id = events.related_issue_id AND issues.deleted_at IS NULL))`,
		)
	}
	return clauses, args
}
```

- [ ] **Step 2: Update all four `exportEvents*` callers to use the new shape**

Each caller currently looks like:

```go
where, args := eventExportWhere(opts)
query += where + ` ORDER BY events.id ASC`
```

Replace with the four-step pattern (orphan filter prepended, then
`whereClause(clauses)` builds the final WHERE):

```go
clauses, args := eventExportWhereClauses(opts)
clauses = append([]string{`(events.issue_id IS NULL OR subject_issue.id IS NOT NULL)`}, clauses...)
query += whereClause(clauses) + ` ORDER BY events.id ASC`
```

This applies at four sites:
- `exportEvents` (>= v8): `internal/jsonl/export.go:666`
- `exportEventsV3`: `internal/jsonl/export.go:724`
- `exportEventsV2`: `internal/jsonl/export.go:786`
- `exportEventsV1`: `internal/jsonl/export.go:836`

- [ ] **Step 3: Add the `subject_issue` LEFT JOIN to all four `exportEvents*` queries**

Each variant builds its query as `FROM events%s LEFT JOIN issues peer ON peer.id = events.related_issue_id`.
Add `LEFT JOIN issues subject_issue ON subject_issue.id = events.issue_id`
right before the `peer` join, in all four functions. The
resulting FROM clause is:

```
FROM events%s
LEFT JOIN issues subject_issue ON subject_issue.id = events.issue_id
LEFT JOIN issues peer ON peer.id = events.related_issue_id
```

(`%s` is `joinProjects`, which may itself add another join. Order
matters: the project join must come before the new alias joins
because `joinProjects` references `events.project_id` directly.)

- [ ] **Step 4: Broaden the `related_issue_id` NULL-scrub in the >=v8 exporter**

In `exportEvents` (the >=v8 variant), find the existing
`relatedIDExpr` / `relatedUIDExpr` block:

```go
relatedIDExpr, relatedUIDExpr := "events.related_issue_id", "events.related_issue_uid"
if !opts.IncludeDeleted {
	relatedIDExpr = `CASE WHEN events.type = 'issue.links_changed'
	                          AND peer.deleted_at IS NOT NULL
	                     THEN NULL
	                     ELSE events.related_issue_id END`
	relatedUIDExpr = `CASE WHEN events.type = 'issue.links_changed'
	                           AND peer.deleted_at IS NOT NULL
	                      THEN NULL
	                      ELSE events.related_issue_uid END`
}
```

Replace with:

```go
// Scrub related_issue_id when the peer is missing entirely
// (any event type) OR, on live-only export, when an
// issue.links_changed peer is soft-deleted (kata#1 history-
// preservation rule). Peer-missing must be checked first so
// `peer.deleted_at` doesn't dereference a NULL row.
scrubCondition := `(peer.id IS NULL AND events.related_issue_id IS NOT NULL)`
if !opts.IncludeDeleted {
	scrubCondition += ` OR (events.type = 'issue.links_changed' AND peer.deleted_at IS NOT NULL)`
}
relatedIDExpr := `CASE WHEN ` + scrubCondition + ` THEN NULL ELSE events.related_issue_id END`
relatedUIDExpr := `CASE WHEN ` + scrubCondition + ` THEN NULL ELSE events.related_issue_uid END`
```

- [ ] **Step 5: Apply the same broadened scrub to V3, V2, V1**

In each of `exportEventsV3` (`export.go:691`), `exportEventsV2`
(`export.go:747`), and `exportEventsV1` (`export.go:809`), replace
the analogous `relatedIDExpr` / `relatedUIDExpr` block with the
same `scrubCondition` pattern from Step 4. (V1 has no
`related_issue_uid` column — apply only to `relatedIDExpr` there.)

- [ ] **Step 6: Run the full jsonl package**

Run: `go test ./internal/jsonl/ -count=1`

Expected: all existing tests pass. The cutover behavior change is
covered indirectly by `TestAutoCutoverUpgradesLegacyV1DB` — verify
it still passes specifically:

Run: `go test ./internal/jsonl/ -run TestAutoCutoverUpgradesLegacyV1DB -v`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/jsonl/export.go
git commit -m "$(cat <<'EOF'
jsonl: scrub orphan events at export

Add LEFT JOIN issues subject_issue + WHERE filter to drop events
whose issue_id refers to a missing issue. Broaden the existing
related_issue_id NULL-scrub to cover any event type whose peer is
fully missing, in addition to the existing soft-deleted-peer case
for issue.links_changed.

Refactor eventExportWhere to return []string clauses so callers
can prepend the orphan filter without WHERE-prefix gymnastics.
EOF
)"
```

---

## Task 4: Wire `AutoCutover` to call preflight and halt on Unknown

**Files:**
- Modify: `internal/jsonl/cutover.go`
- Modify: `internal/jsonl/cutover_test.go`

Adds the preflight call before export and halts if the source has
unknown FK corruption. Also adds the integration test for the
known-classes happy path (TestAutoCutover_DropsAllKnownOrphanClasses).

- [ ] **Step 1: Write `TestAutoCutover_HaltsOnUnknownFKClass` first**

Append to `internal/jsonl/cutover_test.go`:

```go
// TestAutoCutover_HaltsOnUnknownFKClass: a source DB containing
// an FK violation outside the known orphan classes (a
// project_aliases row pointing at a missing project) refuses to
// cutover and reports actionable detail. The source DB is left
// untouched and no temp files remain.
func TestAutoCutover_HaltsOnUnknownFKClass(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	seedV3DBWithOrphans(t, path, orphanSpec{OrphanProjectAlias: true})

	before, err := os.ReadFile(path) //nolint:gosec // test fixture under TempDir
	require.NoError(t, err)

	err = jsonl.AutoCutover(ctx, path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "project_aliases")
	assert.Contains(t, err.Error(), "parent=projects")
	assert.Contains(t, err.Error(), "column=project_id")

	after, err := os.ReadFile(path) //nolint:gosec // test fixture under TempDir
	require.NoError(t, err)
	assert.Equal(t, before, after, "source DB must not be mutated on preflight halt")
	assertNoCutoverTemps(t, path)
}
```

- [ ] **Step 2: Write `TestAutoCutover_DropsAllKnownOrphanClasses` second**

Append to `internal/jsonl/cutover_test.go`:

```go
// TestAutoCutover_DropsAllKnownOrphanClasses: a source DB with
// orphans across all four known classes (events, comments,
// links, issue_labels) cuts over successfully. Orphan rows are
// dropped; events with valid issue_id but orphan related_issue_id
// are preserved with NULL related fields.
func TestAutoCutover_DropsAllKnownOrphanClasses(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	seedV3DBWithOrphans(t, path, orphanSpec{
		OrphanComments:     2,
		OrphanLinks:        2,
		OrphanIssueLabels:  1,
		OrphanEventIssueID: 1,
		OrphanEventRelated: 1,
	})

	require.NoError(t, jsonl.AutoCutover(ctx, path))

	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	assertCurrentSchemaVersion(t, path)

	var commentCount, linkCount, labelCount, eventCount int
	require.NoError(t, d.QueryRowContext(ctx, `SELECT COUNT(*) FROM comments`).Scan(&commentCount))
	require.NoError(t, d.QueryRowContext(ctx, `SELECT COUNT(*) FROM links`).Scan(&linkCount))
	require.NoError(t, d.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_labels`).Scan(&labelCount))
	require.NoError(t, d.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&eventCount))
	assert.Equal(t, 0, commentCount, "orphan comments should be dropped")
	assert.Equal(t, 0, linkCount, "orphan links should be dropped")
	assert.Equal(t, 0, labelCount, "orphan issue_labels should be dropped")

	// One event survives: the related-only orphan, with NULL
	// related fields. The issue_id-orphan event was dropped.
	assert.Equal(t, 1, eventCount, "only the related-only-orphan event should survive")
	var relatedID, relatedUID sql.NullString
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT related_issue_id, related_issue_uid FROM events`).Scan(&relatedID, &relatedUID))
	assert.False(t, relatedID.Valid, "related_issue_id must be NULL after scrub")
	assert.False(t, relatedUID.Valid, "related_issue_uid must be NULL after scrub")

	assertNoCutoverTemps(t, path)
}
```

- [ ] **Step 3: Run both tests, confirm they fail**

Run: `go test ./internal/jsonl/ -run 'TestAutoCutover_(HaltsOnUnknownFKClass|DropsAllKnownOrphanClasses)' -v`

Expected: both fail. The halt test fails because AutoCutover
currently calls export directly (no preflight) and the export's
INNER JOIN on project_aliases doesn't catch the orphan — it'll
fail somewhere in the import path with the rewritten Task 1
diagnostic, not the preflight format. The drops-all test fails on
the import-time FK check unless the events scrub from Task 3 ran;
if Task 3 already ran, the drops-all test may already pass — that
is fine, the assertion is still meaningful.

- [ ] **Step 4: Modify `AutoCutover` in `cutover.go` to call preflight**

Edit `internal/jsonl/cutover.go`. After the `version >= db.CurrentSchemaVersion()` early-return check at line 32-34, add the preflight call. The full updated `AutoCutover` body:

```go
func AutoCutover(ctx context.Context, path string) error {
	tmpJSONL := path + ".import.tmp.jsonl"
	tmpDB := path + ".import.tmp.db"
	if err := rejectCutoverTemps(tmpJSONL, tmpDB); err != nil {
		return err
	}
	version, err := db.PeekSchemaVersion(ctx, path)
	if err != nil {
		return err
	}
	if version >= db.CurrentSchemaVersion() {
		return nil
	}

	report, err := PreflightSourceFKs(ctx, path)
	if err != nil {
		return err
	}
	if len(report.UnknownViolations) > 0 {
		return formatUnknownViolations(path, report.UnknownViolations)
	}

	cleanupTemps := true
	defer func() {
		if cleanupTemps {
			removeSQLiteFileSet(tmpJSONL)
			removeSQLiteFileSet(tmpDB)
		}
	}()
	if err := exportCutoverSource(ctx, path, tmpJSONL); err != nil {
		return err
	}
	if err := importCutoverTarget(ctx, tmpJSONL, tmpDB); err != nil {
		return err
	}

	backup := fmt.Sprintf("%s.bak.%s", path, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(path, backup); err != nil {
		return fmt.Errorf("backup source db: %w", err)
	}
	if err := os.Rename(tmpDB, path); err != nil {
		_ = os.Rename(backup, path)
		return fmt.Errorf("install cutover db: %w", err)
	}
	cleanupTemps = false
	removeSQLiteFileSet(tmpJSONL)
	return nil
}
```

- [ ] **Step 5: Add `formatUnknownViolations` to `cutover.go`**

Append at the end of `cutover.go` (before `removeSQLiteFileSet`):

```go
// formatUnknownViolations renders the preflight halt error.
// Caps per-child-table output at 20 rows to bound log size on
// widely-corrupted DBs. Includes a remediation hint pointing at
// the sqlite3 PRAGMA the operator can run by hand.
func formatUnknownViolations(path string, violations []FKViolation) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "preflight: source DB at %s has unhandled foreign-key corruption that cutover cannot resolve. ", path)
	sb.WriteString("Inspect with `sqlite3 ")
	sb.WriteString(path)
	sb.WriteString(" 'PRAGMA foreign_key_check;'` and repair before retrying. Found:")
	perTable := map[string]int{}
	for _, v := range violations {
		if perTable[v.Table] >= 20 {
			continue
		}
		perTable[v.Table]++
		col := v.Column
		if col == "" {
			col = "?"
		}
		fmt.Fprintf(&sb, "\n  %s rowid=%d parent=%s column=%s", v.Table, v.RowID, v.ParentTable, col)
	}
	return errors.New(sb.String())
}
```

- [ ] **Step 6: Add the imports to `cutover.go`**

Add `"errors"` and `"strings"` to the import block. The full
updated imports for `cutover.go`:

```go
import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wesm/kata/internal/db"
)
```

- [ ] **Step 7: Run both new cutover tests, confirm they pass**

Run: `go test ./internal/jsonl/ -run 'TestAutoCutover_(HaltsOnUnknownFKClass|DropsAllKnownOrphanClasses)' -v`

Expected: both PASS.

- [ ] **Step 8: Run the full jsonl package**

Run: `go test ./internal/jsonl/ -count=1`

Expected: all tests pass.

- [ ] **Step 9: Commit**

```bash
git add internal/jsonl/cutover.go internal/jsonl/cutover_test.go
git commit -m "$(cat <<'EOF'
jsonl: preflight source FK violations before cutover

AutoCutover now classifies source-DB FK violations before
export. Known orphan classes (events/comments/links/issue_labels
on issues) proceed to the export-side scrub. Unknown classes
abort the cutover with per-row detail (table, rowid, parent,
column) and a sqlite3 hint for manual inspection.

Source DB stays read-only on the halt path.
EOF
)"
```

---

## Task 5: Print stderr summary on successful cutover

**Files:**
- Modify: `internal/jsonl/cutover.go`
- Modify: `internal/jsonl/cutover_test.go`

Adds the final user-visible piece: a one-line stderr summary
listing per-class drop counts. Does not count NULL-scrubs.

- [ ] **Step 1: Write `TestAutoCutover_PrintsOrphanSummary` first**

Append to `internal/jsonl/cutover_test.go`:

```go
// TestAutoCutover_PrintsOrphanSummary: when cutover discards
// orphan rows, exactly one stderr line summarizes them, listing
// only nonzero classes in the fixed order events / comments /
// links / issue_labels. NULL-scrubbed events are NOT counted.
func TestAutoCutover_PrintsOrphanSummary(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	seedV3DBWithOrphans(t, path, orphanSpec{
		OrphanComments:     2,
		OrphanLinks:        2,
		OrphanIssueLabels:  1,
		OrphanEventIssueID: 1,
		OrphanEventRelated: 1, // scrub, must not appear in summary
	})

	stderr, restore := captureStderr(t)
	err := jsonl.AutoCutover(ctx, path)
	captured := restore()
	require.NoError(t, err)

	// 1 dropped event + 2 dropped comments + 2 dropped links +
	// 1 dropped issue_label = 6. The related-only event is
	// scrubbed, not dropped, and is excluded from the summary.
	expected := "kata cutover: discarded 6 orphan rows from old DB (events: 1, comments: 2, links: 2, issue_labels: 1)\n"
	assert.Equal(t, expected, string(stderr.Bytes()))
	_ = captured
}

// TestAutoCutover_NoSummaryWhenClean: no stderr output when the
// source DB has zero orphans, so existing operators upgrading a
// clean DB see no behavior change.
func TestAutoCutover_NoSummaryWhenClean(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	seedV3DBWithOrphans(t, path, orphanSpec{}) // baseline only

	stderr, restore := captureStderr(t)
	err := jsonl.AutoCutover(ctx, path)
	_ = restore()
	require.NoError(t, err)
	assert.Empty(t, stderr.Bytes())
}
```

- [ ] **Step 2: Add the `captureStderr` helper to `fixtures_test.go`**

Append to `internal/jsonl/fixtures_test.go`:

```go
// captureStderr redirects os.Stderr to an in-memory buffer for
// the duration of the test. The returned restore function reverts
// os.Stderr and copies any pending pipe data into the buffer.
// Use the buffer (not the restore return value) for assertions.
func captureStderr(t *testing.T) (*bytes.Buffer, func() *bytes.Buffer) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	original := os.Stderr
	os.Stderr = w
	buf := &bytes.Buffer{}
	done := make(chan struct{})
	go func() {
		_, _ = buf.ReadFrom(r)
		close(done)
	}()
	return buf, func() *bytes.Buffer {
		os.Stderr = original
		_ = w.Close()
		<-done
		_ = r.Close()
		return buf
	}
}
```

- [ ] **Step 3: Run the test, confirm it fails**

Run: `go test ./internal/jsonl/ -run TestAutoCutover_PrintsOrphanSummary -v`

Expected: FAIL — captured stderr is empty (no summary printed yet).

- [ ] **Step 4: Modify `AutoCutover` to print the summary**

In `internal/jsonl/cutover.go`, the current success path ends:

```go
cleanupTemps = false
removeSQLiteFileSet(tmpJSONL)
return nil
```

Replace with:

```go
cleanupTemps = false
removeSQLiteFileSet(tmpJSONL)
if line := formatOrphanSummary(report); line != "" {
	fmt.Fprintln(os.Stderr, line)
}
return nil
```

(The `report` variable already lives in the function scope from
the preflight call earlier in the function.)

- [ ] **Step 5: Add `formatOrphanSummary` to `cutover.go`**

Append in `cutover.go` (alongside `formatUnknownViolations`):

```go
// formatOrphanSummary renders the post-cutover summary line.
// Returns "" when no orphans were dropped, so callers can skip
// the println entirely on clean DBs. Only nonzero classes are
// listed, in the fixed order events / comments / links /
// issue_labels. ScrubbedRowsByTable is intentionally not
// included — scrubs preserve the row, so reporting them as
// "discarded" would mislead.
func formatOrphanSummary(report OrphanReport) string {
	classes := []string{"events", "comments", "links", "issue_labels"}
	var parts []string
	total := 0
	for _, c := range classes {
		n := len(report.DroppedRowsByTable[c])
		if n == 0 {
			continue
		}
		total += n
		parts = append(parts, fmt.Sprintf("%s: %d", c, n))
	}
	if total == 0 {
		return ""
	}
	return fmt.Sprintf("kata cutover: discarded %d orphan rows from old DB (%s)",
		total, strings.Join(parts, ", "))
}
```

- [ ] **Step 6: Run both summary tests, confirm they pass**

Run: `go test ./internal/jsonl/ -run 'TestAutoCutover_(PrintsOrphanSummary|NoSummaryWhenClean)' -v`

Expected: both PASS.

- [ ] **Step 7: Run the full jsonl package**

Run: `go test ./internal/jsonl/ -count=1`

Expected: all tests pass.

- [ ] **Step 8: Commit**

```bash
git add internal/jsonl/cutover.go internal/jsonl/cutover_test.go internal/jsonl/fixtures_test.go
git commit -m "$(cat <<'EOF'
jsonl: print one-line stderr summary on cutover with drops

After successful cutover, if any orphan rows were dropped, print
one summary line to stderr listing nonzero classes in the fixed
order events/comments/links/issue_labels. NULL-scrubbed events
are not counted -- the row survives and counting it as data
loss would mislead. Clean cutovers print nothing.
EOF
)"
```

---

## Task 6: Final verification

- [ ] **Step 1: Full test suite**

Run: `go test ./... -count=1`

Expected: all packages pass.

- [ ] **Step 2: Linters**

Run: `golangci-lint run ./...`

Expected: zero new warnings. If any pre-existing warnings appear in
the touched files, fix them inline.

- [ ] **Step 3: Verify the `gh issue view 43` reproduction is now fixed end-to-end**

The reported scenario (a v6 DB with 13 event orphans + 39 link
orphans + 10 comment orphans) cannot be reproduced exactly without
the user's actual DB, but the equivalent v3 DB seeded by
`TestAutoCutover_DropsAllKnownOrphanClasses` covers the same code
path. If you want extra confidence, run that test in `-v -count=5`
mode to confirm there's no flake from rowid ordering.

Run: `go test ./internal/jsonl/ -run TestAutoCutover_DropsAllKnownOrphanClasses -v -count=5`

Expected: 5 PASS in a row.

- [ ] **Step 4: Confirm no stray workspace changes outside the plan**

Run: `git status`

Expected: clean working tree (all changes committed across Tasks 1-5).

- [ ] **Step 5: Push branch and open PR**

(Only if the user confirms — do not push without explicit approval.)

```bash
git push -u origin fix/issue-43
gh pr create --title "Fix daemon cutover wedge on pre-existing FK orphans (#43)" --body "$(cat <<'EOF'
## Summary

- `jsonl.AutoCutover` now runs a read-only `PRAGMA foreign_key_check` classifier on the source DB before export. Known orphan classes (issues-child rows in events/comments/links/issue_labels) are scrubbed at export and reported on stderr. Unknown classes halt cutover with per-row detail (table/rowid/parent/column) so the operator can repair manually.
- `exportEvents` (all four schema-version variants) gains a `LEFT JOIN issues subject_issue` filter so orphan `events.issue_id` rows are dropped at export, matching the existing INNER-JOIN drop precedent for orphan comments/links/issue_labels. The `related_issue_id` NULL-scrub is broadened to cover any event type whose peer is fully missing.
- `validateBeforeCommit` (defense-in-depth) now reports per-row violations with column resolution via `PRAGMA foreign_key_list`, capped at 20 rows per child table.

Closes #43.
EOF
)"
```

- [ ] **Step 6: Close the issue (only after the PR merges)**

```bash
gh issue close 43 --comment "Fixed by <PR link>. Cutover now classifies source-DB FK violations: events/comments/links/issue_labels orphans against issues are dropped at export with a stderr summary of what was discarded; everything else halts with actionable detail."
```

---

## Notes for the implementing engineer

- The `internal/jsonl/import_test.go:168` line is already an
  uncommitted edit at the start of this work. It is the seed for
  Task 1's failing test — leave it in place; Task 1 commits it as
  part of the implementation.
- `db.OpenReadOnly` already exists in `internal/db/`; preflight
  uses it as-is.
- `whereClause` and `joinClauses` helpers live at
  `internal/jsonl/export.go:1173-1186` and are reused by Task 3.
- `setupClosedTestDB`, `assertCurrentSchemaVersion`,
  `assertNoCutoverTemps`, `writeLegacyV3DB` are pre-existing
  helpers in `fixtures_test.go` and `cutover_test.go`.
- Cap-at-20 appears twice (preflight halt + importer
  defense-in-depth). They are independent caps; do not extract a
  shared helper for them — the contexts are different enough that
  collapsing would cost more clarity than it saves.
