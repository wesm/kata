# Kata TUI Projects View Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the modal-based project picker with a real top-level `viewProjects` table surface that browses every registered project with per-project stats, drills into a project's issue list via Enter, and replaces the current `P` modal binding with a "switch to projects view" semantic.

**Architecture:** Three layers change in lockstep. (1) DB gains `BatchProjectStats` returning per-project `{open, closed, last_event_at}` from a CTE-aggregated query that pre-aggregates issues and events independently to avoid cross-table row inflation. (2) API surfaces a new `ProjectOut` projection across all five project-bearing response types (decoupling the wire from `db.Project`) plus `?include=stats` on `GET /api/v1/projects`. (3) TUI gains a `viewProjects` view that renders the project table, an `All projects` pinned sentinel row whose totals are client-summed from the per-project rows, transition-driven refetch on entry (boot landing or `P` from list), and SSE-driven freshness invalidation while active. The existing modal-based picker (`openProjectPicker`, `modalProjectPicker`, etc.) is retired wholesale.

**Tech Stack:** Go 1.22+, modernc.org/sqlite (CTE + LEFT JOIN), Charmbracelet Bubble Tea (TUI), Lipgloss (rendering), Huma (HTTP routing), testify + golden snapshot tests.

**Spec reference:** `docs/superpowers/specs/2026-05-04-kata-tui-projects-view-design.md`

---

## File Structure

Files this plan creates or modifies:

**DB layer**
- Modify: `internal/db/types.go` — add `ProjectStats` struct
- Modify: `internal/db/queries_projects.go` (or new `internal/db/queries_projects_stats.go`) — add `BatchProjectStats`
- Modify: `internal/db/queries_projects_test.go` — add `TestBatchProjectStats_*` tests

**API layer**
- Modify: `internal/api/types.go` — add `ProjectOut`, `ProjectStatsOut`; replace `db.Project` references in `ProjectResolveBody`, `ListProjectsResponse`, `ShowProjectResponse`, `ResetCounterResponse`, `RemoveProjectResponse`
- Modify: `internal/daemon/handlers_projects.go` — add `dbProjectToOut`, route every project-returning handler through it, parse `?include=stats` in listProjects
- Modify: `internal/daemon/handlers_projects_test.go` — add JSON-snapshot tests for the five surfaces; add `TestListProjects_WithStats*`

**TUI client**
- Modify: `internal/tui/client_types.go` — add `ProjectSummaryWithStats`, `ProjectStatsSummary`
- Modify: `internal/tui/client.go` — add `Client.ListProjectsWithStats`
- Modify: `internal/tui/client_test.go` — add `TestClient_ListProjectsWithStats_*`

**TUI view**
- Create: `internal/tui/projects_view.go` — `viewProjects` enum value (in `model.go`), key handlers, transition helpers, sentinel summing
- Create: `internal/tui/projects_view_render.go` — table rendering, identity footer
- Create: `internal/tui/projects_view_test.go` — view tests
- Create: `internal/tui/testdata/golden/projects-view-wide.txt` — wide golden
- Create: `internal/tui/testdata/golden/projects-view-narrow.txt` — narrow golden
- Modify: `internal/tui/model.go` — add `viewProjects` enum value, `m.projectStats`, view branch in `View()`, route through new view in `Update`
- Modify: `internal/tui/messages.go` — extend `projectsLoadedMsg` to carry stats
- Modify: `internal/tui/keymap.go` — rename `SwitchProject` → `Projects`
- Modify: `internal/tui/help.go` — update "Global" section to use `Projects`
- Modify: `internal/tui/run.go` — refactor `bootResolveScope` to return `(scope, viewID, error)`; clean up stale comment block
- Modify: `internal/tui/run_test.go` — add `TestBoot_UnresolvedWithProjects_LandsViewProjects`
- Modify: `internal/tui/sse_update.go` (or `model.go` — wherever the SSE event router lives) — flag `m.projectsStale` on relevant events
- Modify: `internal/tui/sse_update_test.go` — add `TestProjectsView_StaleOnIssueCreated`, debounce/inactive cases
- Modify: `internal/tui/testdata/golden/help-wide.txt`, `help-narrow.txt` — refresh

**Modal retirement (last)**
- Delete: most of `internal/tui/scope.go` (keep only the bootResolveScope notes worth preserving — the scope struct itself stays, just the picker pieces go)
- Modify: `internal/tui/quit_modal.go` — drop `modalProjectPicker` enum value
- Modify: `internal/tui/scope_test.go` — delete `TestProjectPicker_*` tests

**e2e**
- Create: `e2e/projects_view_test.go` — `TestSmoke_ProjectsViewLoop`

---

## Task 1: DB layer — `BatchProjectStats`

Implement the pre-aggregated CTE query from spec §6.1. Returns one row per active (non-archived) project with open/closed counts and `last_event_at`. Projects with zero issues / zero events still appear with zeroes / nil.

**Files:**
- Modify: `internal/db/types.go`
- Modify: `internal/db/queries.go` (add the new function)
- Modify: `internal/db/queries_projects_test.go`

- [ ] **Step 1: Define the `ProjectStats` type**

Add to `internal/db/types.go` near the other project types (~line 18):

```go
// ProjectStats is the per-project aggregate returned by BatchProjectStats.
// Used by GET /api/v1/projects?include=stats. LastEventAt is nil for a
// project with zero events; tests pin this so the TUI's "—" rendering
// is exercised.
type ProjectStats struct {
    Open        int
    Closed      int
    LastEventAt *time.Time
}
```

- [ ] **Step 2: Write the failing test for the empty-projects case**

Add to `internal/db/queries_projects_test.go`:

```go
func TestBatchProjectStats_EmptyProjectReturnsZeroes(t *testing.T) {
    d := openTestDB(t)
    ctx := context.Background()
    p, err := d.CreateProject(ctx, "github.com/wesm/empty", "empty")
    require.NoError(t, err)

    stats, err := d.BatchProjectStats(ctx)
    require.NoError(t, err)

    require.Contains(t, stats, p.ID)
    s := stats[p.ID]
    assert.Equal(t, 0, s.Open)
    assert.Equal(t, 0, s.Closed)
    assert.Nil(t, s.LastEventAt, "no events → LastEventAt is nil")
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/db/ -run TestBatchProjectStats_EmptyProjectReturnsZeroes -count=1 -v`
Expected: FAIL with `d.BatchProjectStats undefined`

- [ ] **Step 4: Implement `BatchProjectStats`**

Add to `internal/db/queries.go` after `ListProjects` (~line 110):

```go
// BatchProjectStats returns aggregate stats for every active project. The
// result includes projects with zero issues (Open=0, Closed=0) and zero
// events (LastEventAt=nil), driven by LEFT JOINs onto pre-aggregated
// subqueries. Pre-aggregation matters: the naive
// projects⋈issues⋈events GROUP BY shape would multiply each issue row by
// each event row and inflate counts. Spec §6.1.
func (d *DB) BatchProjectStats(ctx context.Context) (map[int64]ProjectStats, error) {
    const q = `
WITH
  issue_counts AS (
    SELECT
      project_id,
      SUM(CASE WHEN status = 'open'   THEN 1 ELSE 0 END) AS open_count,
      SUM(CASE WHEN status = 'closed' THEN 1 ELSE 0 END) AS closed_count
    FROM issues
    WHERE deleted_at IS NULL
    GROUP BY project_id
  ),
  event_max AS (
    SELECT project_id, MAX(created_at) AS last_event_at
    FROM events
    GROUP BY project_id
  )
SELECT
  p.id,
  COALESCE(ic.open_count,   0),
  COALESCE(ic.closed_count, 0),
  em.last_event_at
FROM projects p
LEFT JOIN issue_counts ic ON ic.project_id = p.id
LEFT JOIN event_max    em ON em.project_id = p.id
WHERE p.deleted_at IS NULL
ORDER BY p.id`
    rows, err := d.QueryContext(ctx, q)
    if err != nil {
        return nil, fmt.Errorf("batch project stats: %w", err)
    }
    defer func() { _ = rows.Close() }()
    out := map[int64]ProjectStats{}
    for rows.Next() {
        var (
            id     int64
            open   int
            closed int
            ts     sql.NullTime
        )
        if err := rows.Scan(&id, &open, &closed, &ts); err != nil {
            return nil, fmt.Errorf("scan project stats: %w", err)
        }
        s := ProjectStats{Open: open, Closed: closed}
        if ts.Valid {
            t := ts.Time
            s.LastEventAt = &t
        }
        out[id] = s
    }
    if err := rows.Err(); err != nil {
        return nil, fmt.Errorf("rows: %w", err)
    }
    return out, nil
}
```

You will need to add `"time"` to `internal/db/types.go` imports if it isn't already there (it is — `Issue.CreatedAt time.Time`).

- [ ] **Step 5: Verify the empty-project test passes**

Run: `go test ./internal/db/ -run TestBatchProjectStats_EmptyProjectReturnsZeroes -count=1 -v`
Expected: PASS

- [ ] **Step 6: Add the inflation guard test**

Append to `internal/db/queries_projects_test.go`:

```go
// TestBatchProjectStats_NoCountInflation pins the spec §6.1 contract:
// the issues-and-events join MUST be pre-aggregated, otherwise N issues
// times M events would inflate counts. Three issues + four events on the
// same project must still report Open=3.
func TestBatchProjectStats_NoCountInflation(t *testing.T) {
    d := openTestDB(t)
    ctx := context.Background()
    p, err := d.CreateProject(ctx, "github.com/wesm/proj", "proj")
    require.NoError(t, err)
    for i := 0; i < 3; i++ {
        _, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
            ProjectID: p.ID,
            Title:     "i",
            Body:      "",
            Author:    "tester",
        })
        require.NoError(t, err)
    }
    // CreateIssue inserted 3 events already; create 1 more (a comment) so
    // events outnumber issues.
    iss, err := d.IssueByNumber(ctx, p.ID, 1)
    require.NoError(t, err)
    _, _, err = d.CreateComment(ctx, db.CreateCommentParams{
        IssueID: iss.ID,
        Author:  "tester",
        Body:    "note",
    })
    require.NoError(t, err)

    stats, err := d.BatchProjectStats(ctx)
    require.NoError(t, err)
    require.Contains(t, stats, p.ID)
    assert.Equal(t, 3, stats[p.ID].Open, "must not inflate by event count")
    assert.Equal(t, 0, stats[p.ID].Closed)
    assert.NotNil(t, stats[p.ID].LastEventAt)
}
```

- [ ] **Step 7: Run inflation guard**

Run: `go test ./internal/db/ -run TestBatchProjectStats_NoCountInflation -count=1 -v`
Expected: PASS (the CTE query already protects against this; the test pins it)

- [ ] **Step 8: Add the soft-delete and archived-project guards**

Append:

```go
// TestBatchProjectStats_ExcludesSoftDeletedIssues pins that issues with
// deleted_at != NULL do not count toward Open/Closed. Spec §6.1.
func TestBatchProjectStats_ExcludesSoftDeletedIssues(t *testing.T) {
    d := openTestDB(t)
    ctx := context.Background()
    p, err := d.CreateProject(ctx, "github.com/wesm/proj", "proj")
    require.NoError(t, err)
    _, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
        ProjectID: p.ID, Title: "live", Body: "", Author: "tester",
    })
    require.NoError(t, err)
    soft, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
        ProjectID: p.ID, Title: "soft", Body: "", Author: "tester",
    })
    require.NoError(t, err)
    _, err = d.PurgeIssue(ctx, soft.ID, "tester")
    require.NoError(t, err)

    stats, err := d.BatchProjectStats(ctx)
    require.NoError(t, err)
    assert.Equal(t, 1, stats[p.ID].Open, "purged issue must not count")
}

// TestBatchProjectStats_ExcludesArchivedProjects pins that archived
// projects don't appear in the result map at all. Spec §6.1.
func TestBatchProjectStats_ExcludesArchivedProjects(t *testing.T) {
    d := openTestDB(t)
    ctx := context.Background()
    live, err := d.CreateProject(ctx, "github.com/wesm/live", "live")
    require.NoError(t, err)
    arch, err := d.CreateProject(ctx, "github.com/wesm/arch", "arch")
    require.NoError(t, err)
    _, err = d.RemoveProject(ctx, arch.ID, "tester", false)
    require.NoError(t, err)

    stats, err := d.BatchProjectStats(ctx)
    require.NoError(t, err)
    assert.Contains(t, stats, live.ID)
    assert.NotContains(t, stats, arch.ID)
}
```

If `PurgeIssue` doesn't exist by that name, look for the existing soft-delete entrypoint — likely `db.SoftDeleteIssue` or similar. Use whatever is current in the codebase.

- [ ] **Step 9: Run guards**

