# kata Short IDs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace per-project sequential `#N` issue numbers with short IDs derived from each issue's ULID, qualified by project name in cross-project references (`kata#abc4`). Implements `docs/superpowers/specs/2026-05-10-kata-short-ids.md`.

**Architecture:** A new `internal/shortid` package provides derivation, validation, and qualified-form parsing. The `issues` table gains a `short_id TEXT NOT NULL` column populated by an auto-extend algorithm that takes the smallest ULID-suffix length giving in-project uniqueness. The JSONL cutover (v7→v8) drops `*_number` columns from `issues`, `projects`, `events`, `purge_log`; preserves stored `short_id` on subsequent cutovers. CLI/API/TUI surfaces switch from numeric `{number}` to string `{ref}` (short_id, qualified, or full ULID).

**Tech Stack:** Go 1.24, SQLite (via `mattn/go-sqlite3`), huma v2 for the HTTP API, cobra for CLI, bubbletea/lipgloss for TUI, testify for assertions. ULIDs via `github.com/oklog/ulid/v2`.

---

## File structure

**Created:**
- `internal/shortid/shortid.go` — `Derive(ulid, length)`, `Parse(ref)` for qualified/bare/ULID input, alphabet/length validation.
- `internal/shortid/shortid_test.go` — unit tests for the above.

**Modified (key files; the plan touches more):**
- `internal/db/schema.sql` — drop `*_number` columns, add `issues.short_id`, add CHECKs.
- `internal/db/db.go` — bump `currentSchemaVersion` to 8.
- `internal/db/types.go` — `Issue.Number int64` → `Issue.ShortID string`.
- `internal/db/queries.go` and siblings — auto-extend on create, lookup-by-short-id, drop number-based queries.
- `internal/db/queries_projects_merge.go` — rewrite collision logic; remove `ProjectMergeCollisionError`.
- `internal/jsonl/cutover.go` — new `cutoverV7toV8` migration; preserve-stored-short_id rule for future cutovers.
- `internal/jsonl/types.go`, `encoder.go`, `decoder.go`, `import.go`, `export.go` — issue envelope carries `short_id`, drops `number`.
- `internal/api/types.go` — every `Number int64 \`path:"number"\`` becomes `Ref string \`path:"ref"\``; response shapes drop `number`/`*_number`/`next_issue_number`, add `short_id`/`qualified_id`.
- `internal/daemon/handlers_*.go` — issue/event/project handlers resolve refs, emit short_ids; project merge handler rewritten; reset-counter handler removed.
- `cmd/kata/*.go` — every command that takes an issue ref or emits an issue number is updated; `cmd/kata/projects.go` loses `projectsResetCounterCmd`.
- `internal/tui/client_types.go`, `events_sse_parse.go`, `messages.go`, plus rendering — swap `Number` for `ShortID`, render `kata#abc4`.
- `README.md`, `CLAUDE.md`, `AGENTS.md`, `cmd/kata/quickstart.go` — docs and agent guidance updated.

**Deleted:**
- `cmd/kata/projects.go` `projectsResetCounterCmd` function and its CLI registration.
- `internal/daemon/handlers_projects.go` `mergeProject` collision branch (and the `db.ProjectMergeCollisionError` it returns); the reset-counter huma registration.
- `internal/api/types.go` `ResetCounterRequest`, `ResetCounterResponse`.
- `internal/db/queries_projects_merge.go` `ProjectMergeCollisionError`, `ErrProjectMergeIssueNumberCollision`, the collision-detection step in `MergeProjects`.

---

## Conventions

- **Run all tests after each task:** `make test` from the repo root. Lint with `make lint`. Vet with `make vet`. Each task ends with these green.
- **Commit message style:** Imperative, ≤72 char subject, follow the existing repo log (`git log --oneline -20`).
- **Test packages:** Existing tests live next to their code in `_test.go` files; integration tests use SQLite via `testenv` / `testfix` helpers in `internal/testenv` and `internal/testfix`. Reach for those helpers when a task needs a fresh DB.
- **No batched edits across unrelated files in one commit** — each task in this plan is one commit unit.

---

## Task 1: Create the shortid package

**Files:**
- Create: `internal/shortid/shortid.go`
- Create: `internal/shortid/shortid_test.go`

This package owns three operations: deriving a short_id from a ULID at a given length; validating a candidate short_id string; parsing a user-supplied reference (`kata#abc4`, `abc4`, or a full 26-char ULID) into a structured form for downstream resolution.

- [ ] **Step 1.1: Write failing tests**

Create `internal/shortid/shortid_test.go`:

```go
package shortid_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/shortid"
)

func TestDeriveTakesLowercaseSuffix(t *testing.T) {
	got, err := shortid.Derive("01HZNQ7VFPK1XGD8R5MABCD4EX", 4)
	require.NoError(t, err)
	assert.Equal(t, "d4ex", got)
}

func TestDeriveLength5(t *testing.T) {
	got, err := shortid.Derive("01HZNQ7VFPK1XGD8R5MABCD4EX", 5)
	require.NoError(t, err)
	assert.Equal(t, "cd4ex", got)
}

func TestDeriveRejectsBadULID(t *testing.T) {
	_, err := shortid.Derive("not-a-ulid", 4)
	assert.Error(t, err)
}

func TestDeriveRejectsLengthOutOfRange(t *testing.T) {
	_, err := shortid.Derive("01HZNQ7VFPK1XGD8R5MABCD4EX", 3)
	assert.ErrorIs(t, err, shortid.ErrLengthOutOfRange)
	_, err = shortid.Derive("01HZNQ7VFPK1XGD8R5MABCD4EX", 27)
	assert.ErrorIs(t, err, shortid.ErrLengthOutOfRange)
}

func TestValidShortIDAcceptsCrockfordLowercase(t *testing.T) {
	assert.True(t, shortid.Valid("abc4"))
	assert.True(t, shortid.Valid("d4ex"))
	assert.True(t, shortid.Valid("xabc4"))
}

func TestValidShortIDRejectsBadAlphabet(t *testing.T) {
	assert.False(t, shortid.Valid("ABC4"))   // uppercase
	assert.False(t, shortid.Valid("ilou"))   // disallowed Crockford letters
	assert.False(t, shortid.Valid("ab-4"))   // non-alphabet char
	assert.False(t, shortid.Valid(""))
	assert.False(t, shortid.Valid("abc"))    // too short
	assert.False(t, shortid.Valid("01234567890123456789012345678")) // too long
}

func TestParseQualified(t *testing.T) {
	r, err := shortid.Parse("kata#abc4")
	require.NoError(t, err)
	assert.Equal(t, "kata", r.Project)
	assert.Equal(t, "abc4", r.ShortID)
	assert.Empty(t, r.ULID)
}

func TestParseBare(t *testing.T) {
	r, err := shortid.Parse("abc4")
	require.NoError(t, err)
	assert.Empty(t, r.Project)
	assert.Equal(t, "abc4", r.ShortID)
	assert.Empty(t, r.ULID)
}

func TestParseULID(t *testing.T) {
	r, err := shortid.Parse("01HZNQ7VFPK1XGD8R5MABCD4EX")
	require.NoError(t, err)
	assert.Empty(t, r.Project)
	assert.Empty(t, r.ShortID)
	assert.Equal(t, "01HZNQ7VFPK1XGD8R5MABCD4EX", r.ULID)
}

func TestParseQualifiedWithMultipleHashes(t *testing.T) {
	r, err := shortid.Parse("my#proj#abc4")
	require.NoError(t, err)
	assert.Equal(t, "my#proj", r.Project)
	assert.Equal(t, "abc4", r.ShortID)
}

func TestParseRejectsLegacyNumber(t *testing.T) {
	_, err := shortid.Parse("12")
	assert.ErrorIs(t, err, shortid.ErrInvalidRef)
	_, err = shortid.Parse("kata#12")
	assert.ErrorIs(t, err, shortid.ErrInvalidRef)
}

func TestParseRejectsEmpty(t *testing.T) {
	_, err := shortid.Parse("")
	assert.ErrorIs(t, err, shortid.ErrInvalidRef)
}
```

- [ ] **Step 1.2: Run the tests to verify failure**

Run: `go test ./internal/shortid/... -count=1`
Expected: build failure, package does not exist.

- [ ] **Step 1.3: Implement the package**

Create `internal/shortid/shortid.go`:

```go
// Package shortid derives, validates, and parses kata's short_id display
// references. A short_id is the lowercased final L characters (4 ≤ L ≤ 26)
// of an issue's ULID; a qualified reference is "<project>#<short_id>".
package shortid

import (
	"errors"
	"strings"

	"github.com/wesm/kata/internal/uid"
)

// MinLength is the smallest short_id length the auto-extend algorithm
// will assign to a new issue. MaxLength is the full ULID length.
const (
	MinLength = 4
	MaxLength = 26
)

var (
	// ErrLengthOutOfRange is returned when a caller asks for a short_id
	// length below MinLength or above MaxLength.
	ErrLengthOutOfRange = errors.New("shortid: length out of range")
	// ErrInvalidULID is returned when Derive is given a non-ULID input.
	ErrInvalidULID = errors.New("shortid: invalid ULID")
	// ErrInvalidRef is returned by Parse when the input cannot be
	// interpreted as a bare short_id, qualified short_id, or ULID.
	ErrInvalidRef = errors.New("shortid: invalid ref")
)

// Derive returns the lowercased length-L suffix of ulid as a short_id.
// L must be in [MinLength, MaxLength]; ulid must be a strict 26-char ULID.
func Derive(ulidStr string, length int) (string, error) {
	if length < MinLength || length > MaxLength {
		return "", ErrLengthOutOfRange
	}
	if !uid.Valid(ulidStr) {
		return "", ErrInvalidULID
	}
	return strings.ToLower(ulidStr[uid.Length()-length:]), nil
}

// Valid reports whether s is a syntactically valid short_id (length in
// range, lowercased Crockford base32 alphabet). Valid does not check
// existence in any project.
func Valid(s string) bool {
	if len(s) < MinLength || len(s) > MaxLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isCrockfordLower(s[i]) {
			return false
		}
	}
	return true
}

// Ref is a parsed user-supplied issue reference. Exactly one of ShortID
// or ULID is populated. Project is set only when the input was qualified
// (e.g. "kata#abc4"); a bare short_id leaves Project empty for the
// caller to fill from workspace context.
type Ref struct {
	Project string
	ShortID string
	ULID    string
}

// Parse interprets s as one of:
//   - a 26-char ULID (Ref.ULID set)
//   - a bare short_id (Ref.ShortID set; Ref.Project empty)
//   - a qualified short_id "<project>#<short_id>" (Ref.Project, Ref.ShortID)
//
// Legacy numeric forms ("12", "kata#12") are rejected with ErrInvalidRef.
func Parse(s string) (Ref, error) {
	if s == "" {
		return Ref{}, ErrInvalidRef
	}
	if uid.Valid(s) {
		return Ref{ULID: s}, nil
	}
	// Split on the LAST '#' so project names containing '#' parse
	// unambiguously. (Project names with '#' are forbidden by schema
	// after cutover, but Parse must work consistently regardless.)
	if i := strings.LastIndex(s, "#"); i >= 0 {
		project := s[:i]
		short := s[i+1:]
		if project == "" || !Valid(short) {
			return Ref{}, ErrInvalidRef
		}
		return Ref{Project: project, ShortID: short}, nil
	}
	if !Valid(s) {
		return Ref{}, ErrInvalidRef
	}
	return Ref{ShortID: s}, nil
}

func isCrockfordLower(c byte) bool {
	switch {
	case c >= '0' && c <= '9':
		return true
	case c >= 'a' && c <= 'z':
		switch c {
		case 'i', 'l', 'o', 'u':
			return false
		default:
			return true
		}
	default:
		return false
	}
}
```

