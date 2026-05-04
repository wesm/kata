# Kata TUI Projects View — Design

> **Status:** design / spec. Replaces the modal-based project picker landed in `tui: replace R scope toggle with P project picker modal` with a real top-level projects surface. Companion to `docs/superpowers/specs/2026-05-02-kata-tui-professional-workspace-design.md` (overall TUI direction). Schema is unchanged; one additive read-only API surface; one new TUI view; the modal and its state are retired.

## 1. Locked decisions

These eight decisions are settled here and are not re-litigated by the implementation plan.

1. **Projects are a real surface, not a setting.** Add `viewProjects` as a top-level view that renders a project table. The user browses projects and drills into one to see its issue queue. The current `modalProjectPicker` and its state machine are retired — modal-based scope switching does not survive this work.

2. **`cwd` match wins; `viewProjects` is the boot landing only when `cwd` does not resolve and registered projects exist.** Launching `kata tui` from inside a registered project repo continues to drop straight into that project's issue list, no projects-view stop. Launching from elsewhere with registered projects → boot lands on `viewProjects`. Empty daemon → existing empty state, unchanged. The boot routing requires `bootResolveScope` (or its caller) to disambiguate "no projects at all" from "projects exist but cwd does not resolve" — see §4.

3. **No "remember last project" behavior in v1.** Persisted last-pick semantics fight cwd: launching from repo B and getting repo A's queue would be confusing. The boot rule is purely cwd-derived.

4. **`P` is the binding from issue list → projects view; `Esc` returns to the prior list when one exists; `r` manually refreshes.** Same `P` key as today with refined meaning ("projects" rather than "picker"). `Esc` from `viewProjects` returns to the most recent `viewList` (preserved scope, no refetch). When the user landed on `viewProjects` at boot with no prior list, `Esc` is a no-op. `r` re-dispatches the projects fetch. All three bindings are gated identically to today's TUI: while a search bar or form is focused, the rune routes to the prompt instead of the global handler.

5. **`All projects` is the first row of the projects table, pinned.** It is a scope peer from the user's perspective, not a hidden command. Cursor + Enter on that row enters all-projects scope and the cross-project issue feed.

6. **Per-project stats are server-computed; the All-projects sentinel totals are client-summed.** `GET /api/v1/projects?include=stats` returns one stats triple per project derived from existing tables. The All-projects row's `Open`/`Closed`/`Total` are summed client-side from the rows already in the response; its `Updated` is the maximum `last_event_at` across the rows. This avoids a second SQL pass for the sentinel and guarantees the displayed totals are consistent with the per-row counts on the same frame. The TUI never makes N issue-list calls to compute counts.

7. **Every project-bearing API response projects through a new API-owned `ProjectOut`, not raw `db.Project`.** Today `db.Project` is leaked verbatim by five response types: `ResolveProjectResponse` (via `ProjectResolveBody`), `InitProjectResponse` (same body), `ListProjectsResponse`, `ShowProjectResponse`, `ResetCounterResponse`, `RemoveProjectResponse`. This spec replaces every one of them with `ProjectOut`, an API-shape type whose JSON keys mirror today's `db.Project` tags exactly (so default responses are byte-identical) and adds the optional `Stats *ProjectStatsOut` field with `omitempty`. The shape is now owned at the API layer; `db.Project` becomes an internal type free to evolve independently. The full set of touched response types is enumerated in §7.2.

8. **Boot landing and `P` transitions dispatch a stats fetch; `Esc`-back does not.** When the user lands on `viewProjects` at boot (§4.2 third row) or transitions from `viewList` via `P`, the model's transition handler dispatches `fetchProjectsWithStats` so the table renders fresh stats. When the user returns from `viewProjects` to `viewList` via `Esc`, no projects fetch is dispatched — the user is leaving the projects view, not entering it. SSE-driven invalidation (§6.3) is a freshness optimization while `viewProjects` is active; transition-driven refetch is the correctness guarantee on entry.

The rest of this document expands what these decisions imply for the API shape, the boot routing, the projects view, the SSE invalidation, and the test plan.

## 2. Goal

Make project navigation a first-class browseable surface so the TUI works coherently as multi-project usage scales. The existing modal worked for a few-projects user but does not present project metadata, fights the user's "browse the workspace" mental model, and overlays whatever was rendered before. Replacing it with a real view also gives a natural home for future surfaces (per-project recent activity, project actions like archive/rename inline, etc.) without re-introducing a different modal.

## 3. Scope

**In scope**