Run: `go test ./internal/db/ -run 'TestBatchProjectStats_(Excludes|NoCount)' -count=1 -v`
Expected: PASS

- [ ] **Step 10: Add the multi-project partition test**

Append:

```go
// TestBatchProjectStats_PartitionsByProject pins that two projects with
// distinct issue counts produce distinct rows; counts are not summed
// across projects. Spec §6.1.
func TestBatchProjectStats_PartitionsByProject(t *testing.T) {
    d := openTestDB(t)
    ctx := context.Background()
    a, err := d.CreateProject(ctx, "github.com/wesm/a", "a")
    require.NoError(t, err)
    b, err := d.CreateProject(ctx, "github.com/wesm/b", "b")
    require.NoError(t, err)
    for i := 0; i < 2; i++ {
        _, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
            ProjectID: a.ID, Title: "x", Author: "tester",
        })
        require.NoError(t, err)
    }
    _, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
        ProjectID: b.ID, Title: "y", Author: "tester",
    })
    require.NoError(t, err)

    stats, err := d.BatchProjectStats(ctx)
    require.NoError(t, err)
    assert.Equal(t, 2, stats[a.ID].Open)
    assert.Equal(t, 1, stats[b.ID].Open)
}
```

- [ ] **Step 11: Run partition test + full DB suite**

Run: `go test ./internal/db/ -count=1`
Expected: PASS (all DB tests, including the four new BatchProjectStats tests)

- [ ] **Step 12: Commit**

```bash
git add internal/db/types.go internal/db/queries.go internal/db/queries_projects_test.go
git commit -m "$(cat <<'EOF'
db: add BatchProjectStats with pre-aggregated CTE query

Returns per-project {open, closed, last_event_at} for every active
project. Pre-aggregating issue counts and event-max independently
avoids the row-multiplication that a naive projects⋈issues⋈events
GROUP BY would produce.

Tests pin the empty-project, soft-delete, archived, multi-project,
and inflation-guard cases per spec §6.1.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: API — `ProjectOut` projection (no stats yet)

Introduce `ProjectOut` and route every project-returning response through it. This task does **not** add `?include=stats`; that's Task 3. Keeping the projection separate from the stats wiring keeps each commit's diff focused and lets the JSON-snapshot tests catch any default-shape regression cleanly.

**Files:**
- Modify: `internal/api/types.go`
- Modify: `internal/daemon/handlers_projects.go`
- Modify: `internal/daemon/handlers_projects_test.go`

- [ ] **Step 1: Add `ProjectOut` and `ProjectStatsOut` types**

Add near the top of `internal/api/types.go`'s project section (just before `ResolveProjectRequest`):

```go
// ProjectStatsOut is the per-project aggregate returned by GET
// /api/v1/projects?include=stats. LastEventAt is nil for a project with
// zero events. Spec §7.2.
type ProjectStatsOut struct {
    Open        int        `json:"open"`
    Closed      int        `json:"closed"`
    LastEventAt *time.Time `json:"last_event_at"`
}

// ProjectOut is the API-shape of a project. JSON keys mirror db.Project
// (internal/db/types.go) exactly so the default response is byte-identical
// to the previous shape. Spec §1.7.
//
// Field set is exhaustively derived from db.Project as of this commit:
// id, uid, identity, name, created_at, next_issue_number, deleted_at
// (omitempty). No updated_at — db.Project has none.
type ProjectOut struct {
    ID              int64      `json:"id"`
    UID             string     `json:"uid"`
    Identity        string     `json:"identity"`
    Name            string     `json:"name"`
    CreatedAt       time.Time  `json:"created_at"`
    NextIssueNumber int64      `json:"next_issue_number"`
    DeletedAt       *time.Time `json:"deleted_at,omitempty"`

    // Stats is populated only when the request carries ?include=stats.
    Stats *ProjectStatsOut `json:"stats,omitempty"`
}
```

You will likely need to add `"time"` to the imports of `internal/api/types.go` if it isn't already there.

- [ ] **Step 2: Replace `db.Project` references in the five response types**

In `internal/api/types.go`, change five places:

```go
// 1. ProjectResolveBody (~line 49)
type ProjectResolveBody struct {
    Project       ProjectOut      `json:"project"`
    Alias         db.ProjectAlias `json:"alias"`
    WorkspaceRoot string          `json:"workspace_root,omitempty"`
}

// 2. ListProjectsResponse (~line 80)
type ListProjectsResponse struct {
    Body struct {
        Projects []ProjectOut `json:"projects"`
    }
}

// 3. ShowProjectResponse (~line 87)
type ShowProjectResponse struct {
    Body struct {
        Project ProjectOut        `json:"project"`
        Aliases []db.ProjectAlias `json:"aliases"`
    }
}

// 4. ResetCounterResponse (~line 128)
type ResetCounterResponse struct {
    Body struct {
        Project ProjectOut `json:"project"`
    }
}

// 5. RemoveProjectResponse (~line 353)
type RemoveProjectResponse struct {
    Body struct {
        Project ProjectOut `json:"project"`
        Event   *db.Event  `json:"event"`
    }
}
```

This will break compilation in `internal/daemon/handlers_projects.go` — that's expected; the next step fixes it.

- [ ] **Step 3: Add the `dbProjectToOut` helper**

Add to the top of `internal/daemon/handlers_projects.go`, just below the imports:

```go
// dbProjectToOut maps a db.Project (internal row) to the API-shape
// ProjectOut. Stats stays nil — that field is populated only by the
// list-projects handler when ?include=stats is set.
func dbProjectToOut(p db.Project) api.ProjectOut {
    return api.ProjectOut{
        ID:              p.ID,
        UID:             p.UID,
        Identity:        p.Identity,
        Name:            p.Name,
        CreatedAt:       p.CreatedAt,
        NextIssueNumber: p.NextIssueNumber,
        DeletedAt:       p.DeletedAt,
    }
}
```

- [ ] **Step 4: Route every project-returning handler through `dbProjectToOut`**

Walk every site in `internal/daemon/handlers_projects.go` that assigns a `db.Project` to a response field and convert it:

- The list-projects handler (~line 53) currently does `out.Body.Projects = ps` where `ps []db.Project`. Replace with a loop:
  ```go
  outs := make([]api.ProjectOut, len(ps))
  for i, p := range ps {
      outs[i] = dbProjectToOut(p)
  }
  out.Body.Projects = outs
  ```
- The reset-counter handler (~line 94) `out.Body.Project = p` becomes `out.Body.Project = dbProjectToOut(p)`.
- The show-project handler (~line 112) similar.
- The init-project handler — look for whatever populates `ProjectResolveBody.Project`; that path may be in a helper called by both `init` and `resolve`. Walk it from the handler that sets `*out` of `*api.ProjectResolveBody`.
- The remove-project handler similar.
- The merge-project handler returns `MergeProjectResponse{ Body: db.ProjectMergeResult }` — `MergeProjectResult` likely contains a `Project` field; check if it leaks `db.Project`. If so, map at the handler edge.

Use grep to confirm: `grep -n "db\.Project\b" internal/daemon/handlers_projects.go` — every match must either be the helper definition or fully eliminated.

- [ ] **Step 5: Build to verify no compile errors**

Run: `go build ./...`
Expected: builds cleanly. If there are errors in `cmd/kata` or other consumers of the API types, those consumers were reading the typed response directly and need their own updates. (cmd/kata uses raw HTTP unmarshal into local types — confirm by `grep -n "api\.ListProjectsResponse" cmd/kata/`. If a CLI command was unmarshalling into `api.ListProjectsResponse` directly, swap to the matching `ProjectOut` structure or to a local CLI struct.)

- [ ] **Step 6: Write the JSON-snapshot test for the default `ListProjects` shape**

Add to `internal/daemon/handlers_projects_test.go`:

```go
// TestListProjects_DefaultShape pins the byte-level wire shape of
// GET /api/v1/projects. A future addition of a field to db.Project
// (e.g. an internal-only column) must not silently leak onto this
// response. Spec §7.2.
func TestListProjects_DefaultShape(t *testing.T) {
    ts := newTestServer(t)
    // CreateProject uses ProjectByIdentity which goes through the daemon's
    // resolve flow; for a focused snapshot test we hit the daemon DB
    // directly via the test handle.
    h := openTestDB(t)
    _, err := h.db.CreateProject(t.Context(), "github.com/wesm/x", "x")
    require.NoError(t, err)
    srv := daemon.NewServer(daemon.ServerConfig{DB: h.db, StartedAt: h.now})
    ts2 := httptest.NewServer(srv.Handler())
    t.Cleanup(ts2.Close)

    body := getBody(t, ts2, "/api/v1/projects")
    var parsed struct {
        Projects []map[string]any `json:"projects"`
    }
    require.NoError(t, json.Unmarshal([]byte(body), &parsed))
    require.Len(t, parsed.Projects, 1)
    p := parsed.Projects[0]

    // Required keys; assert by membership not equality so created_at and
    // numeric values aren't pinned.
    for _, key := range []string{"id", "uid", "identity", "name", "created_at", "next_issue_number"} {
        _, ok := p[key]
        assert.True(t, ok, "missing key %q in projects[0]: %s", key, body)
    }
    // No "stats" without ?include=stats.
    _, hasStats := p["stats"]
    assert.False(t, hasStats, "stats must not appear in default response: %s", body)
    // No "updated_at" — db.Project has none.
    _, hasUpdated := p["updated_at"]
    assert.False(t, hasUpdated, "updated_at must not appear: %s", body)
    // No "deleted_at" for an active project — it's omitempty.
    _, hasDeleted := p["deleted_at"]
    assert.False(t, hasDeleted, "deleted_at must omit on active project: %s", body)

    _ = ts // suppress unused
}
```

The test fixture is a little awkward because `openTestDB` returns `testDBHandle` while `newTestServer` constructs its own — feel free to refactor `newTestServer` to take a handle, or just duplicate-construct the server here.

- [ ] **Step 7: Run snapshot test**

Run: `go test ./internal/daemon/ -run TestListProjects_DefaultShape -count=1 -v`
Expected: PASS

- [ ] **Step 8: Run the full daemon suite to catch regressions**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS (every existing project test must still pass — this is the "byte-identical default" contract working)

- [ ] **Step 9: Commit**

```bash
git add internal/api/types.go internal/daemon/handlers_projects.go internal/daemon/handlers_projects_test.go
git commit -m "$(cat <<'EOF'
api: project responses use ProjectOut, not raw db.Project

Replaces db.Project across all five project-returning response types
(resolve, init, list, show, reset-counter, remove) with a new
api.ProjectOut. Wire shape is byte-identical for active projects;
db.Project becomes free to evolve internally.

Lays the groundwork for ?include=stats (next commit) without polluting
the projection-only diff.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: API — `?include=stats` on `GET /api/v1/projects`

Wire the new query parameter through the list-projects handler. Stats are populated by calling `db.BatchProjectStats` (Task 1) and stitched onto each `ProjectOut`. Default response (no query param) stays unchanged.

**Files:**
- Modify: `internal/daemon/handlers_projects.go`
- Modify: `internal/daemon/handlers_projects_test.go`

- [ ] **Step 1: Write the failing test for `?include=stats`**

Add to `internal/daemon/handlers_projects_test.go`:

```go
// TestListProjects_WithStatsIncludesAggregates pins the new wire
// contract: ?include=stats returns a stats triple per project. Spec §7.1.
func TestListProjects_WithStatsIncludesAggregates(t *testing.T) {
    h := openTestDB(t)
    ctx := t.Context()
    p, err := h.db.CreateProject(ctx, "github.com/wesm/x", "x")
    require.NoError(t, err)
    for i := 0; i < 3; i++ {
        _, _, err := h.db.CreateIssue(ctx, db.CreateIssueParams{
            ProjectID: p.ID, Title: "i", Author: "tester",
        })
        require.NoError(t, err)
    }
    srv := daemon.NewServer(daemon.ServerConfig{DB: h.db, StartedAt: h.now})
    ts := httptest.NewServer(srv.Handler())
    t.Cleanup(ts.Close)

    body := getBody(t, ts, "/api/v1/projects?include=stats")
    var parsed struct {
        Projects []struct {
            ID    int64 `json:"id"`
            Stats *struct {
                Open        int     `json:"open"`
                Closed      int     `json:"closed"`
                LastEventAt *string `json:"last_event_at"`
            } `json:"stats"`
        } `json:"projects"`
    }
    require.NoError(t, json.Unmarshal([]byte(body), &parsed))
    require.Len(t, parsed.Projects, 1)
    require.NotNil(t, parsed.Projects[0].Stats, "stats present with ?include=stats")
    assert.Equal(t, 3, parsed.Projects[0].Stats.Open)
    assert.Equal(t, 0, parsed.Projects[0].Stats.Closed)
    require.NotNil(t, parsed.Projects[0].Stats.LastEventAt)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestListProjects_WithStatsIncludesAggregates -count=1 -v`