This implementation depends on `uid.Length()`. Add it to `internal/uid/uid.go` if not present:

```go
// Length is the fixed character length of a kata ULID (Crockford base32).
func Length() int { return encodedLen }
```

(The constant `encodedLen = 26` already exists in `internal/uid/uid.go`.)

- [ ] **Step 1.4: Run the tests to verify they pass**

Run: `go test ./internal/shortid/... -count=1`
Expected: all PASS.

- [ ] **Step 1.5: Commit**

```bash
git add internal/shortid/ internal/uid/
git commit -m "feat(shortid): package for short_id derivation and ref parsing"
```

---

## Task 2: Schema changes (drop *_number, add short_id)

**Files:**
- Modify: `internal/db/schema.sql`
- Modify: `internal/db/db.go` (bump `currentSchemaVersion`)
- Modify: `internal/db/db_test.go` (`TestOpen_RecordsCurrentSchemaVersion`)
- Modify: `internal/db/schema_completeness_test.go`

The fresh schema applied to new databases must reflect the post-cutover state: `issues.short_id` present and required, `*_number` columns gone, `projects.name NOT GLOB '*#*'`, the new unique index, and the new CHECK constraints. The cutover (Task 5) handles existing databases.

- [ ] **Step 2.1: Write failing tests**

Add to `internal/db/db_test.go` (in the same `db_test` package):

```go
func TestOpen_RecordsCurrentSchemaVersion(t *testing.T) {
	assert.Equal(t, 8, db.CurrentSchemaVersion())
	d := openTempDB(t)
	t.Cleanup(func() { _ = d.Close() })
	assertSchemaVersion(t, d, db.CurrentSchemaVersion())
}

func TestSchema_IssuesHasShortIDColumn(t *testing.T) {
	d := openTempDB(t)
	t.Cleanup(func() { _ = d.Close() })
	var typ string
	err := d.SQL().QueryRow(
		`SELECT type FROM pragma_table_info('issues') WHERE name='short_id'`,
	).Scan(&typ)
	require.NoError(t, err)
	assert.Equal(t, "TEXT", typ)
}

func TestSchema_IssuesNumberColumnGone(t *testing.T) {
	d := openTempDB(t)
	t.Cleanup(func() { _ = d.Close() })
	var n int
	err := d.SQL().QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('issues') WHERE name='number'`,
	).Scan(&n)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestSchema_ProjectsNextIssueNumberGone(t *testing.T) {
	d := openTempDB(t)
	t.Cleanup(func() { _ = d.Close() })
	var n int
	err := d.SQL().QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name='next_issue_number'`,
	).Scan(&n)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestSchema_EventsIssueNumberGone(t *testing.T) {
	d := openTempDB(t)
	t.Cleanup(func() { _ = d.Close() })
	var n int
	err := d.SQL().QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('events') WHERE name='issue_number'`,
	).Scan(&n)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestSchema_PurgeLogIssueNumberGone(t *testing.T) {
	d := openTempDB(t)
	t.Cleanup(func() { _ = d.Close() })
	var n int
	err := d.SQL().QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('purge_log') WHERE name='issue_number'`,
	).Scan(&n)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestSchema_ProjectNameRejectsHash(t *testing.T) {
	d := openTempDB(t)
	t.Cleanup(func() { _ = d.Close() })
	_, err := d.SQL().Exec(
		`INSERT INTO projects(uid, name) VALUES('01HZNQ7VFPK1XGD8R5MABCD4EX', 'has#hash')`,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CHECK")
}
```

(`openTempDB` and `assertSchemaVersion` are existing helpers in `internal/db/db_test.go`.)

- [ ] **Step 2.2: Run tests to verify failure**

Run: `go test ./internal/db/... -count=1 -run 'TestOpen_RecordsCurrentSchemaVersion|TestSchema_'`
Expected: FAIL on schema-version assertion (still 7) and on the absent columns/CHECKs.

- [ ] **Step 2.3: Update `internal/db/schema.sql`**

Edit `internal/db/schema.sql`:

1. **`projects` table:** drop the `next_issue_number INTEGER NOT NULL DEFAULT 1` column line. Add to the table-level CHECKs:

```sql
CHECK (name NOT GLOB '*#*')
```

2. **`issues` table:** drop the `number INTEGER NOT NULL` column line. Drop the `UNIQUE(project_id, number)` constraint. Add a `short_id` column and CHECKs:

```sql
short_id      TEXT NOT NULL,
...
CHECK (length(short_id) BETWEEN 4 AND 26),
CHECK (short_id NOT GLOB '*[^0-9abcdefghjkmnpqrstvwxyz]*'),
CHECK (short_id = lower(substr(uid, 27 - length(short_id), length(short_id))))
```

Add the unique index right after the existing per-issues indexes:

```sql
CREATE UNIQUE INDEX uniq_issues_project_short_id
  ON issues(project_id, short_id);
```

3. **`events` table:** drop the `issue_number INTEGER` column line. (The table keeps `issue_id`, `issue_uid`, `related_issue_id`, `related_issue_uid`.)

4. **`purge_log` table:** drop the `issue_number` column. (Find it with `grep -n issue_number internal/db/schema.sql`.)

5. Drop any indexes that reference the removed columns (search the file for `number`).

- [ ] **Step 2.4: Bump schema version**

In `internal/db/db.go`:

```go
const currentSchemaVersion = 8
```

- [ ] **Step 2.5: Run tests to verify they pass**

Run: `go test ./internal/db/... -count=1 -run 'TestOpen_RecordsCurrentSchemaVersion|TestSchema_'`
Expected: all PASS. Older tests in `internal/db/...` will now fail because they reference `Issue.Number` or `next_issue_number`. That is expected and addressed in subsequent tasks; do **not** delete or amend those tests yet.

- [ ] **Step 2.6: Commit**

```bash
git add internal/db/schema.sql internal/db/db.go internal/db/db_test.go
git commit -m "schema(v8): drop *_number columns, add issues.short_id and CHECKs"
```

---

## Task 3: Issue type changes (`Issue.Number` → `Issue.ShortID`)

**Files:**
- Modify: `internal/db/types.go`
- Modify: every Go file under `internal/db/` that references `Issue.Number` (find with `grep -rn 'Issue.Number\|i.Number\|issue.Number' internal/db/`).

The Go DTOs are how every other package sees an issue. Updating them now lets later tasks pick up the new field; the transitive compiler errors guide the rest of the work.

- [ ] **Step 3.1: Find every consumer**

```bash
grep -rn '\.Number' internal/db/*.go cmd/kata/*.go internal/api/*.go internal/daemon/*.go internal/jsonl/*.go internal/tui/*.go
```

Save the list — each one is a downstream call site that subsequent tasks will rename. The job in this task is only the `internal/db/types.go` definition and the `internal/db/` callers.

- [ ] **Step 3.2: Write a failing test**

Add to `internal/db/types_test.go` (create the file if it does not exist):

```go
package db_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wesm/kata/internal/db"
)

func TestIssue_HasShortIDFieldAndNoNumber(t *testing.T) {
	typ := reflect.TypeOf(db.Issue{})
	_, hasShortID := typ.FieldByName("ShortID")
	_, hasNumber := typ.FieldByName("Number")
	assert.True(t, hasShortID, "Issue.ShortID should exist")
	assert.False(t, hasNumber, "Issue.Number should be removed")
}
```

- [ ] **Step 3.3: Run to verify failure**

Run: `go test ./internal/db/... -run TestIssue_HasShortIDFieldAndNoNumber -count=1`
Expected: FAIL — Issue.Number still exists.

- [ ] **Step 3.4: Update `internal/db/types.go`**

Replace `Number int64` on the `Issue` struct with `ShortID string`. Keep `UID string`, `ProjectID int64`, etc. The struct now reads roughly:

```go
type Issue struct {
	ID           int64
	UID          string
	ProjectID    int64
	ShortID      string
	Title        string
	Body         string
	Status       string
	ClosedReason *string
	Owner        *string
	Priority     *int64
	Author       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ClosedAt     *time.Time
	DeletedAt    *time.Time
}
```

Drop `NextIssueNumber` from any `Project` struct in the same file.

- [ ] **Step 3.5: Run the type test**

Run: `go test ./internal/db/... -run TestIssue_HasShortIDFieldAndNoNumber -count=1`
Expected: PASS.

- [ ] **Step 3.6: Mechanically fix `internal/db/` callers**

The compiler now lists every call site to update inside `internal/db`. Run:

```bash
go build ./internal/db/...
```

For each error, replace the broken `Number` access with `ShortID`. Where SQL queries select `number` or `next_issue_number`, switch them to `short_id` (no scanning destinations for `next_issue_number`). Keep the test files in `internal/db/...` as-is for now if they read `Number` — they will be updated in their own focused commits in Tasks 5 and 7. (Test files outside `internal/db/...` are deferred to later tasks.)

- [ ] **Step 3.7: Run the db package build and unit tests for the fixed files**

Run: `go build ./internal/db/...`
Expected: build passes.

Run: `go test ./internal/db/... -count=1 -run TestIssue_HasShortIDFieldAndNoNumber`
Expected: PASS. Other tests in this package may still fail; that is expected until Task 5.

- [ ] **Step 3.8: Commit**

```bash
git add internal/db/types.go internal/db/types_test.go internal/db/queries*.go
git commit -m "db: rename Issue.Number to Issue.ShortID, drop NextIssueNumber"
```

---

## Task 4: Auto-extend on issue creation

**Files:**
- Modify: `internal/db/queries.go` (the `CreateIssue` / `CreateIssueInitial` path; find with `grep -n 'INSERT INTO issues' internal/db/`)
- Modify: `internal/db/queries_create_initial_test.go`
- Modify: any sibling create-path tests that broke from Task 3.

The auto-extend algorithm: at issue creation, find the smallest L ≥ 4 such that `lower(suffix(uid, L))` is not already used by any issue (live or soft-deleted) in the same project. Persist the resulting short_id.

- [ ] **Step 4.1: Write failing test**

Add to `internal/db/queries_create_initial_test.go`:

```go
func TestCreateIssue_AssignsLength4WhenUnique(t *testing.T) {
	d, ctx := openTestDB(t)
	pid := mustCreateProject(t, d, "demo")
	row, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: pid,
		UID:       "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title:     "first",
		Author:    "tester",
	})
	require.NoError(t, err)
	assert.Equal(t, "d4ex", row.ShortID)
}

func TestCreateIssue_ExtendsToLength5OnCollision(t *testing.T) {
	d, ctx := openTestDB(t)
	pid := mustCreateProject(t, d, "demo")
	_, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: pid,
		UID:       "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title:     "first",
		Author:    "tester",
	})
	require.NoError(t, err)
	// Different ULID with the same last 4 chars (D4EX), forcing extension.
	row2, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: pid,
		UID:       "01HZNQ7VFPK1XGD8R5MABCXD4EX",
		Title:     "second",
		Author:    "tester",
	})
	require.NoError(t, err)
	assert.Equal(t, "xd4ex", row2.ShortID)
}

func TestCreateIssue_ExtendsToLength6OnDoubleCollision(t *testing.T) {
	d, ctx := openTestDB(t)
	pid := mustCreateProject(t, d, "demo")
	uids := []string{
		"01HZNQ7VFPK1XGD8R5MABCD4EX",
		"01HZNQ7VFPK1XGD8R5MABCXD4EX"[:26], // pad/trim — see helper note below
		"01HZNQ7VFPK1XGD8R5MAYBXD4EX",
	}
	for i, u := range uids {
		_, err := d.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: pid,
			UID:       u,
			Title:     "i" + string(rune('0'+i)),
			Author:    "tester",
		})
		require.NoError(t, err, "create %s", u)
	}
	rows, err := d.IssuesByProject(ctx, pid)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	// Order is insertion (= ULID-ascending here).
	assert.Equal(t, "d4ex", rows[0].ShortID)
	assert.Equal(t, "xd4ex", rows[1].ShortID)
	assert.Equal(t, "ybxd4ex", rows[2].ShortID)
}
```

(`openTestDB` and `mustCreateProject` are existing helpers; if a 26-char ULID literal is awkward, use `uid.New()` plus a deterministic seeded variant — there is already `uid.FromStableSeed` in `internal/uid/uid.go`. Adjust the literal-vs-helper choice to fit existing patterns in this test file.)

- [ ] **Step 4.2: Run to verify failure**

Run: `go test ./internal/db/... -run TestCreateIssue_ -count=1`
Expected: FAIL — auto-extend not implemented.

- [ ] **Step 4.3: Implement auto-extend in the create path**

Inside `CreateIssue` (in `internal/db/queries.go` or whichever file currently holds it), before the `INSERT INTO issues`, compute the short_id:

```go
import (
	"github.com/wesm/kata/internal/shortid"
)

// ... inside CreateIssue, with `uidVal` being the new issue's ULID
// and `tx` being the active transaction:
shortID, err := assignShortID(ctx, tx, params.ProjectID, uidVal)
if err != nil {
	return Issue{}, fmt.Errorf("assign short_id: %w", err)
}
```

Add the helper to the same file (or a new `internal/db/queries_short_id.go`):

```go
func assignShortID(ctx context.Context, tx *sql.Tx, projectID int64, ulid string) (string, error) {
	for length := shortid.MinLength; length <= shortid.MaxLength; length++ {
		candidate, err := shortid.Derive(ulid, length)
		if err != nil {
			return "", err
		}
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM issues WHERE project_id = ? AND short_id = ?`,
			projectID, candidate,
		).Scan(&n); err != nil {
			return "", fmt.Errorf("collision check at length %d: %w", length, err)
		}
		if n == 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("short_id auto-extend exhausted for ulid %s", ulid)
}
```

Update the `INSERT INTO issues` SQL in `CreateIssue` to include the new `short_id` column with the value computed above.

- [ ] **Step 4.4: Run tests to verify they pass**

Run: `go test ./internal/db/... -run TestCreateIssue_ -count=1`
Expected: PASS.

Run: `go test ./internal/db/... -count=1`
Expected: any remaining failures should now be in tests that read `Issue.Number` (Task 3 left those alone) or the project merge collision tests (Task 7). Note them; do not fix in this commit.

- [ ] **Step 4.5: Commit**

```bash
git add internal/db/queries*.go
git commit -m "db: auto-extend short_id on issue creation"
```

---

## Task 5: Lookup queries by short_id

**Files:**
- Modify: `internal/db/queries.go` (or wherever `IssueByNumber` lives — find with `grep -n 'IssueByNumber\|issuesByNumber\|number = ?' internal/db/`).
- Modify: any tests that exercise `IssueByNumber`.

Replace number-keyed lookups with short_id-keyed lookups. Names: `IssueByNumber` → `IssueByShortID`. Keep `IssueByUID` unchanged.

- [ ] **Step 5.1: Write failing test**

Add to `internal/db/queries_issues_test.go`:

```go
func TestIssueByShortID_ReturnsLiveIssue(t *testing.T) {
	d, ctx := openTestDB(t)
	pid := mustCreateProject(t, d, "demo")
	created, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: pid, UID: "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title: "find me", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.IssueByShortID(ctx, pid, "d4ex", db.IncludeDeletedNo)
	require.NoError(t, err)
	assert.Equal(t, created.UID, got.UID)
}