- New TUI view `viewProjects` with a project table.
- Boot routing changes in `internal/tui/run.go`'s `bootResolveScope` to land on `viewProjects` when cwd does not resolve and registered projects exist.
- New keymap entry: `Projects` bound to `P` (replaces `SwitchProject`'s modal-opening role; same key).
- API additive: `GET /api/v1/projects?include=stats` returns per-project `{open, closed, last_event_at}`. Default response unchanged.
- New DB helper: `db.ProjectStats(ctx, projectIDs []int64) (map[int64]ProjectStats, error)` returning aggregates from `issues` and `events`.
- TUI client: `ListProjectsWithStats(ctx)` consumes the new endpoint; existing `ListProjects` stays untouched as the no-stats path.
- SSE-driven stats refresh: events that mutate issue counts (`issue.created`, `issue.closed`, `issue.reopened`, `issue.deleted`) trigger a debounced re-fetch of the projects list when `viewProjects` is the active view.
- Retire the modal: delete `modalProjectPicker`, `projectPickerState`, `projectPickerItem`, `openProjectPicker`, `routeProjectPickerKey`, `applyProjectPickerSelection`, `renderProjectPickerModal`. Delete the modal-specific tests in `internal/tui/scope_test.go` (replace with `viewProjects` tests, see §8).
- Clean up the now-stale comment block at `internal/tui/run.go:140-143` that claims `allProjects` is gated and the daemon ships no cross-project endpoint — both are false today.

**Out of scope (deferred)**

- Per-project actions inline in the projects view (archive, rename, merge, reset-counter). Those exist as CLI commands; their TUI wiring is a separate spec.
- Re-sortable columns / interactive sort selector. Default sort is fixed (see §5.3).
- A column for archived projects or a toggle to surface them. Archived projects are hidden from the table; reachable only via CLI.
- Filtering / searching projects within the view. Future work; with <100 projects the table fits without it.
- "Last selected project" persistence. Per §1.3.
- Pagination. Most installations will have <50 projects; the table fits in a normal terminal without paging.
- A new SSE event type for project stats. v1 piggybacks on existing issue events (see §6).
- Path / identity column in the default columns. Identity gets noisy on long Git URLs and adds little; surfaced in the detail footer when a row is highlighted.

## 4. Boot routing

### 4.1 Current behavior (for reference)

`bootResolveScope` (`internal/tui/run.go:169`) today returns one of two scopes:

1. cwd resolves via `POST /projects/resolve(cwd)` → single-project `scope{projectID, projectName, …}`.
2. resolve fails with `project_not_initialized` → `scope{empty: true}`, regardless of how many projects are registered. The pre-gate code dropped into all-projects when ≥1 project existed; that path was disabled when the cross-project list endpoint was missing. The current cross-project endpoint exists (verified — see §3 cleanup), but `bootResolveScope` was never re-wired to use it.

The boot caller then maps the returned scope to an initial view: a non-empty scope lands on `viewList`; `scope{empty: true}` lands on `viewEmpty`. There is no current branch that lands on a cross-project list at boot.

### 4.2 Required boot rule

| daemon state | cwd resolves to a registered project | initial view |
|---|---|---|
| 0 projects | n/a (resolve fails with `project_not_initialized`) | `viewEmpty` (unchanged) |
| 1+ projects | yes | `viewList` in that project's scope (unchanged — cwd fast path) |
| 1+ projects | no (resolve fails with `project_not_initialized`) | `viewProjects` (NEW) |

The third row is the new behavior. `bootResolveScope` cannot return a single `scope` that captures it (a `scope` is per-issue-queue state — empty, single-project, or all-projects), so the boot caller must learn the initial view too.

### 4.3 Implementation sketch

The boot path needs to disambiguate "no projects registered" from "projects exist but cwd does not resolve" before deciding the initial view. Two acceptable shapes; pick one in the plan doc:

1. **Tagged result.** `bootResolveScope` returns a `bootLanding` value with two variants: `landSingleProject(scope)` and `landUnresolved`. The caller separately calls `ListProjects` (or `ListProjectsWithStats`) when it sees `landUnresolved` to decide between `viewEmpty` (zero projects) and `viewProjects` (≥1).
2. **Combined scope+view return.** `bootResolveScope` returns `(scope, initialView, error)`. On `project_not_initialized` it calls `ListProjects` itself and returns either `(scope{empty: true}, viewEmpty, nil)` or `(scope{}, viewProjects, nil)`. Caller plugs the view into `Model.view` directly.

Either way, the boot path now does **one extra round trip** when cwd does not resolve: `ListProjects` (or `ListProjectsWithStats`, depending on whether we want the projects view's first frame populated). Recommend the latter so `viewProjects` renders with stats on the first frame instead of an empty table that fills in after a second round trip.

### 4.4 Notes

- "cwd resolves" means `POST /projects/resolve` returns a project for the cwd's git remote (existing logic).
- The single-project + cwd-resolves path stays the **fastest** path. Users who `kata tui` from inside their working repo continue to get the issue queue immediately. No regression.
- The "1 project total + cwd doesn't resolve" case still opens `viewProjects`. Even with one project, the user explicitly launched from elsewhere; showing them the workspace once is the right call. They press Enter and they're in.
- `viewProjects` has its own back-out: pressing `q` opens the existing quit-confirm modal (`viewProjects` and `viewList` share the global key router).
- The first frame's chrome (title strip, state strip, footer) is rendered the same way as `viewList` — only the body changes.

## 5. Projects view layout

### 5.1 Top-level structure

```
┌───────────────────────────────────────────────────────────────┐
│  kata / projects                          actor=wesm  v0.4.x  │
│  3 projects · live                                            │
├───────────────────────────────────────────────────────────────┤
│   Project              Open   Closed   Total   Updated        │
│ ▶ All projects           21       8      29   just now        │
│   kata                   12       3      15   2m ago          │
│   roborev                 7       2       9   34m ago         │
│   msgvault                2       3       5   2d ago          │
│                                                               │
│   identity: github.com/wesm/kata.git                          │
└───────────────────────────────────────────────────────────────┘
  [↑/↓ k/j] move   [enter] open   [q] quit   [?] help
```

- Title strip: `kata / projects` on the left, the standard right-side actor + version block.
- State strip: project count + sync state — re-uses the same chrome `viewList` renders, swapping "N issues" for "N projects".
- Body: a table with five default columns; one row per project plus the All-projects sentinel.
- Detail footer: when a real project is highlighted, render its `identity` (Git URL or `local:<path>`) in a single line below the table. Highlighting `All projects` shows a one-line description ("issue queue across every registered project").
- Footer hints: standard hint row.

### 5.2 Default columns

| column | source | width strategy |
|---|---|---|
| Project | `projects.name` | flex; truncate with ellipsis if needed |
| Open | aggregate over `issues` where `status='open'` and `deleted_at IS NULL` | right-align, fixed |
| Closed | aggregate over `issues` where `status='closed'` and `deleted_at IS NULL` | right-align, fixed |
| Total | `Open + Closed` | right-align, fixed |
| Updated | `MAX(events.created_at)` for the project, rendered as relative time | flex but compact |

`identity` is **not** a default column. It's surfaced under the table for the highlighted row only. Same reasoning as the "Path" column omission in the professional-workspace spec: long Git URLs blow out narrow widths, and the user only needs it for the row they care about.

### 5.3 Sort

Default sort: `last_event_at` desc (most recent activity first). Ties broken by `name` ascending (case-insensitive). The `All projects` sentinel is always pinned at index 0, regardless of sort.

Re-sortable columns / sort key cycling are deferred (see §3 out-of-scope). The fixed default makes the most-active project most visible, which matches how a user would scan the table.

### 5.4 Cursor and Enter

- Cursor lands on the row matching the active scope on view entry. If no scope is set (boot landing on `viewProjects` without a prior scope), cursor lands on row 1 (first real project after the All-projects sentinel).
- `j`/`k` / `↓`/`↑` move the cursor; `g`/`G` jump to first/last; standard nav.
- `Enter` on a real project: `m.scope = scope{projectID: row.ID, projectName: row.Name, ...}`, drop the issue cache, transition to `viewList`, dispatch `fetchInitial`.
- `Enter` on `All projects`: `m.scope = scope{allProjects: true}`, drop the issue cache, transition to `viewList`, dispatch `fetchInitial` against the cross-project endpoint.
- `q` from `viewProjects` opens the standard quit-confirm modal (no surprise — same as `viewList`).

### 5.5 P binding from `viewList` → `viewProjects`

`P` from the issue list (when no input is focused) transitions `viewList → viewProjects`. The `scope` is preserved on the way out, so `Esc`-back from the projects view returns to the same queue without a refetch (per §1.4). The keymap rename is `SwitchProject → Projects`.

`P` while a search bar / form is focused remains a literal `P` keystroke into the prompt. The existing `m.input.activeField()` gating logic stays untouched.

## 6. Stats data and SSE invalidation

### 6.1 Where the numbers come from

Per-project counts and timestamps come from existing tables. No schema change.

The naive shape `projects ⋈ issues ⋈ events GROUP BY p.id` would multiply each issue row by the number of events for that project, inflating both counters. The batched query must pre-aggregate each child table independently and then join the aggregates to `projects`:

```sql
-- BatchProjectStats: one row per active project, all stats joined to a single
-- per-project key. Uses two pre-aggregated subqueries so issue counts are not
-- multiplied by event row counts (and vice versa).
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
    SELECT
      project_id,
      MAX(created_at) AS last_event_at
    FROM events
    GROUP BY project_id
  )
SELECT
  p.id,
  COALESCE(ic.open_count,   0) AS open_count,
  COALESCE(ic.closed_count, 0) AS closed_count,
  em.last_event_at
FROM projects p
LEFT JOIN issue_counts ic ON ic.project_id = p.id
LEFT JOIN event_max    em ON em.project_id = p.id
WHERE p.deleted_at IS NULL
ORDER BY p.id;
```

The `LEFT JOIN`s preserve projects with zero issues (counts collapse to zero via `COALESCE`) and zero events (`last_event_at` stays `NULL`). `ORDER BY p.id` keeps the daemon-side response stable; the TUI re-sorts client-side per §5.3.

The single-project variant `ProjectStats(ctx, projectID int64)` is structurally the same query with a `WHERE p.id = ?` predicate; in practice the batched query is cheap enough that we use it everywhere and filter to one row in the rare callers that want a single project.

`MAX(events.created_at)` is `NULL` for a project with zero events. The TUI renders that as `—` (em dash), not "1969 ago". Tests pin this.

Soft-deleted issues (`issues.deleted_at IS NOT NULL`) do **not** count toward `open` or `closed`. This is consistent with how `ListIssues` already filters them.

Archived projects (`projects.deleted_at IS NOT NULL`) are excluded from the response. The existing `ListProjects` already filters them, and `?include=stats` follows the same rule.

The All-projects sentinel row is **not** computed by this query. The TUI sums the per-row stats client-side for that row (per §1.6). This keeps the SQL one-pass and the displayed totals consistent with the per-row counts on the same frame.

### 6.2 Transition-driven refetch (correctness)

Two transitions dispatch `fetchProjectsWithStats`: the boot path that lands on `viewProjects` (§4.2 third row, dispatched as the boot cmd alongside the SSE-stream cmd), and the `P`-binding handler in `viewList` that transitions to `viewProjects`. Both happen inside `Update`, not inside `Model.Init` — `Init` runs once per program in Bubble Tea, not once per view entry.

The third transition into `viewProjects` is `Esc`-back from a list view that the user originally entered via `P`. That transition does **not** dispatch a fetch. The user is leaving the projects view to return to it; the table state from before the `P → viewList` jaunt is what the user expects to see, and SSE invalidation has been keeping the `m.projectsStale` flag honest in the background. If the flag is set on `Esc`-return, the standard 500ms-debounced refetch (§6.3) fires; otherwise the cached table renders immediately.

This is the correctness guarantee for the two entry transitions: the table renders with up-to-date stats even when SSE-driven invalidation missed an event (reconnect window, missed delta, etc.). The first frame after entry may render briefly with stale-but-cached stats; the in-flight `fetchProjectsWithStats` updates the rows when its `projectsLoadedMsg` arrives. The user sees a populated table immediately, not an empty placeholder.

### 6.3 SSE invalidation (freshness)

While `viewProjects` is active, the TUI watches the SSE stream the rest of the TUI already consumes. The events that change the numbers are `issue.created`, `issue.closed`, `issue.reopened`, `issue.deleted`, and (for the timestamp column) any event affecting any project the table is showing.

v1 strategy:

- When `viewProjects` is active and an event arrives whose `project_id` matches a row currently rendered, mark the projects table stale.
- Coalesce stale-flips inside a 500ms window; refetch once at the end of the window. This keeps the TUI calm during a burst (e.g. an SSE catch-up after reconnect) without making counters lag.
- If `viewProjects` is **not** active, ignore the event for this purpose. We re-fetch on view re-entry anyway (per §6.2). Tracked by a simple `m.projectsStale bool` flag flipped on every relevant event.

We deliberately do **not** add a `project.stats_updated` event type. The existing event surface is sufficient; adding new types belongs to a sync-layer spec, not this one.

### 6.4 Refetch transport

A standalone `tea.Cmd` named `fetchProjectsWithStats` that calls `Client.ListProjectsWithStats` and dispatches a `projectsLoadedMsg`. The existing no-stats `fetchProjects` cmd (used by the boot project-name cache) stays alongside; the two coexist because the boot cache only needs names. The projects view's `m.projectStats map[int64]ProjectStats` is populated on every `projectsLoadedMsg` carrying stats; the existing `m.projectsByID` cache is updated in the same message handler.

## 7. Wire shapes

### 7.1 Daemon endpoint

Existing endpoint, additive query parameter.

```
GET /api/v1/projects                  → existing shape
GET /api/v1/projects?include=stats    → projects + stats per row
```

Default response (unchanged):

```json
{ "projects": [ { "id": 7, "identity": "...", "name": "kata", ... } ] }
```

With `?include=stats`:

```json
{
  "projects": [
    {
      "id": 7,
      "identity": "github.com/wesm/kata.git",
      "name": "kata",
      "stats": {
        "open": 12,
        "closed": 3,
        "last_event_at": "2026-05-04T13:42:01.221Z"
      }
    }
  ]
}
```

`stats.last_event_at` is ISO-8601 with millisecond precision (matches `events.created_at` formatting), or `null` for a project with zero events. `open` and `closed` are non-negative integers.

The query parameter name `include=stats` (a comma-separated set, even though only `stats` is defined) leaves room for future additions (`?include=stats,labels`, etc.) without breaking the wire. v1 only honors `stats`.

### 7.2 Go-level types

Today `internal/api/types.go` leaks `db.Project` verbatim from five distinct response types. The full set:

| line | type | route |
|---|---|---|
| `:50`  | `ProjectResolveBody.Project` (used by `ResolveProjectResponse` and `InitProjectResponse`) | `POST /api/v1/projects/resolve`, `POST /api/v1/projects` |
| `:82`  | `ListProjectsResponse.Body.Projects []db.Project` | `GET /api/v1/projects` |
| `:89`  | `ShowProjectResponse.Body.Project` | `GET /api/v1/projects/{id}` |
| `:130` | `ResetCounterResponse.Body.Project` | `POST /api/v1/projects/{id}/reset-counter` |
| `:355` | `RemoveProjectResponse.Body.Project` | `DELETE /api/v1/projects/{id}` |

This spec replaces every one of them with `ProjectOut`. The reason is twofold: (a) the list response gains an optional `Stats` field that has no place on `db.Project`; (b) decoupling the wire from the DB row lets `db.Project` evolve (e.g. add internal-only columns) without breaking API consumers. Doing this consistently across the five surfaces keeps the wire shape coherent — partial projection (e.g. only `List` and `Show`) would mean `kata init` and `kata projects remove` callers get a different field set than `kata projects list`, which is the exact inconsistency the projection is meant to remove.

The new shape:

```go
// internal/api/types.go

type ProjectStatsOut struct {
    Open        int        `json:"open"`
    Closed      int        `json:"closed"`
    LastEventAt *time.Time `json:"last_event_at"`
}

// ProjectOut is the API-shape of a project. JSON keys mirror today's
// db.Project tags exactly so the default (no ?include=stats) response is
// byte-identical to the current wire.
//
// The field set is derived from internal/db/types.go:10 (db.Project) and
// must be kept exhaustive: id, uid, identity, name, created_at,
// next_issue_number, deleted_at (omitempty). No updated_at — db.Project
// has none.
type ProjectOut struct {
    ID              int64      `json:"id"`
    UID             string     `json:"uid"`
    Identity        string     `json:"identity"`
    Name            string     `json:"name"`
    CreatedAt       time.Time  `json:"created_at"`
    NextIssueNumber int64      `json:"next_issue_number"`
    DeletedAt       *time.Time `json:"deleted_at,omitempty"`

    Stats *ProjectStatsOut `json:"stats,omitempty"` // present only with ?include=stats
}

// All five surfaces switch to ProjectOut:
type ProjectResolveBody struct {
    Project       ProjectOut      `json:"project"`
    Alias         db.ProjectAlias `json:"alias"`
    WorkspaceRoot string          `json:"workspace_root,omitempty"`
}

type ListProjectsResponse struct {
    Body struct {
        Projects []ProjectOut `json:"projects"`
    }
}
type ShowProjectResponse struct {
    Body struct {
        Project ProjectOut        `json:"project"`
        Aliases []db.ProjectAlias `json:"aliases"`
    }
}
type ResetCounterResponse struct {
    Body struct {
        Project ProjectOut `json:"project"`
    }
}
type RemoveProjectResponse struct {
    Body struct {
        Project ProjectOut `json:"project"`
        Event   *db.Event  `json:"event"`
    }
}
```

A single `dbProjectToOut(p db.Project) ProjectOut` helper is the only call site that maps between the two; every handler routes through it.

`LastEventAt *time.Time` (not `*string`) keeps timestamp formatting centralized in the JSON encoder — `time.Time`'s default JSON marshaling already produces RFC3339 with millisecond precision, matching how `events.created_at` is rendered today (`internal/db/types.go:55` and adjacent fields are `time.Time`). Handler-level string conversion would risk drift between the projects endpoint's format and the rest of the API.

`omitempty` on `Stats` keeps the wire identical to today's response when the query parameter is absent — no breaking change for the CLI's `kata projects list` or any other consumer. JSON-bytes-snapshot tests on each of the five response types pin the default shape so any future `db.Project` field addition is caught loudly rather than leaking onto the wire.

### 7.3 TUI client

`internal/tui/client.go`:

```go
// Existing — keep verbatim, used by the projects-name-cache boot fetch.
func (c *Client) ListProjects(ctx context.Context) ([]ProjectSummary, error) { … }

// NEW — used by viewProjects.
func (c *Client) ListProjectsWithStats(ctx context.Context) ([]ProjectSummaryWithStats, error) {
    return c.list(ctx, "/api/v1/projects?include=stats", &resp)
}
```

`ProjectSummaryWithStats` extends `ProjectSummary` with the stats triple (`Open int`, `Closed int`, `LastEventAt *time.Time`). The two existing call sites of `ListProjects` (boot cache, single-project scope's project-name backfill) keep using `ListProjects` and ignore stats — they only want names.

## 8. Test plan

### 8.1 DB unit tests (`internal/db/queries_projects_test.go` extension)

- `TestProjectStats_SingleProject`: a project with N open + M closed issues + K events returns `{open: N, closed: M, last_event_at: max(events.created_at)}`.
- `TestProjectStats_BatchAcrossProjects`: a single batched call against three projects returns three rows; counts and timestamps are correctly partitioned by `project_id`.
- `TestProjectStats_ExcludesSoftDeletedIssues`: a project with one soft-deleted open issue and one live open issue reports `open=1`.
- `TestProjectStats_NullLastEventForEmptyProject`: a project with zero events returns `LastEventAt == nil`.
- `TestProjectStats_ExcludesArchivedProjects`: a project with `projects.deleted_at IS NOT NULL` is **not** in the batched result.

### 8.2 Daemon endpoint tests (`internal/daemon/handlers_projects_test.go` extension)

- `TestListProjects_DefaultShapeUnchanged`: `GET /api/v1/projects` returns the v1 shape; no `stats` field is present on any row.
- `TestListProjects_WithStatsIncludesAggregates`: `GET /api/v1/projects?include=stats` returns each project with a populated `stats` block; counts and timestamps match issue / event fixtures.
- `TestListProjects_WithStatsHandlesEmptyProjects`: a freshly-initialized project (zero issues, zero events) has `stats.open=0`, `stats.closed=0`, `stats.last_event_at=null`.
- `TestListProjects_WithStatsHidesArchivedProjects`: an archived project is omitted from both shapes (consistent with current behavior).

### 8.3 TUI client tests (`internal/tui/client_test.go` extension)

- `TestClient_ListProjectsWithStats_Decodes`: feed a fixture response with stats; assert the typed result populates `Open`, `Closed`, `LastEventAt`.
- `TestClient_ListProjectsWithStats_NilLastEvent`: a row with `"last_event_at": null` decodes as `LastEventAt == nil` (no panic).

### 8.4 TUI boot routing (`internal/tui/run_test.go` extension)

- `TestBoot_CwdResolves_DropsToList`: cwd matches a registered project → initial `Model.view == viewList`.
- `TestBoot_CwdUnresolved_ManyProjects_LandsOnViewProjects`: cwd does not match any project, daemon returns ≥1 project → initial `Model.view == viewProjects`.
- `TestBoot_NoProjects_LandsOnEmpty`: daemon returns zero projects → `Model.view == viewEmpty` (regression guard).

### 8.5 viewProjects unit + golden tests (`internal/tui/projects_view_test.go` NEW)

- `TestProjectsView_AllProjectsSentinelFirst`: render with three real projects → row 0 is "All projects", rows 1-3 are the projects sorted by `last_event_at` desc.
- `TestProjectsView_SortByLastEventDesc`: with mixed timestamps the table renders most-recent-first; ties break by name asc.
- `TestProjectsView_CursorOpensProject`: cursor on a project row + Enter → resulting model has `view == viewList`, `scope.projectID` matches the row.
- `TestProjectsView_CursorOpensAllProjects`: cursor on the sentinel + Enter → `view == viewList`, `scope.allProjects == true`.
- `TestProjectsView_PFromListReturnsHere`: `Model{view: viewList}` + `tea.KeyMsg{P}` → resulting model has `view == viewProjects`; scope is preserved.
- `TestProjectsView_PWhileInputFocusedRoutesToPrompt`: scope-test pattern — `P` while `m.input` is non-nil reaches the prompt, does not transition view.
- `TestProjectsView_EscReturnsToPriorList`: `viewList → P → viewProjects → Esc` → `view == viewList`, scope unchanged, no `fetchInitial` cmd dispatched (cache reused).
- `TestProjectsView_EscNoOpOnBootEntry`: model where `viewProjects` was the boot landing (no prior list view) + `Esc` → view stays `viewProjects`, no transition.
- `TestProjectsView_RRefreshes`: `r` from `viewProjects` → dispatches `fetchProjectsWithStats`, view stays `viewProjects`.
- `TestProjectsView_AllProjectsSentinelTotalsAreClientSummed`: render with three project rows; assert the sentinel row's `Open`, `Closed`, `Total` equal the per-row sums and `Updated` equals the row-max `last_event_at`.
- `TestProjectsView_EntryDispatchesFetch`: every entry path (boot, `P` from list, `Esc` ... wait, Esc back doesn't refetch — this case is `P` from list and boot only) dispatches `fetchProjectsWithStats`. Subtests for boot landing + P-transition.
- `TestProjectsView_LastEventFormatting`: a row with `LastEventAt == nil` renders `—`; a recent timestamp renders as relative time.
- `TestProjectsView_HighlightShowsIdentityFooter`: cursor on a project → identity line under the table; cursor on the sentinel → description line.
- Golden snapshots: `projects-view-wide.txt` (120 cols) and `projects-view-narrow.txt` (80 cols) at typical fixture state.

### 8.6 SSE invalidation tests (`internal/tui/sse_update_test.go` extension)

- `TestProjectsView_StaleOnIssueCreated`: `viewProjects` active; an `issue.created` SSE event arrives → `m.projectsStale == true`.
- `TestProjectsView_DebouncesRefetch`: three SSE events within 500ms → exactly one `fetchProjectsWithStats` dispatched.
- `TestProjectsView_IgnoresEventsWhenNotActive`: `viewList` active → an SSE event does not flip `projectsStale` (we'll refetch on next view entry anyway).

### 8.7 e2e

`TestSmoke_ProjectsViewLoop` (under `e2e/`):

1. Start daemon with three registered projects: `proj-a` (0 issues), `proj-b` (5 open / 2 closed), `proj-c` (10 open / 3 closed, all activity recent).
2. Launch TUI from a directory that does not match any project's identity → first frame shows `viewProjects`.
3. Assert the `proj-c` row shows `Open=10 Closed=3` and is rendered above `proj-b` (`last_event_at` desc).
4. Press Enter on `All projects` → next frame is `viewList` with cross-project rows.
5. Press `P` → returns to `viewProjects`, scope preserved.
6. Create an issue against `proj-a` via the daemon → SSE event fires → `proj-a`'s row updates to `Open=1`.

## 9. Open questions / tunables

- **Identity footer on highlight: full URL or short label?** A long Git URL (`github.com/some-org/very-long-repo-name.git`) can blow out narrow widths even on the footer line. Recommend rendering it un-truncated (one footer line), with the rest of the body re-laying out around it. If the URL is longer than `width-2`, truncate with ellipsis. Implementation detail; pinned in tests.
- **First-row cursor on entry from `P`.** When the user lands on `viewProjects` from `viewList` with an active scope (`scope.projectID == 7`), the cursor should land on that row, not on the sentinel. When entering from boot with no prior scope, cursor lands on row 1. Already covered by §5.4.
- **Should `viewProjects` survive a daemon restart?** The TUI re-derives state from the daemon on reconnect. If the user is on `viewProjects` when the daemon restarts, the post-reconnect frame stays on `viewProjects` and the next `fetchProjectsWithStats` populates fresh stats. No special handling needed — a behavioral test in §8.6 documents this.
- **CLI parity for `--include=stats`.** `kata projects list` currently prints names, identities, and counters from the no-stats response. Adding a `--stats` flag is one CLI commit's worth of work and makes the same data available outside the TUI. Recommend yes, but track separately from this spec — plan can add it as a tail commit if convenient.

## 10. Sequencing

This spec lands as a single design pass. Implementation may be split into multiple commits per the plan doc (see §11), but the wire shape is decided here.

Order of operations from the user's perspective:

1. Spec accepted (this doc).
2. Implementation plan written via `superpowers:writing-plans`.
3. Plan executed via `superpowers:subagent-driven-development`. The natural commit groupings are below.
4. End user sees: launching `kata tui` from a non-project directory now lands on a project table; selecting a project drops them straight into that project's queue; `P` from a queue returns to the table.

## 11. Implementation order (for the plan doc)

Hand off to `superpowers:writing-plans`. Expected ordering:

1. **DB layer**
   - Add `db.BatchProjectStats(ctx) (map[int64]ProjectStats, error)` (or equivalent name) implementing the pre-aggregated CTE query from §6.1. Returns one row per active (non-archived) project, including projects with zero issues and zero events.
   - Single-project variant `db.ProjectStats(ctx, projectID int64) (ProjectStats, error)` is a thin wrapper that runs the same query with a `WHERE p.id = ?` predicate.
   - Tests per §8.1.

2. **API: `ProjectOut` projection (all five surfaces)**
   - Define `ProjectOut` and `ProjectStatsOut` in `internal/api/types.go` per §7.2. Field set derived exhaustively from `db.Project` (`id, uid, identity, name, created_at, next_issue_number, deleted_at`); no fictional `updated_at`.
   - Replace every `db.Project` reference in API response types with `ProjectOut`: `ProjectResolveBody.Project` (covers `ResolveProjectResponse` and `InitProjectResponse`), `ListProjectsResponse.Body.Projects`, `ShowProjectResponse.Body.Project`, `ResetCounterResponse.Body.Project`, `RemoveProjectResponse.Body.Project`. Five surfaces; one consistent projection.
   - Add a `dbProjectToOut(p db.Project) ProjectOut` helper that every project-returning handler routes through.
   - Add JSON-bytes-snapshot tests on each of the five response types so a future `db.Project` field addition is caught loudly rather than leaking onto the wire.

3. **API + daemon: `?include=stats`**
   - Extend the `GET /api/v1/projects` handler in `internal/daemon/handlers_projects.go` to honor `?include=stats` (parse a comma-separated `include` query param, dispatch to `db.BatchProjectStats` when `stats` is present, populate `ProjectOut.Stats` per row).
   - Tests per §8.2.

4. **TUI client**
   - Add `Client.ListProjectsWithStats` and the `ProjectSummaryWithStats` shape (`internal/tui/client.go`, `client_types.go`). `LastEventAt` is `*time.Time` per §7.2.
   - Tests per §8.3.

5. **Boot routing**
   - Refactor `bootResolveScope` (`internal/tui/run.go`) per §4.3. Pick one of the two shapes proposed there in the plan; recommend the combined `(scope, initialView, error)` return so the caller plugs the view into `Model.view` directly.
   - The unresolved-cwd branch calls `Client.ListProjectsWithStats` to disambiguate `viewEmpty` from `viewProjects` and to populate the first frame's stats.
   - Tests per §8.4.

6. **viewProjects TUI surface**
   - Add the `viewProjects` enum value and the body rendering function (new file `internal/tui/projects_view.go` plus `projects_view_render.go`).
   - Wire the view into `Model.View()` — render branch identical in shape to `viewList` for chrome.
   - Add the `Projects` keymap entry (rename `SwitchProject → Projects`); update help text and golden snapshots.
   - Add `m.projectStats` cache + `fetchProjectsWithStats` cmd (extending the existing `projectsLoadedMsg`).
   - Add Enter / P / Esc / r key handling on `viewProjects`. Implement client-side All-projects sentinel summing per §1.6.
   - Wire entry-driven refetch dispatch (boot landing, P-transition) per §6.2.
   - Tests per §8.5.

7. **SSE invalidation**
   - Hook `viewProjects` into the SSE event router so issue events flip `m.projectsStale` and dispatch a debounced refetch (when active).
   - Tests per §8.6.

8. **Retire the modal**
   - Delete `modalProjectPicker`, `projectPickerState`, `projectPickerItem`, `openProjectPicker`, `routeProjectPickerKey`, `applyProjectPickerSelection`, `renderProjectPickerModal`.
   - Delete the modal-specific tests in `scope_test.go` (the `TestProjectPicker_*` family) — they were the picker's home before this spec.
   - Delete the picker render branches in `Model.View()`.
   - Delete the now-stale comment block at `internal/tui/run.go:140-167` describing the gated all-projects path.

9. **e2e**
   - `TestSmoke_ProjectsViewLoop` per §8.7.

10. **Cross-cutting review**
    - Help golden snapshots (narrow + wide) refreshed.
    - `kata projects list --stats` CLI flag if scope permits (per §9 tunables); otherwise drop a follow-up note in the plan.
    - Master spec doc edits if the projects-view direction belongs in `2026-04-29-kata-design.md` (light touch — one paragraph in §3 noting `viewProjects` exists). Or skipped, if the master spec is being kept narrow.