Expected: FAIL — `parsed.Projects[0].Stats` is nil because the handler ignores `?include=stats` today.

- [ ] **Step 3: Extend the listProjects handler input shape**

In `internal/daemon/handlers_projects.go`, the listProjects handler currently takes `*struct{}` as input. Switch it to a typed input that captures `?include=stats`:

```go
huma.Register(humaAPI, huma.Operation{
    OperationID: "listProjects",
    Method:      "GET",
    Path:        "/api/v1/projects",
}, func(ctx context.Context, in *struct {
    Include string `query:"include"`
}) (*api.ListProjectsResponse, error) {
    ps, err := cfg.DB.ListProjects(ctx)
    if err != nil {
        return nil, api.NewError(500, "internal", err.Error(), "", nil)
    }
    outs := make([]api.ProjectOut, len(ps))
    for i, p := range ps {
        outs[i] = dbProjectToOut(p)
    }
    if includeContains(in.Include, "stats") {
        stats, err := cfg.DB.BatchProjectStats(ctx)
        if err != nil {
            return nil, api.NewError(500, "internal", err.Error(), "", nil)
        }
        for i, p := range ps {
            if s, ok := stats[p.ID]; ok {
                outs[i].Stats = &api.ProjectStatsOut{
                    Open:        s.Open,
                    Closed:      s.Closed,
                    LastEventAt: s.LastEventAt,
                }
            }
        }
    }
    out := &api.ListProjectsResponse{}
    out.Body.Projects = outs
    return out, nil
})
```

Add `includeContains` near the helpers in the same file:

```go
// includeContains reports whether the comma-separated ?include= value
// names the given token. Whitespace is trimmed; matching is case-
// insensitive on the token side. Spec §7.1.
func includeContains(includeParam, token string) bool {
    for _, part := range strings.Split(includeParam, ",") {
        if strings.EqualFold(strings.TrimSpace(part), token) {
            return true
        }
    }
    return false
}
```

You may need to add `"strings"` to the imports.

- [ ] **Step 4: Run the failing test**

Run: `go test ./internal/daemon/ -run TestListProjects_WithStatsIncludesAggregates -count=1 -v`
Expected: PASS

- [ ] **Step 5: Add the empty-project edge case**

Append:

```go
// TestListProjects_WithStatsHandlesEmptyProjects pins that a project
// with zero issues and zero events serializes Open=0, Closed=0,
// LastEventAt=null. Spec §7.1.
func TestListProjects_WithStatsHandlesEmptyProjects(t *testing.T) {
    h := openTestDB(t)
    _, err := h.db.CreateProject(t.Context(), "github.com/wesm/empty", "empty")
    require.NoError(t, err)
    srv := daemon.NewServer(daemon.ServerConfig{DB: h.db, StartedAt: h.now})
    ts := httptest.NewServer(srv.Handler())
    t.Cleanup(ts.Close)

    body := getBody(t, ts, "/api/v1/projects?include=stats")
    var parsed struct {
        Projects []struct {
            Stats struct {
                Open        int     `json:"open"`
                Closed      int     `json:"closed"`
                LastEventAt *string `json:"last_event_at"`
            } `json:"stats"`
        } `json:"projects"`
    }
    require.NoError(t, json.Unmarshal([]byte(body), &parsed))
    require.Len(t, parsed.Projects, 1)
    assert.Equal(t, 0, parsed.Projects[0].Stats.Open)
    assert.Equal(t, 0, parsed.Projects[0].Stats.Closed)
    assert.Nil(t, parsed.Projects[0].Stats.LastEventAt, "no events → null")
}
```

- [ ] **Step 6: Add the default-shape-still-unchanged test**

Append:

```go
// TestListProjects_DefaultShapeUnchangedAfterStats pins that
// GET /api/v1/projects (no query) still emits no stats key after Task 3
// — backwards-compat for kata projects list. Spec §7.1.
func TestListProjects_DefaultShapeUnchangedAfterStats(t *testing.T) {
    h := openTestDB(t)
    _, err := h.db.CreateProject(t.Context(), "github.com/wesm/x", "x")
    require.NoError(t, err)
    srv := daemon.NewServer(daemon.ServerConfig{DB: h.db, StartedAt: h.now})
    ts := httptest.NewServer(srv.Handler())
    t.Cleanup(ts.Close)

    body := getBody(t, ts, "/api/v1/projects")
    var parsed struct {
        Projects []map[string]any `json:"projects"`
    }
    require.NoError(t, json.Unmarshal([]byte(body), &parsed))
    require.Len(t, parsed.Projects, 1)
    _, has := parsed.Projects[0]["stats"]
    assert.False(t, has, "stats must omit without ?include=stats")
}
```

- [ ] **Step 7: Run the full daemon suite**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/daemon/handlers_projects.go internal/daemon/handlers_projects_test.go
git commit -m "$(cat <<'EOF'
api: GET /api/v1/projects?include=stats returns per-project stats

Adds the optional include query parameter. When stats is requested, each
ProjectOut row carries a Stats triple computed by db.BatchProjectStats.
Default response (no query) is byte-identical to before.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: TUI client — `ListProjectsWithStats`

Wire the typed client. The existing `ListProjects` (no-stats) stays — used by the boot project-name cache in `fetchProjects`.

**Files:**
- Modify: `internal/tui/client_types.go`
- Modify: `internal/tui/client.go`
- Modify: `internal/tui/client_test.go`

- [ ] **Step 1: Add the typed shape**

Add to `internal/tui/client_types.go` near `ProjectSummary` (~line 139):

```go
// ProjectStatsSummary is the per-project aggregate carried by
// /api/v1/projects?include=stats. LastEventAt is nil for a project with
// zero events. Spec §7.2.
type ProjectStatsSummary struct {
    Open        int        `json:"open"`
    Closed      int        `json:"closed"`
    LastEventAt *time.Time `json:"last_event_at"`
}

// ProjectSummaryWithStats extends ProjectSummary with the stats triple.
// The boot project-name cache uses ProjectSummary; viewProjects uses
// this shape.
type ProjectSummaryWithStats struct {
    ProjectSummary
    Stats *ProjectStatsSummary `json:"stats,omitempty"`
}
```

If `client_types.go` doesn't already import `"time"`, add it. (It does — `CommentEntry.CreatedAt time.Time`.)

- [ ] **Step 2: Write the failing decode test**

Add to `internal/tui/client_test.go`:

```go
// TestClient_ListProjectsWithStats_Decodes pins that the typed client
// decodes the ?include=stats wire shape into ProjectSummaryWithStats,
// including the optional Stats field. Spec §7.3.
func TestClient_ListProjectsWithStats_Decodes(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        require.Equal(t, "/api/v1/projects", r.URL.Path)
        require.Equal(t, "stats", r.URL.Query().Get("include"))
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write([]byte(`{
            "projects": [
                {"id": 7, "identity": "github.com/wesm/x", "name": "x",
                 "stats": {"open": 3, "closed": 1, "last_event_at": "2026-05-04T12:00:00.000Z"}},
                {"id": 9, "identity": "github.com/wesm/empty", "name": "empty",
                 "stats": {"open": 0, "closed": 0, "last_event_at": null}}
            ]
        }`))
    }))
    defer srv.Close()
    c := NewClient(srv.URL, srv.Client())

    got, err := c.ListProjectsWithStats(t.Context())
    require.NoError(t, err)
    require.Len(t, got, 2)

    require.NotNil(t, got[0].Stats)
    assert.Equal(t, 3, got[0].Stats.Open)
    assert.Equal(t, 1, got[0].Stats.Closed)
    require.NotNil(t, got[0].Stats.LastEventAt)

    require.NotNil(t, got[1].Stats)
    assert.Equal(t, 0, got[1].Stats.Open)
    assert.Nil(t, got[1].Stats.LastEventAt, "null wire → nil pointer")
}
```

- [ ] **Step 3: Run test, expect failure**

Run: `go test ./internal/tui/ -run TestClient_ListProjectsWithStats_Decodes -count=1 -v`
Expected: FAIL — `c.ListProjectsWithStats undefined`

- [ ] **Step 4: Implement `ListProjectsWithStats`**

Add to `internal/tui/client.go` immediately after `ListProjects` (~line 196):

```go
// ListProjectsWithStats returns every active project with per-project
// aggregates {open, closed, last_event_at} populated. Used by the
// projects view. Spec §7.3.
func (c *Client) ListProjectsWithStats(ctx context.Context) ([]ProjectSummaryWithStats, error) {
    var resp struct {
        Projects []ProjectSummaryWithStats `json:"projects"`
    }
    if err := c.do(ctx, http.MethodGet, "/api/v1/projects?include=stats", nil, &resp); err != nil {
        return nil, err
    }
    return resp.Projects, nil
}
```

- [ ] **Step 5: Run test, expect pass**

Run: `go test ./internal/tui/ -run TestClient_ListProjectsWithStats_Decodes -count=1 -v`
Expected: PASS

- [ ] **Step 6: Add the not-nil-on-success regression test**

Append to `client_test.go` (mirrors the existing `TestClient_ListAllIssues_NotNilOnSuccess` shape):

```go
// TestClient_ListProjectsWithStats_NotNilOnSuccess pins the same
// regression covered for ListIssues / ListAllIssues: a 200 with an empty
// array returns []ProjectSummaryWithStats{}, never nil — callers iterate
// without nil-checks. Spec §7.3.
func TestClient_ListProjectsWithStats_NotNilOnSuccess(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write([]byte(`{"projects": []}`))
    }))
    defer srv.Close()
    c := NewClient(srv.URL, srv.Client())

    got, err := c.ListProjectsWithStats(t.Context())
    require.NoError(t, err)
    require.NotNil(t, got)
    assert.Len(t, got, 0)
}
```

- [ ] **Step 7: Run full TUI client tests**

Run: `go test ./internal/tui/ -run 'TestClient_' -count=1`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/tui/client_types.go internal/tui/client.go internal/tui/client_test.go
git commit -m "$(cat <<'EOF'
tui: add Client.ListProjectsWithStats

Typed client for GET /api/v1/projects?include=stats. Returns
ProjectSummaryWithStats rows with optional Stats triple. The boot
project-name cache continues to use ListProjects (no stats); only the
projects view reaches for stats data.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: viewProjects scaffold — enum, model fields, render stub

Introduce the new view as a renderable entry point with placeholder content. Key handling and live data wiring come in Tasks 6-8. After this task, `m.view = viewProjects` produces a frame that says "projects view" without crashing.

**Files:**
- Modify: `internal/tui/model.go` (add enum value, model field)
- Modify: `internal/tui/messages.go` (extend `projectsLoadedMsg`)
- Create: `internal/tui/projects_view.go`
- Create: `internal/tui/projects_view_render.go`
- Create: `internal/tui/projects_view_test.go`

- [ ] **Step 1: Add the `viewProjects` enum value**

In `internal/tui/model.go` (~line 19):

```go
const (
    viewList viewID = iota
    viewDetail
    viewHelp
    viewEmpty
    viewProjects
)
```

- [ ] **Step 2: Add the `projectStats` cache to `Model`**

Find the existing `projectsByID map[int64]string` field in `Model` (~line 231) and add immediately after:

```go
// projectStats is the per-project aggregate cache populated by
// fetchProjectsWithStats. nil-safe: viewProjects renders rows for
// projects in projectsByID even if their stats haven't loaded yet
// (counts render as zeroes / "—" until the message lands).
projectStats map[int64]ProjectStatsSummary
```

Also initialize it in `initialModel` alongside `projectsByID`:

```go
projectsByID:  map[int64]string{},
projectStats:  map[int64]ProjectStatsSummary{},
```

- [ ] **Step 3: Extend `projectsLoadedMsg` to carry stats**

In `internal/tui/messages.go` (~line 184):

```go
// projectsLoadedMsg is delivered after a /api/v1/projects fetch returns.
// The all-projects list view uses the projects map to prefix each row's
// title with the owning project's display name. The stats map is
// populated only by fetchProjectsWithStats; the boot fetchProjects cmd
// leaves it nil so callers can distinguish "names only" vs "with stats".
type projectsLoadedMsg struct {
    projects map[int64]string
    stats    map[int64]ProjectStatsSummary
    err      error
}
```

- [ ] **Step 4: Update the existing `projectsLoadedMsg` handler in `model.go`**

Find `if pl, ok := msg.(projectsLoadedMsg); ok {` (~line 334) and confirm it copies `pl.projects` into `m.projectsByID`. If `pl.stats != nil`, also copy into `m.projectStats`. If you add it now, you stay forward-compatible with Task 6 even before `fetchProjectsWithStats` exists. Inspect the existing block:

```go
if pl, ok := msg.(projectsLoadedMsg); ok {
    if pl.err == nil {
        m.projectsByID = pl.projects
        if pl.stats != nil {
            m.projectStats = pl.stats
        }
    }
    // ... existing tail
    return m, nil
}
```

(Adapt to the actual structure of the existing handler.)

- [ ] **Step 5: Write the render stub test**

Create `internal/tui/projects_view_test.go`:

```go
package tui

import (
    "strings"
    "testing"
)

// TestProjectsView_RendersWithoutPanic confirms the view renders a
// non-empty frame for a model in viewProjects state, even with no
// projects loaded yet. Required for boot landing where the fetch is
// still in flight.
func TestProjectsView_RendersWithoutPanic(t *testing.T) {
    m := initialModel(Options{})
    m.view = viewProjects
    m.width = 80
    m.height = 24

    out := m.View()
    if out == "" {
        t.Fatal("viewProjects must render a non-empty frame")
    }
    if !strings.Contains(out, "projects") {
        t.Errorf("expected 'projects' in viewProjects output:\n%s", out)
    }
}
```

- [ ] **Step 6: Run the test, expect failure**

Run: `go test ./internal/tui/ -run TestProjectsView_RendersWithoutPanic -count=1 -v`
Expected: FAIL — `Model.View()` has no `viewProjects` branch yet, falls through to default render.

- [ ] **Step 7: Create the render stub file**

Create `internal/tui/projects_view_render.go`:

```go
package tui

import (
    "strings"

    "github.com/charmbracelet/lipgloss"
)

// renderProjects draws the project-table view. The full layout (table
// rows, sentinel, footer) is implemented in Task 7; this scaffold just
// produces a recognizable frame so View() can route to it.
func renderProjects(m Model) string {
    body := strings.Join([]string{
        titleStyle.Render("kata / projects"),
        "",
        subtleStyle.Render("(stub — table renders in Task 7)"),
    }, "\n")
    if m.width <= 0 || m.height <= 0 {
        return body
    }
    return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
}
```

- [ ] **Step 8: Wire `viewProjects` into `Model.View()`**

Find the current `View()` body in `model.go`. There is a narrow-terminal branch and a normal branch. Add a `viewProjects` case to **both** in shape:

For the normal-rendering branch (the one that produces the standard frame), add early — around the existing `if m.view == viewHelp` block:

```go
if m.view == viewProjects {
    return renderProjects(m)
}
```

The narrow branch likely has its own `viewEmpty` short-circuit; mirror it for `viewProjects` so a too-narrow terminal still gets the projects body without crashing into list-render code that expects a non-empty issue list.

- [ ] **Step 9: Run the render test, expect pass**

Run: `go test ./internal/tui/ -run TestProjectsView_RendersWithoutPanic -count=1 -v`
Expected: PASS

- [ ] **Step 10: Add the `fetchProjectsWithStats` cmd**

Create `internal/tui/projects_view.go`:

```go
package tui

import (
    "context"
    "time"

    tea "github.com/charmbracelet/bubbletea"
)

// fetchProjectsWithStats issues GET /api/v1/projects?include=stats and
// dispatches a projectsLoadedMsg carrying the stats map. The boot
// fetchProjects cmd is the no-stats variant used by the list view's
// project-name cache; this cmd is dispatched by every transition into
// viewProjects per spec §6.2.
//
// Failures populate err so the message handler can surface a toast
// without leaving the table empty.
func (m Model) fetchProjectsWithStats() tea.Cmd {
    api := m.api
    return func() tea.Msg {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        rows, err := api.ListProjectsWithStats(ctx)
        if err != nil {
            return projectsLoadedMsg{err: err}
        }
        names := make(map[int64]string, len(rows))
        stats := make(map[int64]ProjectStatsSummary, len(rows))
        for _, r := range rows {
            names[r.ID] = r.Name
            if r.Stats != nil {
                stats[r.ID] = *r.Stats
            }
        }
        return projectsLoadedMsg{projects: names, stats: stats}
    }
}
```

- [ ] **Step 11: Build to verify**

Run: `go build ./...`
Expected: builds cleanly.

- [ ] **Step 12: Run the full TUI suite**

Run: `go test ./internal/tui/ -count=1`
Expected: PASS

- [ ] **Step 13: Commit**

```bash
git add internal/tui/model.go internal/tui/messages.go internal/tui/projects_view.go internal/tui/projects_view_render.go internal/tui/projects_view_test.go
git commit -m "$(cat <<'EOF'
tui: add viewProjects scaffold + fetchProjectsWithStats cmd

Adds the viewProjects view enum and a placeholder render branch so the
view can be entered without crashing, plus the fetchProjectsWithStats
tea.Cmd that populates m.projectStats via the new
?include=stats endpoint.

Real table layout, key handling, and entry transitions land in
subsequent commits.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: viewProjects rendering — table, sentinel summing, golden snapshots

Replace the stub render with the actual project table. The All-projects sentinel is computed client-side from the rows in `m.projectStats`. Identity footer renders below the table for the highlighted row.

**Files:**
- Modify: `internal/tui/projects_view.go` — add `projectsRow` type, `projectsRows` builder, sentinel summing
- Modify: `internal/tui/projects_view_render.go` — full layout
- Modify: `internal/tui/projects_view_test.go`
- Create: `internal/tui/testdata/golden/projects-view-wide.txt`
- Create: `internal/tui/testdata/golden/projects-view-narrow.txt`

- [ ] **Step 1: Define the row builder**

Append to `internal/tui/projects_view.go`:

```go
// projectsRow is one row of the projects view. Sentinel=true marks the
// implicit "All projects" entry at index 0; otherwise the row carries a
// concrete projectID.
type projectsRow struct {
    sentinel  bool
    projectID int64
    name      string
    identity  string
    stats     ProjectStatsSummary
}

// projectsRows builds the row list rendered by viewProjects. The
// sentinel row is always at index 0; remaining rows are sorted by
// last_event_at desc with name asc as the tiebreak. Spec §5.3.
//
// The sentinel's totals are client-summed from the per-row stats (spec
// §1.6) so the "All projects" Open/Closed/Total are guaranteed
// consistent with the rows on the same frame, and last_event_at is the
// max across rows.
func projectsRows(byID map[int64]string, identByID map[int64]string, stats map[int64]ProjectStatsSummary) []projectsRow {
    rows := []projectsRow{}
    for id, name := range byID {
        rows = append(rows, projectsRow{
            projectID: id,
            name:      name,
            identity:  identByID[id],
            stats:     stats[id],
        })
    }
    sort.SliceStable(rows, func(i, j int) bool {
        ti, tj := timeOrZero(rows[i].stats.LastEventAt), timeOrZero(rows[j].stats.LastEventAt)
        if !ti.Equal(tj) {
            return ti.After(tj)
        }
        return strings.ToLower(rows[i].name) < strings.ToLower(rows[j].name)
    })
    sentinel := projectsRow{sentinel: true, name: "All projects"}
    var maxT time.Time
    for _, r := range rows {
        sentinel.stats.Open += r.stats.Open
        sentinel.stats.Closed += r.stats.Closed
        if r.stats.LastEventAt != nil && r.stats.LastEventAt.After(maxT) {
            maxT = *r.stats.LastEventAt
        }
    }
    if !maxT.IsZero() {
        sentinel.stats.LastEventAt = &maxT
    }
    return append([]projectsRow{sentinel}, rows...)
}

// timeOrZero unwraps an optional time pointer, returning the zero value
// for nil. Sort uses this so a project with no events sinks to the end
// of the descending order.
func timeOrZero(t *time.Time) time.Time {
    if t == nil {
        return time.Time{}
    }
    return *t
}
```

Add `"sort"`, `"strings"`, `"time"` to the imports if missing.

You'll also need `identByID` — the source-of-truth for project identity strings. The existing TUI doesn't cache identity; today's `m.projectsByID` only holds names. **You need to also cache identity** as part of this task. Two options:

1. Add `m.projectIdentByID map[int64]string` and populate it in the `projectsLoadedMsg` handler from `pl.rows` (you'll need to add a third field carrying `[]ProjectSummaryWithStats` to the message — easier than yet another map).
2. Drop the identity footer from the v1 table and render it later when projects gain a richer cache.

Pick option 1. Update `projectsLoadedMsg`:

```go
type projectsLoadedMsg struct {
    projects map[int64]string
    idents   map[int64]string                  // NEW
    stats    map[int64]ProjectStatsSummary
    err      error
}
```

Update `Model` (alongside `projectsByID`):

```go
projectIdentByID map[int64]string
```

Initialize in `initialModel`:

```go
projectIdentByID: map[int64]string{},
```

Update `fetchProjectsWithStats` to populate `idents`:

```go
idents := make(map[int64]string, len(rows))
for _, r := range rows {
    names[r.ID] = r.Name
    idents[r.ID] = r.Identity
    // ...
}
return projectsLoadedMsg{projects: names, idents: idents, stats: stats}
```

Update the `projectsLoadedMsg` handler in `model.go` to also copy `pl.idents`.

- [ ] **Step 2: Write the sentinel-summing test**

Add to `internal/tui/projects_view_test.go`:

```go
// TestProjectsRows_SentinelSumsAndPinsFirst pins spec §1.6: the All-
// projects sentinel row's Open/Closed are the sum of per-row counts and
// LastEventAt is the row-max. The sentinel is always at index 0.
func TestProjectsRows_SentinelSumsAndPinsFirst(t *testing.T) {
    t1 := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
    t2 := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC) // newer
    byID := map[int64]string{1: "kata", 2: "roborev", 3: "msgvault"}
    idents := map[int64]string{1: "github.com/wesm/kata", 2: "...", 3: "..."}
    stats := map[int64]ProjectStatsSummary{
        1: {Open: 5, Closed: 2, LastEventAt: &t2},
        2: {Open: 3, Closed: 1, LastEventAt: &t1},
        3: {Open: 0, Closed: 0, LastEventAt: nil},
    }
    rows := projectsRows(byID, idents, stats)
    require.Len(t, rows, 4) // sentinel + 3 projects
    assert.True(t, rows[0].sentinel, "row 0 must be the sentinel")
    assert.Equal(t, 8, rows[0].stats.Open, "sentinel open = 5+3+0")
    assert.Equal(t, 3, rows[0].stats.Closed, "sentinel closed = 2+1+0")
    require.NotNil(t, rows[0].stats.LastEventAt)
    assert.True(t, rows[0].stats.LastEventAt.Equal(t2), "sentinel last = max(t1, t2) = t2")
}

// TestProjectsRows_SortByLastEventDesc pins spec §5.3: rows after the
// sentinel are sorted by last_event_at desc with name asc as the
// tiebreak. A row with no events sinks to the bottom.
func TestProjectsRows_SortByLastEventDesc(t *testing.T) {
    t1 := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
    t2 := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
    byID := map[int64]string{1: "older", 2: "newer", 3: "noevents"}
    idents := map[int64]string{1: "...", 2: "...", 3: "..."}
    stats := map[int64]ProjectStatsSummary{
        1: {LastEventAt: &t1},
        2: {LastEventAt: &t2},
        3: {LastEventAt: nil},
    }
    rows := projectsRows(byID, idents, stats)
    assert.True(t, rows[0].sentinel)
    assert.Equal(t, "newer", rows[1].name)
    assert.Equal(t, "older", rows[2].name)
    assert.Equal(t, "noevents", rows[3].name)
}
```

Add `"time"` and `"github.com/stretchr/testify/assert"` plus `require` to the test imports if missing.

- [ ] **Step 3: Run the row-builder tests, expect pass**

Run: `go test ./internal/tui/ -run TestProjectsRows_ -count=1 -v`
Expected: PASS

- [ ] **Step 4: Implement the table render**

Replace the body of `renderProjects` in `internal/tui/projects_view_render.go`:

```go
package tui

import (
    "fmt"
    "strings"
    "time"

    "github.com/charmbracelet/lipgloss"
    "github.com/mattn/go-runewidth"
)

// renderProjects draws the projects-view body: a 5-column table
// (Project / Open / Closed / Total / Updated), an All-projects sentinel
// pinned at row 0, and a 1-line identity footer for the highlighted
// row. Spec §5.
func renderProjects(m Model) string {
    rows := projectsRows(m.projectsByID, m.projectIdentByID, m.projectStats)
    cursor := m.projectsCursor
    if cursor >= len(rows) {
        cursor = len(rows) - 1
    }
    if cursor < 0 {
        cursor = 0
    }

    headerCells := []string{"Project", "Open", "Closed", "Total", "Updated"}
    body := []string{
        titleStyle.Render("kata / projects"),
        subtleStyle.Render(fmt.Sprintf("%d projects", len(rows)-1)),
        "",
        renderProjectsHeader(headerCells, m.width),
    }
    for i, r := range rows {
        body = append(body, renderProjectsRow(r, i == cursor, m.width))
    }
    body = append(body, "")
    if cursor >= 0 && cursor < len(rows) {
        body = append(body, subtleStyle.Render(footerForRow(rows[cursor], m.width)))
    }
    body = append(body, "")
    body = append(body, subtleStyle.Render(
        "[↑/↓ k/j] move  [enter] open  [esc] back  [r] refresh  [q] quit  [?] help"))

    if m.width <= 0 || m.height <= 0 {
        return strings.Join(body, "\n")
    }
    return strings.Join(body, "\n")
}