func TestIssueByShortID_NotFoundForUnknownShortID(t *testing.T) {
	d, ctx := openTestDB(t)
	pid := mustCreateProject(t, d, "demo")
	_, err := d.IssueByShortID(ctx, pid, "zzzz", db.IncludeDeletedNo)
	assert.ErrorIs(t, err, db.ErrIssueNotFound)
}

func TestIssueByShortID_DefaultExcludesSoftDeleted(t *testing.T) {
	d, ctx := openTestDB(t)
	pid := mustCreateProject(t, d, "demo")
	created, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: pid, UID: "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title: "soon-gone", Author: "tester",
	})
	require.NoError(t, err)
	require.NoError(t, d.SoftDeleteIssue(ctx, created.UID, "tester"))

	_, err = d.IssueByShortID(ctx, pid, created.ShortID, db.IncludeDeletedNo)
	assert.ErrorIs(t, err, db.ErrIssueNotFound)
}

func TestIssueByShortID_IncludeDeletedYesResolvesSoftDeleted(t *testing.T) {
	d, ctx := openTestDB(t)
	pid := mustCreateProject(t, d, "demo")
	created, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: pid, UID: "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title: "soon-gone", Author: "tester",
	})
	require.NoError(t, err)
	require.NoError(t, d.SoftDeleteIssue(ctx, created.UID, "tester"))

	got, err := d.IssueByShortID(ctx, pid, created.ShortID, db.IncludeDeletedYes)
	require.NoError(t, err)
	assert.Equal(t, created.UID, got.UID)
}
```

- [ ] **Step 5.2: Run tests to verify failure**

Run: `go test ./internal/db/... -run TestIssueByShortID_ -count=1`
Expected: FAIL — function does not exist.

- [ ] **Step 5.3: Implement `IssueByShortID` and the `IncludeDeleted` enum**

In `internal/db/queries.go`:

```go
// IncludeDeleted controls whether soft-deleted rows are visible to a lookup.
type IncludeDeleted int

const (
	IncludeDeletedNo  IncludeDeleted = 0
	IncludeDeletedYes IncludeDeleted = 1
)

// IssueByShortID resolves a project-scoped short_id. Soft-deleted issues are
// returned only when include == IncludeDeletedYes (used by restore, idempotent
// re-delete, purge confirmation, and idempotency-key collision detection).
func (d *DB) IssueByShortID(ctx context.Context, projectID int64, shortID string, include IncludeDeleted) (Issue, error) {
	q := `SELECT ` + issueColumns + ` FROM issues
	      WHERE project_id = ? AND short_id = ?`
	if include == IncludeDeletedNo {
		q += ` AND deleted_at IS NULL`
	}
	row := d.SQL().QueryRowContext(ctx, q, projectID, shortID)
	issue, err := scanIssue(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, ErrIssueNotFound
	}
	return issue, err
}
```

(`issueColumns` and `scanIssue` are existing helpers — find with `grep -n 'issueColumns\|scanIssue' internal/db/`.)

Delete the old `IssueByNumber` function.

- [ ] **Step 5.4: Update internal callers**

Run `go build ./internal/db/...`. Replace each `IssueByNumber(ctx, pid, num)` call with `IssueByShortID(ctx, pid, ref, db.IncludeDeletedNo)` using the appropriate include argument. For restore/delete/purge code paths, pass `db.IncludeDeletedYes`.

- [ ] **Step 5.5: Run tests to verify they pass**

Run: `go test ./internal/db/... -run TestIssueByShortID_ -count=1`
Expected: PASS.

- [ ] **Step 5.6: Commit**

```bash
git add internal/db/queries*.go
git commit -m "db: replace IssueByNumber with IssueByShortID"
```

---

## Task 6: Fix the rest of `internal/db/` and helper queries

**Files:**
- Modify: every test file under `internal/db/` that still references `Issue.Number`, `next_issue_number`, or `IssueByNumber`. Find with `go test ./internal/db/... -count=1` — the failures list them.
- Modify: `internal/db/queries_events.go`, `internal/db/queries_links.go`, `internal/db/queries_delete.go`, etc. — anywhere a query selects `issues.number`, `events.issue_number`, or `purge_log.issue_number`.

This task is the cleanup pass that returns `internal/db/...` to a fully-green state, except for project merge (Task 7).

- [ ] **Step 6.1: Run all internal/db tests, capture failures**

```bash
go test ./internal/db/... 2>&1 | tee /tmp/dbtests.log
```

Expected output: failures naming files and missing fields. Each becomes a step.

- [ ] **Step 6.2: Sweep test files**

For each test that scans `Issue.Number`, replace with `Issue.ShortID`. For tests asserting an issue by number (e.g. `assertIssueExistsByNumber(t, d, pid, 1)`), update to assert by short_id. Where a test was constructing fixtures with `Number: 1`, drop the field — short_id is derived during create.

For tests on the events table that read an `issue_number` column, change to read `issue_uid` and resolve the short_id via a join only if the test specifically asserts display formatting.

- [ ] **Step 6.3: Sweep query files**

Find: `grep -rn 'issue_number\|next_issue_number\|number INT' internal/db/`. For each query, drop the `number` / `issue_number` from the SELECT/INSERT/UPDATE and let `short_id` (or `issue_uid` for events) carry the reference.

In `internal/db/queries_events.go`, the event-emission functions take an issue and currently denormalize `issue_number` into the row. Drop that argument and column; events keep `issue_uid` only.

In `internal/db/queries_delete.go`, the purge-log row no longer carries `issue_number`; drop it from the INSERT.

- [ ] **Step 6.4: Run db tests to confirm green (modulo merge)**

```bash
go test ./internal/db/... -count=1
```

Expected: all pass except project-merge tests, which Task 7 covers.

- [ ] **Step 6.5: Commit**

```bash
git add internal/db/
git commit -m "db: drop number-based selects/inserts across queries and tests"
```

---

## Task 7: Rewrite project merge collision behavior

**Files:**
- Modify: `internal/db/queries_projects_merge.go`
- Modify: `internal/db/queries_projects_test.go`
- Modify: `internal/daemon/handlers_projects.go` (the `mergeProject` handler error mapping)
- Modify: `internal/daemon/handlers_projects_test.go`

`MergeProjects` currently fails fast when source and target overlap on `(project_id, number)`. After cutover, the basis is `(project_id, short_id)`, and instead of failing the merge runs auto-extend on each colliding source-side issue. The merge response now lists the pre/post-merge short_ids for any shifted issues.

- [ ] **Step 7.1: Write failing test**

Replace `internal/db/queries_projects_test.go`'s collision test with one that asserts the new behavior:

```go
func TestMergeProjects_ExtendsCollidingSourceShortIDs(t *testing.T) {
	d, ctx := openTestDB(t)
	src := mustCreateProject(t, d, "src")
	dst := mustCreateProject(t, d, "dst")

	// Two issues whose ULIDs share the last 4 chars; one in each project.
	dstIssue, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: dst, UID: "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title: "dst", Author: "tester",
	})
	require.NoError(t, err)
	require.Equal(t, "d4ex", dstIssue.ShortID)
	srcIssue, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: src, UID: "01HZNQ7VFPK1XGD8R5MABCXD4EX",
		Title: "src", Author: "tester",
	})
	require.NoError(t, err)
	require.Equal(t, "d4ex", srcIssue.ShortID) // independent assignment per project

	res, err := d.MergeProjects(ctx, db.MergeProjectsParams{
		SourceProjectID: src, TargetProjectID: dst, Actor: "tester",
	})
	require.NoError(t, err)
	require.Len(t, res.ShortIDExtensions, 1)
	ext := res.ShortIDExtensions[0]
	assert.Equal(t, srcIssue.UID, ext.UID)
	assert.Equal(t, "d4ex", ext.PreMergeShortID)
	assert.Equal(t, "xd4ex", ext.PostMergeShortID)

	// Both issues now visible on dst with distinct short_ids.
	got, err := d.IssueByShortID(ctx, dst, "d4ex", db.IncludeDeletedNo)
	require.NoError(t, err)
	assert.Equal(t, dstIssue.UID, got.UID)
	got, err = d.IssueByShortID(ctx, dst, "xd4ex", db.IncludeDeletedNo)
	require.NoError(t, err)
	assert.Equal(t, srcIssue.UID, got.UID)
}
```

Delete the old `TestMergeProjects_FailsOnNumberCollision` test (or whatever the existing collision test is named).

- [ ] **Step 7.2: Run to verify failure**

Run: `go test ./internal/db/... -run TestMergeProjects_ -count=1`
Expected: FAIL — `ShortIDExtensions` field, new behavior absent.

- [ ] **Step 7.3: Rewrite `MergeProjects`**

In `internal/db/queries_projects_merge.go`:

1. Delete the `ProjectMergeCollisionError` type, `ErrProjectMergeIssueNumberCollision`, the related `Numbers` field, and the collision-detection step that used `(project_id, number)` overlap.
2. Add to `ProjectMergeResult`:

```go
type ShortIDExtension struct {
	UID              string
	PreMergeShortID  string
	PostMergeShortID string
}