func renderProjectsHeader(cells []string, width int) string {
    // Fixed-width numeric columns; flexible Project column.
    return projectsRowLayout(cells[0], cells[1], cells[2], cells[3], cells[4], width, false)
}

func renderProjectsRow(r projectsRow, highlight bool, width int) string {
    name := r.name
    if r.sentinel {
        name = "All projects"
    }
    open := fmt.Sprintf("%d", r.stats.Open)
    closed := fmt.Sprintf("%d", r.stats.Closed)
    total := fmt.Sprintf("%d", r.stats.Open+r.stats.Closed)
    updated := relativeTimeOrDash(r.stats.LastEventAt)
    return projectsRowLayout(name, open, closed, total, updated, width, highlight)
}

// projectsRowLayout lays out the five columns with the Project column
// flexing and the four numeric/time columns fixed-width and right- or
// left-aligned per spec §5.2.
func projectsRowLayout(project, open, closed, total, updated string, width int, highlight bool) string {
    const (
        openW    = 6
        closedW  = 7
        totalW   = 6
        updatedW = 12
        gap      = 2
    )
    projectW := width - (openW + closedW + totalW + updatedW + 4*gap) - 2
    if projectW < 8 {
        projectW = 8
    }
    cursor := "  "
    if highlight {
        cursor = "▶ "
    }
    line := cursor + padR(project, projectW) +
        strings.Repeat(" ", gap) + padL(open, openW) +
        strings.Repeat(" ", gap) + padL(closed, closedW) +
        strings.Repeat(" ", gap) + padL(total, totalW) +
        strings.Repeat(" ", gap) + padR(updated, updatedW)
    if highlight {
        line = lipgloss.NewStyle().Bold(true).Render(line)
    }
    return line
}

// padR / padL truncate-with-ellipsis or right-pad / left-pad to width.
// Use runewidth for visible-cell counting; truncation prefers '…' over
// hard cut.
func padR(s string, w int) string {
    sw := runewidth.StringWidth(s)
    if sw == w {
        return s
    }
    if sw < w {
        return s + strings.Repeat(" ", w-sw)
    }
    return runewidth.Truncate(s, w, "…")
}
func padL(s string, w int) string {
    sw := runewidth.StringWidth(s)
    if sw == w {
        return s
    }
    if sw < w {
        return strings.Repeat(" ", w-sw) + s
    }
    return runewidth.Truncate(s, w, "…")
}

// relativeTimeOrDash formats t as "5m ago" / "2h ago" / "—" for nil.
// Spec §5.2 + §6.1: a project with zero events renders as em-dash.
func relativeTimeOrDash(t *time.Time) string {
    if t == nil {
        return "—"
    }
    d := time.Since(*t)
    switch {
    case d < time.Minute:
        return "just now"
    case d < time.Hour:
        return fmt.Sprintf("%dm ago", int(d.Minutes()))
    case d < 24*time.Hour:
        return fmt.Sprintf("%dh ago", int(d.Hours()))
    default:
        return fmt.Sprintf("%dd ago", int(d.Hours()/24))
    }
}

// footerForRow renders the 1-line identity footer for a highlighted row.
// Sentinel: a description; concrete project: the identity URL truncated
// to width-2 if longer. Spec §5.1, §9.
func footerForRow(r projectsRow, width int) string {
    if r.sentinel {
        return "issue queue across every registered project"
    }
    label := "identity: " + r.identity
    if width > 0 && runewidth.StringWidth(label) > width-2 {
        label = runewidth.Truncate(label, width-2, "…")
    }
    return label
}
```

Add `m.projectsCursor int` to `Model`. Initialize in `initialModel` to `0`.

- [ ] **Step 5: Add a render-with-fixtures test using a frozen clock**

Append to `internal/tui/projects_view_test.go`:

```go
// TestProjectsView_RendersTable confirms the table renders with the
// expected column headers and row content for a fixture model. Wide
// terminal so all columns fit.
func TestProjectsView_RendersTable(t *testing.T) {
    t1 := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
    m := initialModel(Options{})
    m.view = viewProjects
    m.width = 120
    m.height = 24
    m.projectsByID = map[int64]string{1: "kata", 2: "roborev"}
    m.projectIdentByID = map[int64]string{1: "github.com/wesm/kata", 2: "github.com/wesm/roborev"}
    m.projectStats = map[int64]ProjectStatsSummary{
        1: {Open: 12, Closed: 3, LastEventAt: &t1},
        2: {Open: 7, Closed: 2, LastEventAt: &t1},
    }

    out := m.View()
    for _, want := range []string{
        "kata / projects", "Project", "Open", "Closed", "Total", "Updated",
        "All projects", "kata", "roborev",
    } {
        assert.Contains(t, out, want, "missing %q in viewProjects output", want)
    }
}
```

- [ ] **Step 6: Run table render test**

Run: `go test ./internal/tui/ -run TestProjectsView_RendersTable -count=1 -v`
Expected: PASS

- [ ] **Step 7: Add the dash-for-empty-events test**

```go
// TestProjectsView_DashWhenNoEvents pins spec §6.1: a row with
// LastEventAt=nil renders "—" in the Updated column.
func TestProjectsView_DashWhenNoEvents(t *testing.T) {
    m := initialModel(Options{})
    m.view = viewProjects
    m.width = 120
    m.height = 24
    m.projectsByID = map[int64]string{1: "fresh"}
    m.projectIdentByID = map[int64]string{1: "github.com/wesm/fresh"}
    m.projectStats = map[int64]ProjectStatsSummary{
        1: {Open: 0, Closed: 0, LastEventAt: nil},
    }
    out := m.View()
    assert.Contains(t, out, "—", "nil LastEventAt must render as em-dash")
}
```

Run: `go test ./internal/tui/ -run TestProjectsView_DashWhenNoEvents -count=1 -v`
Expected: PASS

- [ ] **Step 8: Add identity-footer test**

```go
// TestProjectsView_IdentityFooterOnHighlight pins spec §5.1:
// highlighting a real project renders its identity URL beneath the
// table; highlighting the sentinel renders the description.
func TestProjectsView_IdentityFooterOnHighlight(t *testing.T) {
    m := initialModel(Options{})
    m.view = viewProjects
    m.width = 120
    m.height = 24
    m.projectsByID = map[int64]string{1: "kata"}
    m.projectIdentByID = map[int64]string{1: "github.com/wesm/kata"}
    m.projectStats = map[int64]ProjectStatsSummary{1: {}}

    m.projectsCursor = 0 // sentinel row
    out := m.View()
    assert.Contains(t, out, "issue queue across every registered project")

    m.projectsCursor = 1 // kata row
    out = m.View()
    assert.Contains(t, out, "identity: github.com/wesm/kata")
}
```

Run: `go test ./internal/tui/ -run TestProjectsView_IdentityFooterOnHighlight -count=1 -v`
Expected: PASS

- [ ] **Step 9: Run the full TUI suite**

Run: `go test ./internal/tui/ -count=1`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/tui/projects_view.go internal/tui/projects_view_render.go internal/tui/projects_view_test.go internal/tui/model.go internal/tui/messages.go
git commit -m "$(cat <<'EOF'
tui: render the projects view table with sentinel summing

Replaces the placeholder with the actual five-column layout (Project /
Open / Closed / Total / Updated), an All-projects sentinel pinned at
row 0 whose totals are client-summed from the per-row stats, and a
one-line identity footer for the highlighted row.

Sort is last_event_at desc with name asc as the tiebreak per spec §5.3.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: viewProjects key handling — j/k/g/G/Enter/Esc/r

Wire navigation, drill-in selection, return-to-list, and manual refresh. The `Esc`-back-to-list path remembers the prior view's scope.

**Files:**
- Modify: `internal/tui/projects_view.go` — add key handlers
- Modify: `internal/tui/model.go` — route key dispatch when `m.view == viewProjects`
- Modify: `internal/tui/projects_view_test.go`

- [ ] **Step 1: Add `routeProjectsViewKey` to `projects_view.go`**

Append:

```go
// routeProjectsViewKey delivers a key to the active projects view.
// j/k or up/down move the cursor (clamped); g/G or Home/End jump;
// Enter selects the highlighted row and transitions to viewList; Esc
// returns to the prior list view (or no-op if there isn't one); r
// dispatches a manual refresh. Other keys are absorbed.
//
// Spec §1.4 (P/Esc/r), §5.4 (Enter), §6.2 (transition-driven refetch).
func (m Model) routeProjectsViewKey(msg tea.KeyMsg) (Model, tea.Cmd) {
    rows := projectsRows(m.projectsByID, m.projectIdentByID, m.projectStats)
    switch msg.String() {
    case "j", "down":
        if m.projectsCursor < len(rows)-1 {
            m.projectsCursor++
        }
        return m, nil
    case "k", "up":
        if m.projectsCursor > 0 {
            m.projectsCursor--
        }
        return m, nil
    case "g", "home":
        m.projectsCursor = 0
        return m, nil
    case "G", "end":
        m.projectsCursor = len(rows) - 1
        return m, nil
    case "esc":
        return m.escFromProjectsView()
    case "r":
        return m, m.fetchProjectsWithStats()
    case "enter":
        return m.applyProjectsViewSelection(rows)
    }
    return m, nil
}

// applyProjectsViewSelection commits the highlighted row's choice. The
// sentinel transitions to all-projects scope; a real row transitions to
// single-project scope. Either way the issue cache is dropped and a
// fresh issue fetch is dispatched. Spec §5.4.
func (m Model) applyProjectsViewSelection(rows []projectsRow) (Model, tea.Cmd) {
    if m.projectsCursor < 0 || m.projectsCursor >= len(rows) {
        return m, nil
    }
    r := rows[m.projectsCursor]
    if r.sentinel {
        m.scope = scope{allProjects: true}
    } else {
        m.scope = scope{
            projectID:       r.projectID,
            projectName:     r.name,
            homeProjectID:   r.projectID,
            homeProjectName: r.name,
        }
    }
    m.view = viewList
    m.list = listModel{actor: m.list.actor}
    m.cache.markStale()
    return m, m.fetchInitial()
}

// escFromProjectsView returns to the prior viewList if scope is set
// (the user came from a list); otherwise it's a no-op (boot landed
// here without a prior list view). Spec §1.4. The cached list is
// reused — no refetch.
func (m Model) escFromProjectsView() (Model, tea.Cmd) {
    if m.scope.projectID == 0 && !m.scope.allProjects {
        return m, nil // boot landing, no prior list
    }
    m.view = viewList
    return m, nil
}
```

Add `tea "github.com/charmbracelet/bubbletea"` to imports if not already there.

- [ ] **Step 2: Route `viewProjects` keys from `Model.Update`**

In `internal/tui/model.go`, find the existing top-level key dispatch (likely `routeTopLevel` or similar that handles per-view keys). Add a case for `m.view == viewProjects` that routes to `routeProjectsViewKey`. The shape mirrors how `viewList` and `viewDetail` are routed; pattern:

```go
if m.view == viewProjects {
    if km, ok := msg.(tea.KeyMsg); ok {
        return m.routeProjectsViewKey(km)
    }
    return m, nil
}
```

Place this **after** the global-key handler (so `q` / `?` still work) and **before** the modal/input dispatch.

You'll also need to extend the `viewProjects` Update path to consume `projectsLoadedMsg` (already handled via the existing `pl, ok := msg.(projectsLoadedMsg)` block, which now copies stats too).

- [ ] **Step 3: Write the j/k navigation test**

Append to `internal/tui/projects_view_test.go`:

```go
// TestProjectsView_JKMoveCursor pins basic vertical navigation. Cursor
// is clamped at both ends; j moves down, k moves up.
func TestProjectsView_JKMoveCursor(t *testing.T) {
    m := initialModel(Options{})
    m.view = viewProjects
    m.projectsByID = map[int64]string{1: "a", 2: "b", 3: "c"}
    m.projectIdentByID = map[int64]string{1: "...", 2: "...", 3: "..."}
    m.projectStats = map[int64]ProjectStatsSummary{1: {}, 2: {}, 3: {}}
    m.projectsCursor = 0

    out, _ := m.routeProjectsViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
    assert.Equal(t, 1, out.projectsCursor, "j → cursor 1")

    out, _ = out.routeProjectsViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
    out, _ = out.routeProjectsViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
    out, _ = out.routeProjectsViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
    assert.Equal(t, 3, out.projectsCursor, "j past end is clamped")

    out, _ = out.routeProjectsViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
    assert.Equal(t, 2, out.projectsCursor, "k → cursor 2")
}
```

- [ ] **Step 4: Write the Enter-on-project test**

```go
// TestProjectsView_EnterOnProjectTransitions pins spec §5.4: Enter on
// a real project sets scope to that project and transitions to viewList
// with a fresh fetch dispatched.
func TestProjectsView_EnterOnProjectTransitions(t *testing.T) {
    m := initialModel(Options{})
    m.view = viewProjects
    m.projectsByID = map[int64]string{7: "kata", 9: "roborev"}
    m.projectIdentByID = map[int64]string{7: "...", 9: "..."}
    m.projectStats = map[int64]ProjectStatsSummary{7: {}, 9: {}}
    // Cursor on the first real project (sentinel + sorted rows; see
    // projectsRows for ordering — alpha tiebreak means 'kata' first).
    m.projectsCursor = 1

    out, cmd := m.routeProjectsViewKey(tea.KeyMsg{Type: tea.KeyEnter})
    assert.Equal(t, viewList, out.view)
    assert.False(t, out.scope.allProjects, "concrete project, not all-projects")
    assert.Equal(t, int64(7), out.scope.projectID)
    assert.Equal(t, "kata", out.scope.projectName)
    require.NotNil(t, cmd, "must dispatch a fetch")
}
```

- [ ] **Step 5: Write the Enter-on-sentinel test**

```go
// TestProjectsView_EnterOnSentinelTransitions pins that Enter on the
// All-projects row sets allProjects=true and transitions to viewList.
func TestProjectsView_EnterOnSentinelTransitions(t *testing.T) {
    m := initialModel(Options{})
    m.view = viewProjects
    m.projectsByID = map[int64]string{1: "a"}
    m.projectIdentByID = map[int64]string{1: "..."}
    m.projectStats = map[int64]ProjectStatsSummary{1: {}}
    m.projectsCursor = 0 // sentinel

    out, cmd := m.routeProjectsViewKey(tea.KeyMsg{Type: tea.KeyEnter})
    assert.Equal(t, viewList, out.view)
    assert.True(t, out.scope.allProjects)
    assert.Zero(t, out.scope.projectID)
    require.NotNil(t, cmd)
}
```

- [ ] **Step 6: Write the Esc-back-to-list test**

```go
// TestProjectsView_EscReturnsToPriorList pins spec §1.4: Esc from
// viewProjects returns to viewList without a refetch when scope is set
// (the user came from a list via P).
func TestProjectsView_EscReturnsToPriorList(t *testing.T) {
    m := initialModel(Options{})
    m.view = viewProjects
    m.scope = scope{projectID: 7, projectName: "kata", homeProjectID: 7, homeProjectName: "kata"}

    out, cmd := m.routeProjectsViewKey(tea.KeyMsg{Type: tea.KeyEsc})
    assert.Equal(t, viewList, out.view, "Esc → viewList")
    assert.Equal(t, int64(7), out.scope.projectID, "scope unchanged")
    assert.Nil(t, cmd, "no refetch on Esc-back")
}

// TestProjectsView_EscNoOpOnBootEntry pins that Esc with no prior scope
// (boot landed on viewProjects) leaves the view in place. Spec §1.4.
func TestProjectsView_EscNoOpOnBootEntry(t *testing.T) {
    m := initialModel(Options{})
    m.view = viewProjects
    // Default scope is zero (empty=false, projectID=0, allProjects=false)
    // — this represents the boot landing case.

    out, cmd := m.routeProjectsViewKey(tea.KeyMsg{Type: tea.KeyEsc})
    assert.Equal(t, viewProjects, out.view, "Esc with no prior list → no transition")
    assert.Nil(t, cmd)
}
```

- [ ] **Step 7: Write the r-refresh test**

```go
// TestProjectsView_RRefreshes pins spec §1.4: r dispatches a manual
// refresh of the projects table. View stays in viewProjects.
func TestProjectsView_RRefreshes(t *testing.T) {
    m := initialModel(Options{})
    m.view = viewProjects

    out, cmd := m.routeProjectsViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
    assert.Equal(t, viewProjects, out.view)
    require.NotNil(t, cmd, "r must dispatch fetchProjectsWithStats")
}
```

- [ ] **Step 8: Run the new key-handling tests**

Run: `go test ./internal/tui/ -run TestProjectsView_ -count=1 -v`
Expected: PASS

- [ ] **Step 9: Run the full TUI suite to catch regressions**

Run: `go test ./internal/tui/ -count=1`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/tui/projects_view.go internal/tui/projects_view_test.go internal/tui/model.go
git commit -m "$(cat <<'EOF'
tui: wire viewProjects key handling

j/k/g/G/Home/End for navigation, Enter to drill into the highlighted
row's scope (sentinel → all-projects, project → single-project), Esc
to return to the prior list when one exists, r for manual refresh.

Esc-back is no-refetch — the cached list state is reused.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Wire the `P` binding to `viewProjects` (replaces the picker handler)

Rename the `SwitchProject` keymap to `Projects`, swap its handler from `openProjectPicker` to a new transition that enters `viewProjects` and dispatches the stats fetch. Drop the modal-specific tests in `scope_test.go`. Refresh help golden snapshots. The picker code itself stays in place (deleted in Task 11).

**Files:**
- Modify: `internal/tui/keymap.go`
- Modify: `internal/tui/help.go`
- Modify: `internal/tui/model.go` (handler swap)
- Modify: `internal/tui/projects_view.go` (transition helper)
- Modify: `internal/tui/scope_test.go` (drop picker-specific tests; keep view-empty tests)
- Modify: `internal/tui/testdata/golden/help-wide.txt`, `help-narrow.txt`

- [ ] **Step 1: Rename the keymap entry**

In `internal/tui/keymap.go`:

```go
// Field rename:
type keymap struct {
    Help, Quit                                     key
    Projects                                       key  // was: SwitchProject
    ToggleLayout                                   key
    // ...
}

// Constructor rename:
return keymap{
    Help:         key{Keys: []string{"?"}, Help: "help"},
    Quit:         key{Keys: []string{"q", "ctrl+c"}, Help: "quit"},
    Projects:     key{Keys: []string{"P"}, Help: "projects"},
    ToggleLayout: key{Keys: []string{"L"}, Help: "toggle layout"},
    // ... rest unchanged
}
```

- [ ] **Step 2: Update the help section reference**

In `internal/tui/help.go`, change `r(km.SwitchProject)` to `r(km.Projects)` in the "Global" section. (One reference; `grep -n SwitchProject internal/tui/`.)

- [ ] **Step 3: Add the transition helper**

Append to `internal/tui/projects_view.go`:

```go
// transitionToProjects switches to viewProjects and dispatches a stats
// fetch per spec §6.2. Cursor positions on the row matching the active
// scope so a no-op P → Esc round-trip leaves the cursor where the user
// expects. When scope.allProjects is true, cursor lands on the sentinel.
func (m Model) transitionToProjects() (Model, tea.Cmd) {
    m.view = viewProjects
    rows := projectsRows(m.projectsByID, m.projectIdentByID, m.projectStats)
    m.projectsCursor = cursorForScope(rows, m.scope)
    return m, m.fetchProjectsWithStats()
}

// cursorForScope finds the row matching the active scope. Returns 0
// (sentinel) when scope.allProjects, the row matching scope.projectID
// when set, or 0 as a safe default.
func cursorForScope(rows []projectsRow, sc scope) int {
    if sc.allProjects {
        return 0
    }
    for i, r := range rows {
        if !r.sentinel && r.projectID == sc.projectID {
            return i
        }
    }
    return 0
}
```

- [ ] **Step 4: Swap the `P` handler in `routeGlobalKey`**

In `internal/tui/model.go`, find the `m.keymap.SwitchProject.matches(msg)` block (~line 1374) and replace:

```go
// Before:
if m.keymap.SwitchProject.matches(msg) {
    next, cmd := m.openProjectPicker()
    return next, cmd, true
}

// After:
if m.keymap.Projects.matches(msg) {
    next, cmd := m.transitionToProjects()
    return next, cmd, true
}
```

The picker is no longer reachable from the keymap, but `openProjectPicker` and friends still exist (Task 11 removes them).

- [ ] **Step 5: Delete the picker-specific tests in scope_test.go**

Open `internal/tui/scope_test.go` and remove these test functions completely:

- `TestProjectPicker_PKeyOpensModal`
- `TestProjectPicker_OpensOnActiveScope`
- `TestProjectPicker_AllProjectsSelection`
- `TestProjectPicker_SwitchesToOtherProject`
- `TestProjectPicker_EscCancels`
- `TestProjectPicker_NoProjectsRefuses`
- `TestProjectPicker_GatedByInputting`

Also remove the helper fixtures (`scopeFixtureSingle`, `scopeFixtureMultiProject`) if they're not used by the remaining tests.

Keep:
- `TestEmptyState_RendersHint`
- `TestEmptyState_QuitsOnQ`
- `TestEmptyState_OtherKeysIgnored`
- `TestRenderEmpty_ZeroDims`

These are about the empty state, not the picker.

- [ ] **Step 6: Add the P-from-list test**

Append to `internal/tui/projects_view_test.go`:

```go
// TestProjectsView_PFromListTransitions pins spec §1.4: P from viewList
// transitions to viewProjects and dispatches the stats fetch. Scope is
// preserved on the way out so an Esc-back returns to the same queue.
func TestProjectsView_PFromListTransitions(t *testing.T) {
    m := initialModel(Options{})
    m.view = viewList
    m.scope = scope{projectID: 7, projectName: "kata", homeProjectID: 7, homeProjectName: "kata"}
    // Need a stub api so the cmd can be dispatched without crashing —
    // the cmd doesn't run to completion in this test.
    m.api = &Client{}

    out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
    nm := out.(Model)
    assert.Equal(t, viewProjects, nm.view)
    assert.Equal(t, int64(7), nm.scope.projectID, "scope preserved on P transition")
    require.NotNil(t, cmd, "P must dispatch a stats fetch")
}