type ProjectMergeResult struct {
	// ... existing fields ...
	ShortIDExtensions []ShortIDExtension
}
```

3. After the source rows are reassigned to the target project, for each source-side issue that now collides with an existing target-side `short_id`, run `assignShortID` (from Task 4) against the target project to find the smallest non-colliding length, update `issues.short_id` for that row, and append a `ShortIDExtension` entry. Process in source-side ULID-ascending order so the result is deterministic.

- [ ] **Step 7.4: Update the daemon handler**

In `internal/daemon/handlers_projects.go`, delete the `errors.As(err, &collision)` branch that maps to `project_merge_issue_number_collision`. Add the `ShortIDExtensions` field to `MergeProjectResponse` (in `internal/api/types.go`):

```go
type MergeProjectResponse struct {
	Body struct {
		// ... existing fields ...
		ShortIDExtensions []MergeShortIDExtension `json:"short_id_extensions,omitempty"`
	}
}

type MergeShortIDExtension struct {
	UID              string `json:"uid"`
	PreMergeShortID  string `json:"pre_merge_short_id"`
	PostMergeShortID string `json:"post_merge_short_id"`
}
```

Plumb `merged.ShortIDExtensions` into the response in the handler.

- [ ] **Step 7.5: Update the daemon test**

In `internal/daemon/handlers_projects_test.go`, delete the test that asserts the 409 / `project_merge_issue_number_collision` response. Add one that asserts a successful merge with `short_id_extensions` populated. Mirror the shape used in Step 7.1.

- [ ] **Step 7.6: Run tests to verify pass**

Run: `go test ./internal/db/... ./internal/daemon/... -run 'Merge' -count=1`
Expected: all PASS. The `ProjectMergeImportMappingCollisionError` path stays unchanged.

- [ ] **Step 7.7: Commit**

```bash
git add internal/db/queries_projects_merge.go internal/db/queries_projects_test.go internal/api/types.go internal/daemon/handlers_projects.go internal/daemon/handlers_projects_test.go
git commit -m "merge: auto-extend source short_ids on collision; remove number-collision error"
```

---

## Task 8: JSONL envelope changes

**Files:**
- Modify: `internal/jsonl/types.go`
- Modify: `internal/jsonl/encoder.go`
- Modify: `internal/jsonl/decoder.go`
- Modify: `internal/jsonl/export.go`
- Modify: `internal/jsonl/import.go`

The JSONL `issue` envelope now carries `short_id` and drops `number`. Older v7 inputs are still readable via the cutover (Task 9); current-version exports always emit the new shape.

- [ ] **Step 8.1: Write failing test**

Add to `internal/jsonl/roundtrip_test.go`:

```go
func TestRoundtrip_IssueEnvelopeCarriesShortID(t *testing.T) {
	d, ctx := openTestDB(t)
	pid := mustCreateProject(t, d, "demo")
	created, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: pid, UID: "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title: "rt", Author: "tester",
	})
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, d, &buf, jsonl.ExportOptions{}))

	scanner := bufio.NewScanner(&buf)
	var issuePayload map[string]any
	for scanner.Scan() {
		var env jsonl.Envelope
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &env))
		if env.Kind == jsonl.KindIssue {
			require.NoError(t, json.Unmarshal(env.Data, &issuePayload))
			break
		}
	}
	require.NotNil(t, issuePayload)
	assert.Equal(t, created.ShortID, issuePayload["short_id"])
	_, hasNumber := issuePayload["number"]
	assert.False(t, hasNumber, "issue envelope should not carry 'number'")
}
```

- [ ] **Step 8.2: Run to verify failure**

Run: `go test ./internal/jsonl/... -run TestRoundtrip_IssueEnvelopeCarriesShortID -count=1`
Expected: FAIL.

- [ ] **Step 8.3: Update the issue envelope type and encoder**

In `internal/jsonl/types.go`, find the issue record struct (it lives near the `KindIssue` constant). Add `ShortID string \`json:"short_id"\``; drop `Number int64 \`json:"number"\``.

In `internal/jsonl/export.go`, update the issue export query and row scan to include `short_id` and exclude `number`. (Find with `grep -n 'KindIssue\|number FROM issues' internal/jsonl/export.go`.)

In the events export: drop the `issue_number` column from the SELECT and from the row struct.

In the purge_log export: drop `issue_number` from the SELECT and the row struct.

In the projects export: drop `next_issue_number` from the SELECT and the row struct.

- [ ] **Step 8.4: Update the import path for the current version**

In `internal/jsonl/import.go`, the new import (no cutover) reads the `short_id` from each issue record and passes it through to `db.CreateIssue` directly — bypassing auto-extend. Add a `ShortIDOverride string` field to `db.CreateIssueParams` for this purpose (used only by JSONL import; absent on the normal create path):

```go
// In internal/db/types.go (CreateIssueParams):
type CreateIssueParams struct {
	// ... existing fields ...
	// ShortIDOverride, when non-empty, bypasses auto-extend and uses the
	// supplied short_id verbatim. Used by JSONL import to preserve stored
	// short_ids across cutovers (spec §8.1).
	ShortIDOverride string
}
```

In the create path, when `ShortIDOverride` is set, validate it (`shortid.Valid` and that it equals the lowercased suffix of the UID at its length), then use it instead of calling `assignShortID`.

- [ ] **Step 8.5: Run tests to verify they pass**

Run: `go test ./internal/jsonl/... -count=1`
Expected: PASS for the new test; old v7 cutover tests may fail until Task 9.

- [ ] **Step 8.6: Commit**

```bash
git add internal/jsonl/ internal/db/
git commit -m "jsonl: issue envelope carries short_id; ShortIDOverride for replays"
```

---

## Task 9: v7→v8 cutover migration

**Files:**
- Modify: `internal/jsonl/cutover.go`
- Create: `internal/jsonl/cutover_v7_test.go`
- Modify: `internal/jsonl/cutover_test.go` (the round-trip baseline test, if it asserts a specific schema version)

The cutover migrates an existing v7 database to v8: derives short_ids in ULID-ascending order per project; drops number-bearing data; rejects projects whose names contain `#`. Existing v8 inputs (later schema versions) flow through with `ShortIDOverride` (from Task 8) so stored short_ids are preserved.

- [ ] **Step 9.1: Write failing tests**

Create `internal/jsonl/cutover_v7_test.go`:

```go
package jsonl_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
)

func TestCutoverV7_AssignsShortIDsInULIDOrder(t *testing.T) {
	// Build a v7 fixture with two issues whose ULIDs share the last 4 chars
	// in the same project. (See fixtures_test.go for the v7 fixture builder.)
	v7Path := writeV7Fixture(t, []v7Issue{
		{ProjectName: "demo", UID: "01HZNQ7VFPK1XGD8R5MABCD4EX", Number: 1, Title: "first"},
		{ProjectName: "demo", UID: "01HZNQ7VFPK1XGD8R5MABCXD4EX", Number: 2, Title: "second"},
	})
	v8Path := tempDBPath(t)
	require.NoError(t, jsonl.Cutover(context.Background(), v7Path, v8Path))

	d, err := db.Open(context.Background(), v8Path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	rows, err := d.IssuesByProjectName(context.Background(), "demo")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "d4ex", rows[0].ShortID)
	assert.Equal(t, "xd4ex", rows[1].ShortID)
}

func TestCutoverV7_RejectsProjectNameWithHash(t *testing.T) {
	v7Path := writeV7Fixture(t, []v7Issue{
		{ProjectName: "has#hash", UID: "01HZNQ7VFPK1XGD8R5MABCD4EX", Number: 1, Title: "ok"},
	})
	v8Path := tempDBPath(t)
	err := jsonl.Cutover(context.Background(), v7Path, v8Path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has#hash")
	assert.Contains(t, err.Error(), "must not contain '#'")
}

func TestCutover_PreservesStoredShortIDs(t *testing.T) {
	// Round-trip a v8 export: stored short_ids must come out unchanged.
	d1, ctx := openTestDB(t)
	pid := mustCreateProject(t, d1, "demo")
	a, err := d1.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: pid, UID: "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title: "a", Author: "tester",
	})
	require.NoError(t, err)
	b, err := d1.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: pid, UID: "01HZNQ7VFPK1XGD8R5MABCXD4EX",
		Title: "b", Author: "tester",
	})
	require.NoError(t, err)

	export := exportToJSONL(t, d1)

	d2 := openEmptyDB(t)
	require.NoError(t, jsonl.Import(context.Background(), d2, export))

	got, err := d2.IssueByUID(context.Background(), a.UID)
	require.NoError(t, err)
	assert.Equal(t, a.ShortID, got.ShortID)
	got, err = d2.IssueByUID(context.Background(), b.UID)
	require.NoError(t, err)
	assert.Equal(t, b.ShortID, got.ShortID)
}
```