// TestProjectsView_PWhileInputFocusedRoutesToPrompt pins spec §1.4: P
// while a search bar / form is focused reaches the prompt instead of
// transitioning the view.
func TestProjectsView_PWhileInputFocusedRoutesToPrompt(t *testing.T) {
    m := initialModel(Options{})
    m.view = viewList
    m.scope = scope{projectID: 7, projectName: "kata"}
    m.input = newSearchBar(ListFilter{})

    out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
    nm := out.(Model)
    assert.Equal(t, viewList, nm.view, "view must not transition while input is focused")
    if v := nm.input.activeField().value(); v != "P" {
        t.Fatalf("input buffer = %q, want %q", v, "P")
    }
}
```

- [ ] **Step 7: Run the new tests**

Run: `go test ./internal/tui/ -run 'TestProjectsView_(PFromList|PWhileInput)' -count=1 -v`
Expected: PASS

- [ ] **Step 8: Refresh help goldens**

Run: `go test ./internal/tui/ -run TestSnapshot_Help -update-goldens`

Verify the diff in `testdata/golden/help-wide.txt` shows the Global section's row changed from `R  toggle all-projects view` (or similar) to `P  projects`. If a row still references `R`, it means the keymap didn't update — re-check Step 1.

- [ ] **Step 9: Run the full TUI suite**

Run: `go test ./internal/tui/ -count=1`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/tui/keymap.go internal/tui/help.go internal/tui/model.go internal/tui/projects_view.go internal/tui/projects_view_test.go internal/tui/scope_test.go internal/tui/testdata/golden/help-wide.txt internal/tui/testdata/golden/help-narrow.txt
git commit -m "$(cat <<'EOF'
tui: P transitions to viewProjects (replaces modal handler)

Renames the SwitchProject keymap entry to Projects, swaps the binding's
handler from openProjectPicker to a new transitionToProjects that sets
view=viewProjects and dispatches the stats fetch.

Drops the now-unreachable TestProjectPicker_* tests; the picker code
itself is retired in a follow-up commit so the binding swap stays
isolated from the deletion diff.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Boot routing — land on `viewProjects` when cwd doesn't resolve

Refactor `bootResolveScope` to learn the initial view, returning `(scope, viewID, error)`. The unresolved-cwd branch consults `Client.ListProjectsWithStats` to disambiguate `viewEmpty` (zero projects) from `viewProjects` (≥1 project). The combined return shape lets `Run` plug the view into `Model.view` and seed `m.projectsByID` / `m.projectStats` from the boot fetch's result.

**Files:**
- Modify: `internal/tui/run.go`
- Modify: `internal/tui/run_test.go`

- [ ] **Step 1: Write the failing test for the new behavior**

Append to `internal/tui/run_test.go`:

```go
// TestBoot_UnresolvedWithProjects_LandsViewProjects pins the new boot
// rule: an unresolved cwd plus ≥1 registered project lands on
// viewProjects, not viewEmpty. Spec §4.2.
func TestBoot_UnresolvedWithProjects_LandsViewProjects(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        switch r.URL.Path {
        case "/api/v1/projects/resolve":
            w.WriteHeader(http.StatusNotFound)
            _ = json.NewEncoder(w).Encode(map[string]any{
                "status": 404,
                "error": map[string]any{
                    "code":    "project_not_initialized",
                    "message": "no kata.toml",
                },
            })
        case "/api/v1/projects":
            require.Equal(t, "stats", r.URL.Query().Get("include"))
            _, _ = w.Write([]byte(`{"projects":[
                {"id":7,"identity":"github.com/wesm/kata","name":"kata",
                 "stats":{"open":3,"closed":1,"last_event_at":"2026-05-04T12:00:00.000Z"}}
            ]}`))
        default:
            http.NotFound(w, r)
        }
    }))
    defer srv.Close()
    c := NewClient(srv.URL, srv.Client())

    sc, view, err := bootResolveScope(t.Context(), c, "/tmp/unbound")
    require.NoError(t, err)
    assert.Equal(t, viewProjects, view)
    assert.True(t, sc.empty, "no project scope when boot lands on viewProjects")
    // ... or whatever shape we settle on; see Step 2.
}
```

You may want to refine the assertion on `sc` after seeing the shape. The simplest invariant: when `view == viewProjects`, `scope.projectID == 0 && !scope.allProjects`. The `empty` flag is reserved for the truly-empty case. So actually:

```go
assert.False(t, sc.empty)
assert.Zero(t, sc.projectID)
assert.False(t, sc.allProjects)
```

- [ ] **Step 2: Run test, expect failure**

Run: `go test ./internal/tui/ -run TestBoot_UnresolvedWithProjects_LandsViewProjects -count=1 -v`
Expected: FAIL — `bootResolveScope` returns 2 values, not 3.

- [ ] **Step 3: Refactor `bootResolveScope`**

In `internal/tui/run.go`, replace the existing `bootResolveScope` function:

```go
// bootResolveScope picks the initial scope + view from cwd. Spec §4.2:
//
//  1. POST /projects/resolve(cwd) success → single-project scope, viewList.
//  2. project_not_initialized + ≥1 registered project → empty scope,
//     viewProjects (the user browses the workspace).
//  3. project_not_initialized + 0 projects → empty scope (sc.empty=true),
//     viewEmpty.
//  4. Any other resolve error → propagate so Run fails loudly.
//
// In case 2, the projects list is fetched here (with stats) so the
// initial Model can render a populated table on the first frame.
func bootResolveScope(
    ctx context.Context, c *Client, cwd string,
) (scope, viewID, error) {
    rr, err := c.ResolveProject(ctx, cwd)
    if err == nil {
        return scope{
            projectID:       rr.Project.ID,
            projectName:     rr.Project.Name,
            workspace:       rr.WorkspaceRoot,
            homeProjectID:   rr.Project.ID,
            homeProjectName: rr.Project.Name,
        }, viewList, nil
    }
    var apiErr *APIError
    if !errors.As(err, &apiErr) || apiErr.Code != "project_not_initialized" {
        return scope{}, viewList, err
    }
    rows, err := c.ListProjectsWithStats(ctx)
    if err != nil {
        return scope{}, viewList, err
    }
    if len(rows) == 0 {
        return scope{empty: true}, viewEmpty, nil
    }
    return scope{}, viewProjects, nil
}
```

- [ ] **Step 4: Update the caller in `Run` / `bootClient`**

`bootClient` currently returns `(*Client, *http.Client, scope, string, error)`. Extend to also carry the view:

```go
func bootClient(ctx context.Context, _ Options) (*Client, *http.Client, scope, viewID, string, error) {
    // ... existing setup ...
    c := NewClient(endpoint, hc)
    cwd, _ := os.Getwd()
    sc, view, err := bootResolveScope(ctx, c, cwd)
    if err != nil {
        return nil, nil, scope{}, viewList, "", err
    }
    return c, sseHC, sc, view, endpoint, nil
}
```

Update `Run`:

```go
c, sseHC, sc, view, endpoint, err := bootClient(ctx, opts)
if err != nil {
    return err
}
m := buildRunModel(opts, c, sc, view)
```

Update `buildRunModel`:

```go
func buildRunModel(opts Options, c *Client, sc scope, view viewID) Model {
    m := initialModel(opts)
    m.api = c
    m.scope = sc
    m.view = view
    return m
}
```

This replaces the `if sc.empty { m.view = viewEmpty }` branch — the view is already correct from `bootResolveScope`.

- [ ] **Step 5: Update existing boot tests for the new signature**

In `internal/tui/run_test.go`, find every `bootResolveScope(...)` call and update for the 3-return signature. Existing test names to touch:

- `TestBoot_ResolvesProject` — now also asserts `view == viewList`.
- `TestBoot_UnboundCwd_LandsInEmptyState` — rename or update; the daemon now serves `?include=stats`. Two sub-cases: zero-projects → `viewEmpty`; ≥1 project → `viewProjects`. Split into the two test functions.

After splitting:

```go
// TestBoot_UnboundCwd_NoProjects_LandsViewEmpty pins the boot rule
// for the truly-empty workspace. Spec §4.2 row 1.
func TestBoot_UnboundCwd_NoProjects_LandsViewEmpty(t *testing.T) {
    // existing fixture, plus stub /api/v1/projects?include=stats
    // returning {"projects":[]}.
    // ...
    sc, view, err := bootResolveScope(t.Context(), c, "/tmp/unbound")
    require.NoError(t, err)
    assert.Equal(t, viewEmpty, view)
    assert.True(t, sc.empty)
}
```

The earlier `TestBoot_UnresolvedWithProjects_LandsViewProjects` (Step 1) already covers the populated case.

- [ ] **Step 6: Clean up the stale comment block in `run.go`**

The doc comment that previously preceded `bootResolveScope` (~line 158-167) claimed `allProjects` was gated and the daemon had no cross-project route. Both are false today. Replace the stale doc with the new one already used in Step 3 (4 numbered cases).

Also delete the `scope` struct's stale doc-comment lines about `allProjects` being "always false" (the lines around `internal/tui/run.go:140-143`):

```go
// type scope describes the issue-set the TUI is browsing. Exactly one of
// projectID, allProjects, empty is set. The boot path drives the initial
// values; runtime transitions in viewProjects mutate scope before
// transitioning to viewList.
//
// homeProjectID/homeProjectName capture the project bootResolveScope
// picked from the cwd. They're zero when boot landed in viewProjects
// or viewEmpty.
type scope struct { ... }
```

- [ ] **Step 7: Run all boot tests**

Run: `go test ./internal/tui/ -run TestBoot_ -count=1 -v`
Expected: PASS

- [ ] **Step 8: Run the full TUI suite**

Run: `go test ./internal/tui/ -count=1`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/tui/run.go internal/tui/run_test.go
git commit -m "$(cat <<'EOF'
tui: boot lands on viewProjects when cwd does not resolve

Refactors bootResolveScope to return (scope, viewID, error). The
unresolved-cwd branch now consults ListProjectsWithStats to
disambiguate viewEmpty (zero projects) from viewProjects (>=1
project). The cwd-resolves fast path is unchanged.

Cleans up the stale comment block at run.go:140-167 that claimed
all-projects was gated and the daemon had no cross-project route.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: SSE invalidation — keep stats fresh while `viewProjects` is active

Add the `m.projectsStale` flag flipped on relevant SSE events; debounce a refetch by 500ms when the view is active.

**Files:**
- Modify: `internal/tui/model.go` — add `projectsStale` field, wire SSE event hook
- Modify: `internal/tui/projects_view.go` — debounce timer cmd
- Modify: `internal/tui/sse_update_test.go` (or wherever SSE tests live; create if absent)

- [ ] **Step 1: Find the existing SSE event router**

Run: `grep -n "func.*Model.*sseFrame\|EventEnvelope\|eventAffectsView\|invalidate" internal/tui/*.go | grep -v _test`

Identify the function that routes incoming SSE events. It's likely `routeSSEEvent` or similar; the existing list view already has invalidation logic. The new code is parallel: when `m.view == viewProjects` and the event's project_id matches a row in `m.projectsByID`, flip `m.projectsStale = true` and dispatch a debounced refetch.

- [ ] **Step 2: Add the `projectsStale` field**

In `internal/tui/model.go` near `pendingRefetch` (which is the existing list-debounce field):

```go
// projectsStale flags that the projects table needs a refetch. Set by
// the SSE event router on issue.created/closed/reopened/deleted events
// whose project_id matches a row in m.projectsByID. Cleared when the
// debounced fetchProjectsWithStats lands. Spec §6.3.
projectsStale bool

// projectsRefetchPending coalesces stale-flips inside the 500ms window
// so a burst of SSE events produces exactly one refetch.
projectsRefetchPending bool
```

- [ ] **Step 3: Define the debounce cmd and message**

In `internal/tui/projects_view.go`:

```go
const projectsRefetchDebounce = 500 * time.Millisecond

// projectsDebounceFireMsg is the wakeup the debounce timer dispatches
// after projectsRefetchDebounce elapses since the last stale-flip.
type projectsDebounceFireMsg struct{}

// projectsDebounceCmd schedules the debounce wakeup. The handler in
// Update consumes the message and either dispatches fetchProjectsWithStats
// or no-ops (if viewProjects is no longer active). Spec §6.3.
func projectsDebounceCmd() tea.Cmd {
    return tea.Tick(projectsRefetchDebounce, func(time.Time) tea.Msg {
        return projectsDebounceFireMsg{}
    })
}
```

- [ ] **Step 4: Wire SSE event invalidation**

In the existing SSE event handler (the function that processes incoming `eventEnvelopeMsg` or similar), add this check after the existing list-invalidation branch and before returning:

```go
// Projects-view freshness: if viewProjects is the active view, an
// issue mutation event affects the row counts. Spec §6.3.
if m.view == viewProjects && eventAffectsProjectsTable(env, m.projectsByID) {
    m.projectsStale = true
    if !m.projectsRefetchPending {
        m.projectsRefetchPending = true
        cmds = append(cmds, projectsDebounceCmd())
    }
}
```

Define `eventAffectsProjectsTable` in `internal/tui/projects_view.go`:

```go
// eventAffectsProjectsTable reports whether an incoming SSE event
// changes the numbers a viewProjects table is rendering. Any event for
// a project the table is showing affects the Updated column at minimum;
// issue lifecycle events also change Open/Closed.
func eventAffectsProjectsTable(env eventEnvelope, byID map[int64]string) bool {
    if env.ProjectID == 0 {
        return false
    }
    _, shown := byID[env.ProjectID]
    return shown
}
```

(Adapt `eventEnvelope` field name to match the existing SSE event type in the codebase. Check `grep -n "ProjectID\|project_id" internal/tui/*.go | grep -v _test` near the SSE-frame struct.)

- [ ] **Step 5: Handle the debounce fire**

In `Model.Update`, add a case for the new message:

```go
if _, ok := msg.(projectsDebounceFireMsg); ok {
    m.projectsRefetchPending = false
    if m.view == viewProjects && m.projectsStale {
        m.projectsStale = false
        return m, m.fetchProjectsWithStats()
    }
    return m, nil
}
```

- [ ] **Step 6: Write the stale-flip test**

Add to `internal/tui/sse_update_test.go` (or `projects_view_test.go` if SSE tests live there):

```go
// TestProjectsView_StaleOnIssueEvent pins spec §6.3: an issue.created
// event for a project the table is showing flips m.projectsStale and
// dispatches the debounce timer.
func TestProjectsView_StaleOnIssueEvent(t *testing.T) {
    m := initialModel(Options{})
    m.view = viewProjects
    m.projectsByID = map[int64]string{7: "kata"}

    // Construct the SSE-event message matching the codebase's incoming
    // shape — adapt this to the actual envelope type.
    msg := makeFakeIssueCreatedEvent(t, 7) // helper, see below
    out, _ := m.Update(msg)
    nm := out.(Model)
    assert.True(t, nm.projectsStale)
    assert.True(t, nm.projectsRefetchPending)
}
```

`makeFakeIssueCreatedEvent` is a test helper that builds whatever shape the SSE-event handler expects. Look at existing list-invalidation tests for the pattern (probably `eventEnvelopeMsg{Type:"issue.created", ProjectID: id}` or similar).

- [ ] **Step 7: Write the inactive-view test**

```go
// TestProjectsView_IgnoresEventsWhenInactive pins that the same event
// is a no-op when viewList is active — we'll refetch on next view
// entry anyway. Spec §6.3.
func TestProjectsView_IgnoresEventsWhenInactive(t *testing.T) {
    m := initialModel(Options{})
    m.view = viewList // not viewProjects
    m.projectsByID = map[int64]string{7: "kata"}

    msg := makeFakeIssueCreatedEvent(t, 7)
    out, _ := m.Update(msg)
    nm := out.(Model)
    assert.False(t, nm.projectsStale)
}
```

- [ ] **Step 8: Write the debounce-coalesces test**

```go
// TestProjectsView_DebouncesRefetch pins that three SSE events within
// the window flip projectsStale once and dispatch exactly one debounce
// timer (no thundering herd). Spec §6.3.
func TestProjectsView_DebouncesRefetch(t *testing.T) {
    m := initialModel(Options{})
    m.view = viewProjects
    m.projectsByID = map[int64]string{7: "kata"}

    var cmds []tea.Cmd
    for i := 0; i < 3; i++ {
        out, cmd := m.Update(makeFakeIssueCreatedEvent(t, 7))
        m = out.(Model)
        if cmd != nil {
            cmds = append(cmds, cmd)
        }
    }
    assert.True(t, m.projectsStale)
    assert.True(t, m.projectsRefetchPending)
    // Three events; only the first should dispatch a debounce timer.
    // The other two see projectsRefetchPending=true and skip.
    assert.Lenf(t, cmds, 1, "exactly one debounce cmd, got %d", len(cmds))
}
```

- [ ] **Step 9: Run the SSE tests**

Run: `go test ./internal/tui/ -run 'TestProjectsView_(Stale|Ignores|Debounces)' -count=1 -v`
Expected: PASS

- [ ] **Step 10: Run the full TUI suite**

Run: `go test ./internal/tui/ -count=1`
Expected: PASS

- [ ] **Step 11: Commit**

```bash
git add internal/tui/model.go internal/tui/projects_view.go internal/tui/sse_update_test.go internal/tui/projects_view_test.go
git commit -m "$(cat <<'EOF'
tui: SSE-driven freshness invalidation for viewProjects

When viewProjects is active and an SSE event arrives for a project the
table is showing, flip m.projectsStale and schedule a 500ms-debounced
fetchProjectsWithStats. Bursts coalesce — three events fire one refetch.

Inactive viewProjects ignores the event; we refetch on next entry per
the transition-driven rule.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Retire the modal — delete picker code

Delete every line of code that supported the picker modal. The keymap binding now points at `transitionToProjects` (Task 8); the picker is unreachable from any code path. This task removes the dead code in one focused commit.

**Files:**
- Delete content from: `internal/tui/scope.go` (the picker pieces — keep nothing of the picker)
- Modify: `internal/tui/quit_modal.go` (remove `modalProjectPicker` enum value)
- Modify: `internal/tui/model.go` (remove `projectPicker projectPickerState` field, remove the modal render branches in `View()`, remove the `modalProjectPicker` case in `routeModalKey`)

- [ ] **Step 1: Delete picker code from `scope.go`**

In `internal/tui/scope.go`, delete:

- `projectPickerState` type
- `projectPickerItem` type
- `func (m Model) openProjectPicker(...)`
- `func (m Model) toastPickerUnavailable(...)`
- `buildProjectPickerItems`
- `pickerCursorForScope`
- `func (m Model) routeProjectPickerKey(...)`
- `func (m Model) applyProjectPickerSelection(...)`
- `func renderProjectPickerModal(...)`
- `const modalProjectPickerWidth = 32`

Keep:
- `func renderEmpty(width, height int) string` — used by `viewEmpty`
- `func renderTooNarrow(width, height int) string` — used by the narrow-terminal hint

If after the deletions `scope.go` is just two render functions, consider renaming it to `view_chrome.go` or merging into an existing file. **Don't** rename in this commit — just delete.

- [ ] **Step 2: Remove `modalProjectPicker` enum value**

In `internal/tui/quit_modal.go` (~line 19):

```go
const (
    modalNone modalKind = iota
    modalQuitConfirm
    // modalProjectPicker  ← DELETE
)
```

- [ ] **Step 3: Remove the `projectPicker` field from `Model`**

In `internal/tui/model.go` (~line 110), delete:

```go
// projectPicker carries the cursor + sorted project list for the
// switch-project modal opened by the P binding. Reset to its zero
// value when the modal closes.
projectPicker projectPickerState
```

- [ ] **Step 4: Remove the modal render branches in `Model.View()`**

Find the two `if m.modal == modalProjectPicker` branches (one in the narrow path, one in the normal path — `grep -n modalProjectPicker internal/tui/model.go`) and delete them.

- [ ] **Step 5: Remove the `modalProjectPicker` case in `routeModalKey`**

Find the `case modalProjectPicker:` block and delete it. The remaining `routeModalKey` switch should still handle `modalQuitConfirm` and `modalNone`.

- [ ] **Step 6: Run the full TUI suite to confirm nothing references the deleted symbols**

Run: `go build ./...`
Expected: builds cleanly. If a reference remains, the error pinpoints it.

Run: `go test ./internal/tui/ -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/scope.go internal/tui/quit_modal.go internal/tui/model.go
git commit -m "$(cat <<'EOF'
tui: delete the modal-based project picker

The P binding now drives a real viewProjects surface (prior commits).
This commit removes the now-unreachable picker state machine in one
focused diff: projectPickerState, projectPickerItem, openProjectPicker,
routeProjectPickerKey, applyProjectPickerSelection,
renderProjectPickerModal, the modalProjectPicker enum value, the
m.projectPicker field, and the View() / routeModalKey branches that
referenced them.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: e2e smoke test

End-to-end exercise: register three projects with distinct activity, launch the TUI from an unbound cwd, assert the boot lands on viewProjects with rows ordered by `last_event_at` desc, drill into All-projects, and return via `P`.

**Files:**
- Create: `e2e/projects_view_test.go`

This task uses the existing e2e harness pattern. Skim `e2e/` to find the existing fixture style; mimic it.

- [ ] **Step 1: Sketch the fixture**

Look at `e2e/` directory: `ls e2e/`. The existing tests (`e2e/smoke_test.go` likely) show the pattern. Pull in whatever spawns a daemon + TUI in a `httptest`-style integration.

Note: a true TUI-driven e2e is hard because `kata tui` requires a TTY. The e2e harness might not exercise the TUI directly. Two acceptable shapes:

1. **TUI-shape integration test** in `internal/tui/` rather than `e2e/`: build a Model, call `Init`, dispatch the boot fetch synchronously against a real httptest server backed by a real `daemon.Server`, assert the resulting frame.
2. **HTTP-only e2e**: hit `GET /api/v1/projects?include=stats` on a running daemon with three projects + varied activity; assert wire shape. (Doesn't test TUI navigation but does exercise the new endpoint end-to-end with a real DB.)

**Pick option 1.** Place it in `internal/tui/` rather than `e2e/`. Existing tests in `internal/tui/` already follow this pattern (`TestBoot_ResolvesProject` builds a real httptest server and runs `bootResolveScope`).

- [ ] **Step 2: Write the smoke test**

Create `internal/tui/projects_view_smoke_test.go`:

```go
package tui

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

// TestSmoke_ProjectsViewLoop covers the end-to-end happy path of
// spec §8.7: an unbound cwd lands on viewProjects, the table is ordered
// by last_event_at desc, Enter on the sentinel transitions to viewList
// in all-projects scope, and P from viewList returns to viewProjects.
func TestSmoke_ProjectsViewLoop(t *testing.T) {
    // Stub the daemon: cwd doesn't resolve, three projects with stats.
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        switch r.URL.Path {
        case "/api/v1/projects/resolve":
            w.WriteHeader(http.StatusNotFound)
            _ = json.NewEncoder(w).Encode(map[string]any{
                "status": 404,
                "error":  map[string]any{"code": "project_not_initialized"},
            })
        case "/api/v1/projects":
            require.Equal(t, "stats", r.URL.Query().Get("include"))
            _, _ = w.Write([]byte(`{"projects":[
                {"id":1,"identity":"github.com/wesm/proj-a","name":"proj-a",
                 "stats":{"open":2,"closed":1,"last_event_at":"2026-05-04T10:00:00.000Z"}},
                {"id":2,"identity":"github.com/wesm/proj-b","name":"proj-b",
                 "stats":{"open":5,"closed":2,"last_event_at":"2026-05-04T12:00:00.000Z"}},
                {"id":3,"identity":"github.com/wesm/proj-c","name":"proj-c",
                 "stats":{"open":10,"closed":3,"last_event_at":"2026-05-04T11:00:00.000Z"}}
            ]}`))
        case "/api/v1/issues":
            // Cross-project list returned for the all-projects drill-in.
            _, _ = w.Write([]byte(`{"issues":[]}`))
        default:
            http.NotFound(w, r)
        }
    }))
    defer srv.Close()
    c := NewClient(srv.URL, srv.Client())

    // 1. Boot resolves to viewProjects.
    sc, view, err := bootResolveScope(t.Context(), c, "/tmp/unbound")
    require.NoError(t, err)
    require.Equal(t, viewProjects, view)

    // 2. Build the model and fire fetchProjectsWithStats synchronously.
    m := initialModel(Options{})
    m.api = c
    m.scope = sc
    m.view = view
    msg := m.fetchProjectsWithStats()()
    out, _ := m.Update(msg)
    m = out.(Model)

    // 3. Rows ordered by last_event_at desc: proj-b (12:00), proj-c (11:00), proj-a (10:00).
    rows := projectsRows(m.projectsByID, m.projectIdentByID, m.projectStats)
    require.Len(t, rows, 4)
    assert.True(t, rows[0].sentinel)
    assert.Equal(t, "proj-b", rows[1].name)
    assert.Equal(t, "proj-c", rows[2].name)
    assert.Equal(t, "proj-a", rows[3].name)

    // 4. Enter on the sentinel transitions to viewList in all-projects scope.
    m.projectsCursor = 0
    out, cmd := m.routeProjectsViewKey(tea.KeyMsg{Type: tea.KeyEnter})
    nm := out.(Model)
    assert.Equal(t, viewList, nm.view)
    assert.True(t, nm.scope.allProjects)
    require.NotNil(t, cmd, "drill-in must dispatch fetchInitial")

    // 5. P from the resulting viewList returns to viewProjects with scope preserved.
    out2, cmd2 := nm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
    nm2 := out2.(Model)
    assert.Equal(t, viewProjects, nm2.view)
    assert.True(t, nm2.scope.allProjects, "scope preserved on P-back")
    require.NotNil(t, cmd2, "P must dispatch fetchProjectsWithStats")
}
```

- [ ] **Step 3: Run smoke test**

Run: `go test ./internal/tui/ -run TestSmoke_ProjectsViewLoop -count=1 -v`
Expected: PASS

- [ ] **Step 4: Run the full repo test suite**

Run: `go test ./... -count=1`
Expected: PASS across every package.

- [ ] **Step 5: Lint**

Run: `golangci-lint run ./internal/tui/... ./internal/api/... ./internal/db/... ./internal/daemon/...`
Expected: no new findings (pre-existing findings in other code may remain — those aren't this plan's scope).

- [ ] **Step 6: Commit**

```bash
git add internal/tui/projects_view_smoke_test.go
git commit -m "$(cat <<'EOF'
tui: add smoke test for the projects-view boot loop

End-to-end: unbound cwd → viewProjects with three projects ordered by
last_event_at desc; Enter on the sentinel → viewList in all-projects
scope; P → return to viewProjects with scope preserved.

Stub HTTP server, no real daemon — exercises the full TUI surface
without TTY requirements.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review

After all twelve tasks are complete:

1. **Spec coverage** — confirm every numbered locked decision in spec §1 maps to a task:
   - §1.1 (real surface) → Tasks 5-12 collectively
   - §1.2 (cwd fast path / boot rule) → Task 9
   - §1.3 (no last-pick) → No task needed; the absence is the design
   - §1.4 (P/Esc/r) → Tasks 7, 8
   - §1.5 (sentinel pinned first) → Task 6
   - §1.6 (server-computed + sentinel client-summed) → Tasks 1, 6
   - §1.7 (ProjectOut projection across 5 surfaces) → Task 2
   - §1.8 (transition-driven refetch) → Tasks 8, 9, 10

2. **Final commands** — once all tasks land:

```bash
go test ./... -count=1                       # full sweep
golangci-lint run ./...                      # lint sweep
git log --oneline origin/main..HEAD          # confirm 12 commits, one per task
```

3. **Manual TTY check** — `kata tui` from a directory unbound to any project should land on the projects view; from a bound directory should still drop into the issue list immediately. `P` should toggle. `Esc` from `viewProjects` should return to the prior list.