(`writeV7Fixture`, `tempDBPath`, `openEmptyDB`, `exportToJSONL` may need to be added to `internal/jsonl/fixtures_test.go`. The existing `cutover_v4_test.go` is the precedent for "build a v_N fixture and assert what cutover produces" — match that style. The `v7Issue` helper struct should mirror the v7 issue envelope shape.)

- [ ] **Step 9.2: Run to verify failure**

Run: `go test ./internal/jsonl/... -run 'TestCutoverV7_|TestCutover_PreservesStoredShortIDs' -count=1`
Expected: FAIL — cutover not implemented.

- [ ] **Step 9.3: Implement the v7→v8 cutover function**

In `internal/jsonl/cutover.go`, add a per-version migration step paralleling the existing v3→v4 path:

```go
func cutoverV7toV8(ctx context.Context, in *Decoder, out *db.DB) error {
	// 1. Stream the input, validating prerequisites.
	type pendingIssue struct {
		envelope IssueEnvelopeV7
		project  string
	}
	var (
		projectsByName = map[string]int64{} // populated as we replay
		issuesByProject = map[string][]pendingIssue{}
	)

	if err := in.WalkIssues(func(env IssueEnvelopeV7) error {
		if strings.Contains(env.ProjectName, "#") {
			return fmt.Errorf("project name %q must not contain '#'; rename before cutover", env.ProjectName)
		}
		issuesByProject[env.ProjectName] = append(issuesByProject[env.ProjectName], pendingIssue{
			envelope: env,
			project:  env.ProjectName,
		})
		return nil
	}); err != nil {
		return err
	}

	// 2. For each project, replay issues in ULID-ascending order.
	for project, list := range issuesByProject {
		sort.Slice(list, func(i, j int) bool { return list[i].envelope.UID < list[j].envelope.UID })
		for _, p := range list {
			if _, err := out.CreateIssue(ctx, db.CreateIssueParams{
				ProjectID: projectsByName[project],
				UID:       p.envelope.UID,
				Title:     p.envelope.Title,
				Body:      p.envelope.Body,
				Author:    p.envelope.Author,
				// ShortIDOverride is unset — auto-extend chooses.
			}); err != nil {
				return fmt.Errorf("replay issue %s: %w", p.envelope.UID, err)
			}
		}
	}
	// 3. Replay the rest (links, comments, labels, events without issue_number,
	//    purge_log without issue_number, etc.) — same as the existing cutover
	//    pattern for unaffected tables.
	return nil
}
```

This is sketch-level — the precise loop should match the existing pattern in `cutover.go`'s v3→v4 step. The two non-obvious requirements are:

1. **ULID-ascending order** for issue replay (so auto-extend reproduces the original creation order).
2. **`#` validation** runs before any inserts so the cutover fails before mutating the target.

Update the dispatch in `Cutover` to call `cutoverV7toV8` when the source version is 7.

- [ ] **Step 9.4: Implement preserve-stored-short_id for current-version imports**

In the new-version import path (Task 8 set up `ShortIDOverride`), the import sets `params.ShortIDOverride = env.ShortID` so the stored value is preserved and auto-extend is bypassed.

- [ ] **Step 9.5: Run tests to verify they pass**

Run: `go test ./internal/jsonl/... -count=1`
Expected: all PASS.

Run: `go test ./... -count=1`
Expected: still some failures in `cmd/kata`, `internal/daemon`, `internal/tui` until later tasks. Lint/vet can be skipped here.

- [ ] **Step 9.6: Commit**

```bash
git add internal/jsonl/
git commit -m "jsonl: v7→v8 cutover with auto-extend; preserve stored short_id on later cutovers"
```

---

## Task 10: API request/response type renames

**Files:**
- Modify: `internal/api/types.go` (every `Number int64 \`path:"number"\`` and every `Number int64 \`json:"number"\``).
- Modify: `internal/api/types_test.go` (if any tests assert OpenAPI shape).

Path parameters change from `{number}` to `{ref}`. Response shapes drop `number` / `*_number` / `next_issue_number`, add `short_id` / `qualified_id` (and pair UID + short_id for nested issue references in link records).

- [ ] **Step 10.1: List the request/response types**

```bash
grep -n 'Number    int64 `path:"number"\|Number int64    `path:"number"\|json:"number"\|next_issue_number\|to_number\|from_number\|parent_number' internal/api/types.go
```

Each line is one rename.

- [ ] **Step 10.2: Write failing test**

Add to `internal/api/types_test.go`:

```go
package api_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wesm/kata/internal/api"
)

func TestIssueResponseHasShortIDAndNoNumber(t *testing.T) {
	body := reflect.TypeOf(api.IssueResponse{}.Body)
	requireFieldHasJSONTag(t, body, "ShortID", "short_id")
	requireFieldHasJSONTag(t, body, "QualifiedID", "qualified_id")
	_, hasNumber := body.FieldByName("Number")
	assert.False(t, hasNumber)
}

func TestProjectResponseHasNoNextIssueNumber(t *testing.T) {
	body := reflect.TypeOf(api.ProjectResponse{}.Body)
	_, hasNINumber := body.FieldByName("NextIssueNumber")
	assert.False(t, hasNINumber)
}

func TestResetCounterTypesAreGone(t *testing.T) {
	// Compile-time check is enough; if these existed the test wouldn't compile.
	// Reflection check: no exported type with that name.
	pkg := reflect.TypeOf(api.IssueResponse{}).PkgPath()
	for _, name := range []string{"ResetCounterRequest", "ResetCounterResponse"} {
		_, ok := api.LookupType(name) // helper added in Step 10.4
		assert.False(t, ok, "%s.%s should not exist", pkg, name)
	}
}

func requireFieldHasJSONTag(t *testing.T, typ reflect.Type, name, jsonTag string) {
	t.Helper()
	f, ok := typ.FieldByName(name)
	if !ok {
		t.Fatalf("field %s missing", name)
	}
	got, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if got != jsonTag {
		t.Fatalf("field %s has json tag %q; want %q", name, got, jsonTag)
	}
}
```

- [ ] **Step 10.3: Run to verify failure**

Run: `go test ./internal/api/... -count=1`
Expected: FAIL on the field assertions.

- [ ] **Step 10.4: Update `internal/api/types.go`**

For each path-bearing request type (`*Request` with `Number int64 \`path:"number"\``), rename the field to `Ref string \`path:"ref" required:"true"\``. The huma path patterns in the daemon handlers will pick up the rename in Task 11.

For response bodies on issue endpoints (issues, lists, search, ready, events, digest), rename `Number int64 \`json:"number"\`` to `ShortID string \`json:"short_id"\``, and add a parallel `QualifiedID string \`json:"qualified_id"\`` field.

For link records inside response bodies, replace `FromNumber int64 \`json:"from_number"\`` and `ToNumber int64 \`json:"to_number"\`` with structured `From` and `To` objects:

```go
type LinkPeer struct {
	UID     string `json:"uid"`
	ShortID string `json:"short_id"`
}

type LinkRecord struct {
	ID        int64    `json:"id"`
	Type      string   `json:"type"`
	From      LinkPeer `json:"from"`
	To        LinkPeer `json:"to"`
	Author    string   `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}
```

For idempotency mismatch responses (find with `grep -n idempotency internal/api/types.go`), the existing-issue identifier becomes `{uid, short_id, qualified_id}`.

For project responses, drop `NextIssueNumber`.

Delete `ResetCounterRequest`, `ResetCounterResponse`, and the helper that mentions `next_issue_number`.

Add a helper for the test in Step 10.2 (only if you want runtime introspection):

```go
func LookupType(name string) (reflect.Type, bool) {
	// trivial lookup over a small registry of exported types in this package.
	// If this proves brittle, drop the runtime check and rely on the
	// compile-time guarantee that the deleted types no longer exist.
	return nil, false // safe default for now
}
```

(Keep the lookup minimal; the meaningful check is compile-time.)

- [ ] **Step 10.5: Run tests to verify pass**

Run: `go test ./internal/api/... -count=1`
Expected: PASS for the new tests. The huma OpenAPI generation may now error in `internal/daemon` until Task 11.

- [ ] **Step 10.6: Commit**

```bash
git add internal/api/
git commit -m "api: rename path:number→ref; drop number/next_issue_number; add short_id"
```

---

## Task 11: Daemon handler ref resolution

**Files:**
- Modify: `internal/daemon/handlers_issues.go` (and any other handler files — find with `grep -ln '{number}\|in.Number' internal/daemon/`).
- Modify: `internal/daemon/handlers_*_test.go` for affected handlers.

Every issue-scoped handler now resolves a `Ref` parameter (string) into an `Issue` row, using `shortid.Parse` and `db.IssueByShortID` / `db.IssueByUID`. The huma path strings are updated from `/issues/{number}` to `/issues/{ref}`.

- [ ] **Step 11.1: Write a failing test**

Add to `internal/daemon/handlers_issues_test.go`:

```go
func TestGetIssue_ResolvesByShortID(t *testing.T) {
	srv, d, ctx := startTestDaemon(t)
	pid := mustCreateProject(t, d, "demo")
	created, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: pid, UID: "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title: "via short_id", Author: "tester",
	})
	require.NoError(t, err)

	resp := srv.GET(t, "/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/"+created.ShortID)
	require.Equal(t, 200, resp.Status)
	body := resp.JSON(t)
	assert.Equal(t, created.UID, body["uid"])
	assert.Equal(t, "demo#"+created.ShortID, body["qualified_id"])
}

func TestGetIssue_ResolvesByULID(t *testing.T) {
	srv, d, ctx := startTestDaemon(t)
	pid := mustCreateProject(t, d, "demo")
	created, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: pid, UID: "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title: "via uid", Author: "tester",
	})
	require.NoError(t, err)

	resp := srv.GET(t, "/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/"+created.UID)
	require.Equal(t, 200, resp.Status)
	body := resp.JSON(t)
	assert.Equal(t, created.UID, body["uid"])
}

func TestGetIssue_LegacyNumberReturns404(t *testing.T) {
	srv, d, ctx := startTestDaemon(t)
	pid := mustCreateProject(t, d, "demo")
	_, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: pid, UID: "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title: "no number lookup", Author: "tester",
	})
	require.NoError(t, err)
	resp := srv.GET(t, "/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/12")
	require.Equal(t, 404, resp.Status)
}
```

(`startTestDaemon` and `srv.GET` are existing helpers in `internal/daemon/handlers_*_test.go` — find them and match the established test style.)

- [ ] **Step 11.2: Run to verify failure**

Run: `go test ./internal/daemon/... -run TestGetIssue_ -count=1`
Expected: FAIL (path still `{number}`, no resolver).

- [ ] **Step 11.3: Add a shared resolver helper**

In `internal/daemon/resolver.go` (new file):

```go
package daemon

import (
	"context"
	"errors"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/shortid"
)

// resolveIssueRef returns the issue identified by the URL path component
// {ref}. include controls soft-deleted visibility.
func resolveIssueRef(ctx context.Context, d *db.DB, projectID int64, ref string, include db.IncludeDeleted) (db.Issue, error) {
	parsed, err := shortid.Parse(ref)
	if err != nil {
		return db.Issue{}, api.NewError(404, "issue_not_found", "issue not found", "", nil)
	}
	switch {
	case parsed.ULID != "":
		issue, err := d.IssueByUID(ctx, parsed.ULID, include)
		if errors.Is(err, db.ErrIssueNotFound) {
			return db.Issue{}, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		// Cross-project guard: a ULID-based GET on /projects/{pid}/issues/{ref}
		// must still match pid.
		if err == nil && issue.ProjectID != projectID {
			return db.Issue{}, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		return issue, err
	default:
		// parsed.ShortID is set; parsed.Project is irrelevant here because
		// the URL already carries the project_id.
		issue, err := d.IssueByShortID(ctx, projectID, parsed.ShortID, include)
		if errors.Is(err, db.ErrIssueNotFound) {
			return db.Issue{}, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		return issue, err
	}
}
```

- [ ] **Step 11.4: Update each issue-scoped handler**

For every handler currently signed `func(ctx, *XRequest) (*XResponse, error)` where `XRequest` had `Number int64 \`path:"number"\``:

1. The huma path metadata changes: `Path: "/api/v1/projects/{project_id}/issues/{number}"` → `Path: "/api/v1/projects/{project_id}/issues/{ref}"`.
2. The handler body uses `resolveIssueRef(ctx, cfg.DB, in.ProjectID, in.Ref, db.IncludeDeletedNo)` (or `IncludeDeletedYes` for restore/delete/purge/idempotency-collision paths) to obtain the issue, then proceeds.
3. The response body fills `ShortID = issue.ShortID` and `QualifiedID = projectName + "#" + issue.ShortID` instead of `Number`.

- [ ] **Step 11.5: Run tests to verify they pass**

Run: `go test ./internal/daemon/... -count=1`
Expected: PASS for the new tests. Some other handlers may still be broken; the next sub-step covers them.

- [ ] **Step 11.6: Commit**

```bash
git add internal/daemon/
git commit -m "daemon: resolve issue refs via shortid.Parse; rename path {number}→{ref}"
```

---

## Task 12: Daemon event/purge handlers and reset-counter removal

**Files:**
- Modify: `internal/daemon/handlers_events.go` and any helpers that emit events.
- Modify: `internal/daemon/handlers_destructive.go` (purge).
- Modify: `internal/daemon/handlers_projects.go` (delete the reset-counter handler).
- Modify: corresponding `_test.go` files.

The events and purge_log columns are already gone (Tasks 2/6). What remains is removing the reset-counter handler and updating any handler that read/wrote the dropped fields.

- [ ] **Step 12.1: Write failing test**

Add to `internal/daemon/handlers_projects_test.go`:

```go
func TestResetCounterEndpointReturns404(t *testing.T) {
	srv, _, _ := startTestDaemon(t)
	resp := srv.POST(t, "/api/v1/projects/1/reset-counter", `{"to":1}`)
	assert.Equal(t, 404, resp.Status)
}
```

Add to `internal/daemon/handlers_events_test.go`:

```go
func TestEvents_PayloadIncludesShortIDNotNumber(t *testing.T) {
	srv, d, ctx := startTestDaemon(t)
	pid := mustCreateProject(t, d, "demo")
	created, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: pid, UID: "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title: "evtest", Author: "tester",
	})
	require.NoError(t, err)

	resp := srv.GET(t, "/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/events")
	require.Equal(t, 200, resp.Status)
	events := resp.JSON(t)["events"].([]any)
	first := events[0].(map[string]any)
	assert.Equal(t, created.ShortID, first["issue_short_id"])
	assert.Equal(t, created.UID, first["issue_uid"])
	_, hasNum := first["issue_number"]
	assert.False(t, hasNum)
}
```

- [ ] **Step 12.2: Run to verify failure**

Run: `go test ./internal/daemon/... -run 'TestResetCounter|TestEvents_PayloadIncludesShortID' -count=1`
Expected: FAIL.

- [ ] **Step 12.3: Delete the reset-counter handler**

In `internal/daemon/handlers_projects.go`, delete the entire huma registration block whose `Path` is `/api/v1/projects/{project_id}/reset-counter`. Delete its handler function.

- [ ] **Step 12.4: Update event payloads**

In `internal/daemon/handlers_events.go` (or wherever events are projected to JSON), drop `issue_number` from the response shape. Add `issue_short_id` populated by joining against `issues.short_id` for each event row.

For the SSE stream (`internal/daemon/handlers_sse.go` or similar — find with `grep -ln 'issue.created\|sse' internal/daemon/`), the same projection rule applies: emit `issue_short_id`, drop `issue_number`. Inside the stored event payload, parent/related/blocks references switch from `*_number` to `*_short_id` and `*_uid`.

- [ ] **Step 12.5: Run tests to verify pass**

Run: `go test ./internal/daemon/... -count=1`
Expected: PASS.

- [ ] **Step 12.6: Commit**

```bash
git add internal/daemon/
git commit -m "daemon: emit short_id in events; remove reset-counter endpoint"
```

---

## Task 13: CLI ref-parsing helper

**Files:**
- Create: `cmd/kata/refflag.go`
- Create: `cmd/kata/refflag_test.go`

A small helper that turns a positional CLI argument plus the workspace's project binding into a fully-qualified `(projectID, ref)` pair for the daemon API. Centralizing it stops every command from re-implementing the bare-vs-qualified split.

- [ ] **Step 13.1: Write failing test**

```go
package main_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	main "github.com/wesm/kata/cmd/kata"
)

func TestResolveRef_QualifiedSelectsProject(t *testing.T) {
	r, err := main.ResolveRef("kata#abc4", "fallback-project")
	require.NoError(t, err)
	assert.Equal(t, "kata", r.ProjectName)
	assert.Equal(t, "abc4", r.RefForAPI)
}

func TestResolveRef_BareUsesFallback(t *testing.T) {
	r, err := main.ResolveRef("abc4", "demo")
	require.NoError(t, err)
	assert.Equal(t, "demo", r.ProjectName)
	assert.Equal(t, "abc4", r.RefForAPI)
}

func TestResolveRef_ULIDUsesFallbackProject(t *testing.T) {
	r, err := main.ResolveRef("01HZNQ7VFPK1XGD8R5MABCD4EX", "demo")
	require.NoError(t, err)
	assert.Equal(t, "demo", r.ProjectName)
	assert.Equal(t, "01HZNQ7VFPK1XGD8R5MABCD4EX", r.RefForAPI)
}

func TestResolveRef_LegacyNumberFails(t *testing.T) {
	_, err := main.ResolveRef("12", "demo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "looks like a legacy issue number")
}

func TestResolveRef_RequiresProjectForBare(t *testing.T) {
	_, err := main.ResolveRef("abc4", "")
	assert.Error(t, err)
}
```

- [ ] **Step 13.2: Run to verify failure**

Run: `go test ./cmd/kata/... -run TestResolveRef_ -count=1`
Expected: FAIL — function doesn't exist.

- [ ] **Step 13.3: Implement the helper**

Create `cmd/kata/refflag.go`:

```go
package main

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/wesm/kata/internal/shortid"
)

// ResolvedRef is what a CLI command passes to the API: the project
// identifier (always a name string; the daemon resolves it to a project_id)
// and the issue ref string suitable for use as the {ref} path component.
type ResolvedRef struct {
	ProjectName string
	RefForAPI   string
}

// ResolveRef parses a positional CLI argument as either a qualified short_id
// ("kata#abc4"), a bare short_id ("abc4"), or a 26-char ULID. workspaceProject
// is the project name read from .kata.toml; it is required for the bare and
// ULID forms.
func ResolveRef(arg, workspaceProject string) (ResolvedRef, error) {
	if _, err := strconv.Atoi(arg); err == nil {
		return ResolvedRef{}, fmt.Errorf("%q looks like a legacy issue number; use a short_id (e.g. abc4) or kata#abc4", arg)
	}
	parsed, err := shortid.Parse(arg)
	if err != nil {
		if errors.Is(err, shortid.ErrInvalidRef) {
			return ResolvedRef{}, fmt.Errorf("%q is not a valid issue ref: %w", arg, err)
		}
		return ResolvedRef{}, err
	}
	switch {
	case parsed.Project != "":
		return ResolvedRef{ProjectName: parsed.Project, RefForAPI: parsed.ShortID}, nil
	case workspaceProject == "":
		return ResolvedRef{}, fmt.Errorf("no project bound to this workspace; use a qualified ref (e.g. kata#abc4)")
	case parsed.ULID != "":
		return ResolvedRef{ProjectName: workspaceProject, RefForAPI: parsed.ULID}, nil
	default:
		return ResolvedRef{ProjectName: workspaceProject, RefForAPI: parsed.ShortID}, nil
	}
}
```

(Match the legacy-number error message exactly; the test in 13.1 asserts the substring.)

- [ ] **Step 13.4: Run tests**

Run: `go test ./cmd/kata/... -run TestResolveRef_ -count=1`
Expected: PASS.

- [ ] **Step 13.5: Commit**

```bash
git add cmd/kata/refflag.go cmd/kata/refflag_test.go
git commit -m "cli: ResolveRef helper for parsing issue refs from positional args"
```

---

## Task 14: CLI commands — issue-ref consumers

**Files:**
- Modify: `cmd/kata/show.go`, `edit.go`, `close.go`, `comment.go`, `delete.go`, `restore.go`, `purge.go`, `assign.go`, `label.go`, plus their `_test.go` siblings.
- Modify: `cmd/kata/create.go` for the link flags (`--parent`, `--blocks`, `--blocked-by`, `--related`).

Each command currently parses its positional arg as an integer (`strconv.ParseInt`). Replace with `ResolveRef`; pipe the result into the URL. Each command's JSON output adopts `short_id` / `qualified_id` and drops `number`.

- [ ] **Step 14.1: Write failing tests for `kata show`**

In `cmd/kata/show_test.go`:

```go
func TestShow_ByShortID(t *testing.T) {
	te := testenv.New(t)
	te.MustCreate("title")
	out := te.Run(t, "show", "abc4") // helper ensures issue's short_id is abc4
	var got struct {
		Issue struct {
			ShortID     string `json:"short_id"`
			QualifiedID string `json:"qualified_id"`
			UID         string `json:"uid"`
		} `json:"issue"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "abc4", got.Issue.ShortID)
	assert.Contains(t, got.Issue.QualifiedID, "#abc4")
}

func TestShow_LegacyNumberFails(t *testing.T) {
	te := testenv.New(t)
	te.MustCreate("title")
	res := te.RunExpectError(t, "show", "1")
	assert.Contains(t, res.Stderr, "legacy issue number")
}
```

(`testenv.New`, `MustCreate`, `Run`, `RunExpectError` are existing helpers in `internal/testenv` / `cmd/kata/testhelpers_test.go`. The `MustCreate` helper may need a tweak so that the created issue's ULID has a known last-4 = `abc4` — add `MustCreateWithULID` if no equivalent exists.)

- [ ] **Step 14.2: Run to verify failure**

Run: `go test ./cmd/kata/... -run TestShow_ -count=1`
Expected: FAIL.

- [ ] **Step 14.3: Update `cmd/kata/show.go`**

Replace:

```go
n, err := strconv.ParseInt(args[0], 10, 64)
// ...
url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d", baseURL, projectID, n)
```

with:

```go
ref, err := ResolveRef(args[0], workspaceProject)
if err != nil { return err }
url := fmt.Sprintf("%s/api/v1/projects/%s/issues/%s", baseURL, url.PathEscape(ref.ProjectName), url.PathEscape(ref.RefForAPI))
```

Update the `Number int64 \`json:"number"\`` field in the response decode struct to `ShortID string \`json:"short_id"\`` and `QualifiedID string \`json:"qualified_id"\``. Adjust the human-readable output formatter from `#%d` to `%s` over the qualified id.

The daemon URL switches from project-id-numeric to project-name. If the existing CLI uses a project-id URL, keep that; otherwise the helper returns the name and the daemon resolves it. Match the existing convention in this command — peek at one or two sibling commands to confirm whether the daemon expects `{project_id}` (integer) or `{project_name}` (string) in the URL. Currently many handlers are `{project_id}`; if so, the CLI must first resolve project name → id (existing helper `resolveProject` or similar — find with `grep -n 'resolveProject\|projectByName' cmd/kata/`).

- [ ] **Step 14.4: Run show tests**

Run: `go test ./cmd/kata/... -run TestShow_ -count=1`
Expected: PASS.

- [ ] **Step 14.5: Repeat for the other ref-consuming commands**

Apply the same edit pattern to:

- `cmd/kata/edit.go` + `_test.go`
- `cmd/kata/close.go` + `_test.go`
- `cmd/kata/comment.go` + `_test.go`
- `cmd/kata/delete.go` + `_test.go`
- `cmd/kata/restore.go` + `_test.go`
- `cmd/kata/purge.go` + `_test.go`
- `cmd/kata/assign.go` + `_test.go`
- `cmd/kata/label.go` + `_test.go`

For each: ResolveRef the positional, switch URL/printf format, adjust the response struct, update the test asserting short_id-based lookup. Commit after each command (`git commit -m "cli: <command> accepts short_id / kata#abc4 / ULID"`).

For `cmd/kata/create.go`'s link flags (`--parent`, `--blocks`, etc.), the current code reads `strconv.ParseInt` and emits `to_number`. Replace with ResolveRef and emit the wire-format `to: {uid, short_id}` once per link. The test fixtures that check link payloads need a corresponding update.

- [ ] **Step 14.6: Run the whole CLI suite**

```bash
go test ./cmd/kata/... -count=1
```

Expected: PASS for all updated commands. Remaining failures should only be in `events`, `digest`, `list`, `search`, `ready`, `projects` — the next two tasks.

---

## Task 15: CLI commands — output-emitting (list, search, ready, events, digest)

**Files:**
- Modify: `cmd/kata/list.go`, `search.go`, `ready.go`, `events.go`, `digest.go`, plus `_test.go`.

These commands consume the daemon's response and emit JSON or human-readable output. The response shapes already changed in Task 10; the CLI just needs to read the new fields and stop printing the old ones.

- [ ] **Step 15.1: Write failing tests**

For each command, add an assertion that the JSON output contains `short_id` and `qualified_id` and lacks `number`. Example for `list`:

```go
func TestList_OutputsShortIDNotNumber(t *testing.T) {
	te := testenv.New(t)
	te.MustCreate("first")
	out := te.Run(t, "list")
	var got struct {
		Issues []map[string]any `json:"issues"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	require.NotEmpty(t, got.Issues)
	first := got.Issues[0]
	_, hasShort := first["short_id"]
	_, hasQualified := first["qualified_id"]
	_, hasNumber := first["number"]
	assert.True(t, hasShort)
	assert.True(t, hasQualified)
	assert.False(t, hasNumber)
}
```

Repeat for `search`, `ready`, `events`, `digest` with the appropriate response shape.

- [ ] **Step 15.2: Run to verify failure**

Run: `go test ./cmd/kata/... -run 'TestList_|TestSearch_|TestReady_|TestEvents_|TestDigest_' -count=1`
Expected: FAIL.

- [ ] **Step 15.3: Update each command's response-decode struct and printer**

For each file:

1. In the response struct literal, swap `Number int64 \`json:"number"\`` for `ShortID string \`json:"short_id"\`` (and add `QualifiedID string \`json:"qualified_id"\``).
2. In any human-readable printer (`fmt.Fprintf(out, "#%d %s\n", i.Number, i.Title)`), switch to `%s` over `QualifiedID`.

- [ ] **Step 15.4: Run all CLI tests to verify pass**

Run: `go test ./cmd/kata/... -count=1`
Expected: PASS for these commands. `projects` is the only remaining holdout (Task 16).

- [ ] **Step 15.5: Commit**

```bash
git add cmd/kata/list.go cmd/kata/search.go cmd/kata/ready.go cmd/kata/events.go cmd/kata/digest.go cmd/kata/*_test.go
git commit -m "cli: list/search/ready/events/digest emit short_id and qualified_id"
```

---

## Task 16: CLI projects — drop reset-counter and next_issue_number

**Files:**
- Modify: `cmd/kata/projects.go`
- Modify: `cmd/kata/projects_test.go`

`projectsResetCounterCmd`, the `--to` flag, the `NextIssueNumber` decode field, and the human-readable `next #%d` line are all removed. The merge command keeps working but its stdout drops the `next #%d` clause and gains a `short_id_extensions` line if any source issue was extended.

- [ ] **Step 16.1: Write failing tests**

```go
func TestProjects_ListJSONHasNoNextIssueNumber(t *testing.T) {
	te := testenv.New(t)
	out := te.Run(t, "projects", "list")
	var got struct {
		Projects []map[string]any `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	for _, p := range got.Projects {
		_, has := p["next_issue_number"]
		assert.False(t, has, "project %v should not have next_issue_number", p)
	}
}

func TestProjects_ResetCounterCommandIsAbsent(t *testing.T) {
	te := testenv.New(t)
	res := te.RunExpectError(t, "projects", "reset-counter", "1")
	assert.Contains(t, res.Stderr, `unknown command "reset-counter"`)
}

func TestProjects_MergeReportsShortIDExtensions(t *testing.T) {
	te := testenv.New(t)
	src := te.MustCreateProject("src")
	dst := te.MustCreateProject("dst")
	_ = te.MustCreateIssueWithULID(src, "01HZNQ7VFPK1XGD8R5MABCD4EX")
	_ = te.MustCreateIssueWithULID(dst, "01HZNQ7VFPK1XGD8R5MABCXD4EX")
	out := te.Run(t, "projects", "merge", strconv.FormatInt(src, 10), strconv.FormatInt(dst, 10), "--json")
	var got struct {
		ShortIDExtensions []map[string]string `json:"short_id_extensions"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	require.Len(t, got.ShortIDExtensions, 1)
	assert.Equal(t, "d4ex", got.ShortIDExtensions[0]["pre_merge_short_id"])
	assert.Equal(t, "xd4ex", got.ShortIDExtensions[0]["post_merge_short_id"])
}
```

(`MustCreateIssueWithULID` may need to be added to `testhelpers_test.go`; mirror the existing `MustCreate`.)

- [ ] **Step 16.2: Run to verify failure**

Run: `go test ./cmd/kata/... -run TestProjects_ -count=1`
Expected: FAIL.

- [ ] **Step 16.3: Update `cmd/kata/projects.go`**

1. Delete the `projectsResetCounterCmd` function and its `cmd.AddCommand(projectsResetCounterCmd())` line.
2. Drop `NextIssueNumber` from the project-list and project-detail decode structs.
3. Drop the `next #%d` clause from the human-readable output line in `projectsMergeCmd` (search the file for `next #`).
4. Add a `ShortIDExtensions` decode field to the merge response and add a printed line per extension when in human-readable mode (`extended <project>#<short> from <pre> to <post>` style).

- [ ] **Step 16.4: Run the projects tests**

Run: `go test ./cmd/kata/... -run TestProjects_ -count=1`
Expected: PASS.

- [ ] **Step 16.5: Commit**

```bash
git add cmd/kata/projects.go cmd/kata/projects_test.go
git commit -m "cli: drop reset-counter and next_issue_number; report short_id_extensions on merge"
```

---

## Task 17: TUI — client types and SSE parsing

**Files:**
- Modify: `internal/tui/client_types.go`
- Modify: `internal/tui/messages.go`
- Modify: `internal/tui/events_sse_parse.go`
- Modify: `internal/tui/sse_update_test.go`
- Modify: `internal/tui/client.go` (the `to_number` request body)
- Modify: `internal/tui/client_test.go`

The TUI types swap `Number int64` (and `*int64`) for `ShortID string` (and `*string`); the SSE parser reads `*_short_id` instead of `*_number`. UID-based fields stay untouched.

- [ ] **Step 17.1: Write failing tests**

Add to `internal/tui/sse_update_test.go`:

```go
func TestSSEUpdate_ReadsIssueShortID(t *testing.T) {
	frame := []byte(`{"event_id":1,"type":"issue.created","issue_uid":"01HZNQ7VFPK1XGD8R5MABCD4EX","issue_short_id":"d4ex","actor":"a","created_at":"2026-05-10T12:00:00Z"}`)
	got, err := parseEventFrame(frame)
	require.NoError(t, err)
	assert.Equal(t, "d4ex", got.IssueShortID)
	assert.Equal(t, "01HZNQ7VFPK1XGD8R5MABCD4EX", got.IssueUID)
}
```

Add to `internal/tui/client_test.go`:

```go
func TestClient_AddLinkSendsToShortID(t *testing.T) {
	rec := httptest.NewRecorder()
	srv := httptest.NewServer(/* ... matches existing test pattern ... */)
	t.Cleanup(srv.Close)
	cli := NewClient(srv.URL, "tester")
	require.NoError(t, cli.AddLink(context.Background(), "demo", "abc4", LinkAddRequest{Type: "blocks", ToRef: "xyz4"}))
	body := /* read recorded body */
	assert.Contains(t, body, `"to":{"short_id":"xyz4"}`) // exact JSON shape
}
```

(The existing test file shows the precise body-recording style; match it.)

- [ ] **Step 17.2: Run to verify failure**

Run: `go test ./internal/tui/... -run 'TestSSEUpdate_ReadsIssueShortID|TestClient_AddLinkSendsToShortID' -count=1`
Expected: FAIL.

- [ ] **Step 17.3: Update the type and parser**

In `internal/tui/client_types.go`:

```go
type Issue struct {
	UID         string  `json:"uid"`
	ShortID     string  `json:"short_id"`
	QualifiedID string  `json:"qualified_id"`
	ProjectID   int64   `json:"project_id"`
	ProjectName string  `json:"project_name"`
	Title       string  `json:"title"`
	// ... other fields unchanged ...
	ParentShortID *string `json:"parent_short_id,omitempty"`
}
```

Apply the same `Number → ShortID` rename throughout the file.

In `internal/tui/events_sse_parse.go`, change `IssueNumber *int64 \`json:"issue_number"\`` to `IssueShortID string \`json:"issue_short_id"\``, and the corresponding `to_number` / `from_number` extractors to `to_short_id` / `from_short_id`.

In `internal/tui/messages.go`, the `LinkRecord` struct's `FromNumber` / `ToNumber` become `From LinkPeer` / `To LinkPeer` (mirroring Task 10's API change).

In `internal/tui/client.go`, the link POST body changes from `{"to_number": …}` to `{"to": {"short_id": …}}`.

- [ ] **Step 17.4: Run TUI tests**

Run: `go test ./internal/tui/... -count=1`
Expected: most pass. Snapshot tests will be addressed in Task 18.

- [ ] **Step 17.5: Commit**

```bash
git add internal/tui/client_types.go internal/tui/messages.go internal/tui/events_sse_parse.go internal/tui/client.go internal/tui/sse_update_test.go internal/tui/client_test.go
git commit -m "tui: client types and SSE parser read short_id; AddLink sends to.short_id"
```

---

## Task 18: TUI — rendering and snapshots

**Files:**
- Modify: `internal/tui/list.go`, `detail.go`, `detail_event.go`, and any other rendering files (find with `grep -ln '#%d\|i.Number' internal/tui/`).
- Modify: snapshot files under `internal/tui/testdata/` (find with `grep -lrn '#1\|#2' internal/tui/testdata/`).

The list view's `#N` column becomes the short_id column; detail-pane headers print `kata#abc4`; the goldens are regenerated.

- [ ] **Step 18.1: Update rendering**

Replace `fmt.Sprintf("#%d", i.Number)` patterns with `i.QualifiedID` (or just `"#"+i.ShortID` in workspace-bound views, depending on the place — look at neighboring code to keep the visual consistent). Match column widths if the new format is longer.

In `internal/tui/detail_event.go`'s `linkPayloadDesc`, the `type #to_number` format becomes `type kata#to_short_id` (or just `type #to_short_id` inside one project).

- [ ] **Step 18.2: Regenerate snapshots**

Run: `UPDATE_SNAPSHOTS=1 go test ./internal/tui/... -count=1` (or whatever the existing repo convention is — check `internal/tui/testdata/README.md` if present, or `grep -rn 'UPDATE_SNAPSHOT' internal/tui/`).

Inspect the diff: `git diff internal/tui/testdata/`. Confirm every `#N` rendering became `kata#abc4` (or `#abc4` for in-workspace views), no other unrelated changes.

- [ ] **Step 18.3: Run the full TUI suite**

Run: `go test ./internal/tui/... -count=1`
Expected: all PASS.

- [ ] **Step 18.4: Commit**

```bash
git add internal/tui/ internal/tui/testdata/
git commit -m "tui: render kata#abc4 in list and detail views; refresh snapshots"
```

---

## Task 19: e2e tests and Beads importer verification

**Files:**
- Modify: `e2e/*` if any e2e fixture references `#N`.
- Modify: `cmd/kata/beads_import.go` and `cmd/kata/beads_import_test.go` (verify new behavior, no breaking changes expected).

The Beads importer creates fresh kata ULIDs for imported issues, so it picks up auto-extend automatically. Verify that with a test, then run e2e.

- [ ] **Step 19.1: Add a beads-import assertion**

Add to `cmd/kata/beads_import_test.go`:

```go
func TestBeadsImport_AssignsShortIDs(t *testing.T) {
	te := testenv.New(t)
	te.WriteBeadsFile(t, "fixture.json", []byte(`...`)) // existing fixture
	out := te.Run(t, "import", "beads", "fixture.json", "--json")
	// Parse the output, assert each created issue has a non-empty short_id.
	var got struct {
		Created []struct {
			ShortID string `json:"short_id"`
			UID     string `json:"uid"`
		} `json:"created"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	require.NotEmpty(t, got.Created)
	for _, c := range got.Created {
		assert.True(t, shortid.Valid(c.ShortID), "short_id %q invalid", c.ShortID)
		assert.NotEmpty(t, c.UID)
	}
}
```

- [ ] **Step 19.2: Run e2e suite**

```bash
go test ./e2e/... -count=1
```

For any failure, the test fixture or assertion is the thing to update — the implementation should not change. If a fixture has `#1` or `#2` literal references, swap them to short_id placeholders or use the per-test created issue's short_id. Commit per fixture file.

- [ ] **Step 19.3: Run the full project test suite**

```bash
make test
```

Expected: green.

- [ ] **Step 19.4: Commit**

```bash
git add cmd/kata/beads_import_test.go e2e/
git commit -m "e2e: update fixtures for short_id; assert beads import assigns short_ids"
```

---

## Task 20: Documentation

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`
- Modify: `AGENTS.md`
- Modify: `cmd/kata/quickstart.go`
- Modify: any `docs/` page that uses `#N` examples.

The agent-facing docs are the most important: they teach agents the new ref shape. Keep examples consistent.

- [ ] **Step 20.1: Update `README.md`**

Search for `#N`, `#1`, `#12`, "issue numbers", "per-project sequential" in the README. Replace examples with `kata#abc4` and `abc4`. Update the comparison table row that currently reads "Per-project sequential numbers (#12)" to "Short IDs derived from each issue's ULID (kata#abc4)".

- [ ] **Step 20.2: Update `CLAUDE.md` and `AGENTS.md`**

In both files, the "issue id" guidance is the part to refresh: explain the qualified vs bare form, give one example, link to the spec.

- [ ] **Step 20.3: Update `cmd/kata/quickstart.go`**

This is the agent contract surface — the example session must use the new ref shape. Find `kata show 1` and similar lines; rewrite them to `kata show abc4` (or use a placeholder + explanatory text describing what an agent will actually see).

- [ ] **Step 20.4: Run any doc-test that the repo carries**

Run: `make test`
Expected: green. (The quickstart text is exercised by tests under `cmd/kata/quickstart_test.go` if present; ensure those pass.)

- [ ] **Step 20.5: Commit**

```bash
git add README.md CLAUDE.md AGENTS.md cmd/kata/quickstart.go cmd/kata/quickstart_test.go docs/
git commit -m "docs: short_id examples in README, CLAUDE.md, AGENTS.md, quickstart"
```

---

## Task 21: Final sweep — lint, vet, nilaway, full test pass

**Files:** (no new files; verify only)

- [ ] **Step 21.1: Run lint**

```bash
make lint
```

Expected: no findings. Fix any caught by `golangci-lint`.

- [ ] **Step 21.2: Run vet**

```bash
make vet
```

Expected: clean.

- [ ] **Step 21.3: Run nilaway**

```bash
make nilaway
```

Expected: clean. (Nilaway can flag pointer-receiver patterns the renames may have shifted.)

- [ ] **Step 21.4: Run full test suite**

```bash
make test
```

Expected: green.

- [ ] **Step 21.5: Final grep for stale `Number` references**

```bash
grep -rn 'issue_number\|issuenum\|next_issue_number\|Issue\.Number\|i\.Number' \
  cmd/kata/ internal/ docs/superpowers/specs/ \
  | grep -v _test.go
```

Each hit is a regression to fix. (Test files often contain field names of structs unrelated to the issue — eyeball them; only fix the ones referring to the dropped fields.)

- [ ] **Step 21.6: Commit if anything was fixed**

```bash
git add -A
git commit -m "polish: final sweep — lint/vet/nilaway clean, no stale Number references"
```

---

## Self-review checklist

Spec coverage:

- §1 (motivation): captured in goal/architecture and the file structure.
- §2 (goals/non-goals): non-goal of dot-notation honored — no task adds it. Non-goal of legacy `#N` input honored — Task 13's `ResolveRef` rejects integers.
- §3 (display format): qualified parsing handled in Task 1; bare/qualified rendering in Tasks 14, 15, 18.
- §4 (derivation): Task 1.
- §4.3 (federation collision): not reified in code (deferred to a future spec); the framing is only documented.
- §5 (auto-extend algorithm): Task 4.
- §5.2 (stability + three shift situations): cutover (Task 9), project merge (Task 7), federation merge (deferred).
- §6 (lookup + soft-delete carveouts): Task 5, with `IncludeDeleted` argument; the carveout sites are noted in Task 11 (`IncludeDeletedYes` for restore/delete/purge/idempotency).
- §7 (schema): Task 2.
- §8 (cutover): Task 9.
- §8.1 (preserve stored short_ids): Task 8 (ShortIDOverride) + Task 9 (current-version import path uses it). Tested in Task 9.
- §9.1 (schema columns dropped): Task 2.
- §9.2 (wire/payload): Tasks 10, 12.
- §9.3 (REST URL paths): Task 11.
- §9.4 (project merge rewrite): Task 7.
- §9.5 (CLI commands + reset-counter removal): Tasks 14, 15, 16.
- §9.6 (TUI): Tasks 17, 18.
- §9.7 (hooks): Task 12 (event payloads); hooks reuse the same projection.
- §10 (Beads importer): Task 19.
- §11 (testing): tests are colocated with the change in each task; the multi-stage cutover-merge-cutover preservation test lives in Task 9.

Type/method consistency: `IssueByShortID` named consistently. `ShortIDOverride`, `ShortIDExtensions`, `MergeShortIDExtension`, `ResolvedRef`, `ResolveRef` consistent across tasks. Daemon helper is `resolveIssueRef` (lower-case private). API helper LinkPeer matches both API types and TUI.

Placeholder scan: no "TBD"/"TODO". Some tasks reference "the existing pattern" or "match neighboring style" — those are appropriate when the codebase already has a precedent the engineer should follow.

---

Plan complete and saved to `docs/superpowers/plans/2026-05-10-kata-short-ids.md`. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — Execute tasks in this session using `superpowers:executing-plans`, batch execution with checkpoints.

Which approach?
