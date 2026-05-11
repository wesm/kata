# Closure Justification and Anti-Agent-Abuse Guards — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `kata close` an evidence-bearing assertion of completion, and stop the parent-close-with-open-children and rapid-sibling-close patterns. Add recovery tooling so a human can clean up bad bulk-closures.

**Architecture:** Three layers of defense, all daemon-side except CLI ergonomics. (1) Per-close validation requires a substantive message and typed evidence whose shape depends on `--reason`. (2) Three structural guards refuse closes that match the observed abuse shapes: parent-close while open children remain, ≥4th sibling-close in a 5-min window under the same parent, and a same-actor / same-parent / same-message guard with a 30-min window for `done` / `audit-no-change` closes. (3) Recovery: a `kata audit closes` read view and bulk filters on `kata reopen` with required confirmation. Throttle refusals emit `close.throttled` events; `kata events --tail` renders them prominently.

**Tech Stack:** Go 1.x, SQLite via standard `database/sql`, `huma/v2` for the HTTP API, `cobra` for the CLI, `testify` (`assert` / `require`) for tests.

**Spec:** `docs/superpowers/specs/2026-05-10-anti-agent-justification-design.md`

---

## Files

### Created

- `internal/api/evidence.go` — typed-union Evidence types and JSON marshal/unmarshal.
- `internal/daemon/close_validation.go` — message substance check and per-reason evidence matrix.
- `internal/daemon/close_guards.go` — parent-completeness check, sibling throttle, repeated-message guard.
- `cmd/kata/audit.go` — `kata audit` command group registration.
- `cmd/kata/audit_closes.go` — `kata audit closes` subcommand.
- Co-located `*_test.go` files for each of the above.

### Modified

- `internal/db/schema.sql` — expand `closed_reason` CHECK constraint to include `superseded` and `audit-no-change`.
- `internal/db/queries.go` — `CloseIssue` accepts `message` + `evidence`; new helpers `OpenChildrenCount`, `RecentSiblingCloses`, `RecentSameMessageClose`.
- `internal/db/queries_events.go` — add `EventsByTypeWithFilters` helper for the audit view.
- `internal/api/types.go` — extend `ActionRequest.Body` with `Message`, `Evidence`, `DryRun`.
- `internal/daemon/handlers_actions.go` — wire validation, parent-completeness, throttles, and `close.throttled` event emission.
- `cmd/kata/close.go` — new flag set (canonical `--reason` / `--message` / `--evidence`, sugar aliases, `--dry-run`).
- `cmd/kata/reopen.go` — bulk mode with `--closed-by` / `--since` / `--until` / `--parent` / `--reason` / `--confirm` / `--dry-run`.
- `cmd/kata/main.go` — register `audit` command.
- `cmd/kata/events.go` — render `close.throttled` events with a visible marker in the default text/table stream.
- `cmd/kata/quickstart.go` — rewrite close step, promote it earlier in the list.
- `AGENTS.md`, `README.md`, `CLAUDE.md` — text updates per §3.14 of the spec.

---

## Task 1: Schema — expand `closed_reason` enum

**Files:**
- Modify: `internal/db/schema.sql:38`
- Test: `internal/db/queries_issues_test.go` (existing close tests; add cases)

- [ ] **Step 1: Write failing tests for the two new reasons**

Append to `internal/db/queries_issues_test.go`:

```go
func TestCloseIssue_SupersededReasonAccepted(t *testing.T) {
    d, cleanup := openTestDB(t)
    defer cleanup()
    ctx := context.Background()

    proj := mustCreateProject(t, d, "p")
    issue := mustCreateIssue(t, d, proj.ID, "x")

    _, _, _, err := d.CloseIssue(ctx, issue.ID, "superseded", "wesm",
        "Replaced by #99 with a different scope.", nil)
    require.NoError(t, err)
}

func TestCloseIssue_AuditNoChangeReasonAccepted(t *testing.T) {
    d, cleanup := openTestDB(t)
    defer cleanup()
    ctx := context.Background()

    proj := mustCreateProject(t, d, "p")
    issue := mustCreateIssue(t, d, proj.ID, "x")

    _, _, _, err := d.CloseIssue(ctx, issue.ID, "audit-no-change", "wesm",
        "Reviewed and concluded no change needed.", nil)
    require.NoError(t, err)
}
```

These will not compile yet — `CloseIssue` is still the 4-arg form. That's intentional. Task 4 changes the signature; until then, mark these tests `t.Skip("blocked on Task 4")` if the rest of the file needs to build. Most clean approach: add the tests in this task as written and let the build break — Task 4 lands the signature change and unblocks them. Use whichever ordering matches your branching strategy.

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/db -run TestCloseIssue_SupersededReasonAccepted
```

Expected: compile error (`too few arguments to CloseIssue`) or runtime SQLite CHECK failure (`closed_reason IN (...)`) depending on which version of the function is current. Either is acceptable as proof the test guards the change.

- [ ] **Step 3: Update the schema**

In `internal/db/schema.sql:38`:

```sql
  closed_reason TEXT CHECK(closed_reason IN ('done','wontfix','duplicate','superseded','audit-no-change')),
```

- [ ] **Step 4: Update schema-completeness test if it asserts the enum**

Inspect `internal/db/schema_completeness_test.go`. If it enumerates valid `closed_reason` values, add the two new ones.

- [ ] **Step 5: Run the existing close test suite**

```
go test ./internal/db -run TestCloseIssue
```

Expected: existing tests still pass; the new ones may still fail until Task 4. That's fine.

- [ ] **Step 6: Commit**

```bash
git add internal/db/schema.sql internal/db/queries_issues_test.go internal/db/schema_completeness_test.go
git commit -m "schema: expand closed_reason enum with superseded, audit-no-change"
```

---

## Task 2: API — define Evidence union types

**Files:**
- Create: `internal/api/evidence.go`
- Test: `internal/api/evidence_test.go`

- [ ] **Step 1: Write failing tests for Evidence marshal/unmarshal**

Create `internal/api/evidence_test.go`:

```go
package api

import (
    "encoding/json"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestEvidence_MarshalCommit(t *testing.T) {
    e := Evidence{Type: EvidenceCommit, SHA: "abc1234"}
    bs, err := json.Marshal(e)
    require.NoError(t, err)
    assert.JSONEq(t, `{"type":"commit","sha":"abc1234"}`, string(bs))
}

func TestEvidence_UnmarshalReviewedPaths(t *testing.T) {
    in := `{"type":"reviewed-paths","paths":["a/b.go","c/d.go"]}`
    var e Evidence
    require.NoError(t, json.Unmarshal([]byte(in), &e))
    assert.Equal(t, EvidenceReviewedPaths, e.Type)
    assert.Equal(t, []string{"a/b.go", "c/d.go"}, e.Paths)
}

func TestEvidence_UnmarshalDuplicateOf(t *testing.T) {
    in := `{"type":"duplicate-of","issue":7}`
    var e Evidence
    require.NoError(t, json.Unmarshal([]byte(in), &e))
    assert.Equal(t, EvidenceDuplicateOf, e.Type)
    assert.Equal(t, int64(7), e.Issue)
}

func TestEvidence_UnmarshalUnknownTypeIsError(t *testing.T) {
    in := `{"type":"bogus"}`
    var e Evidence
    err := json.Unmarshal([]byte(in), &e)
    require.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/api -run TestEvidence
```

Expected: FAIL — `Evidence` and the `EvidenceCommit` constants do not exist.

- [ ] **Step 3: Implement Evidence**

Create `internal/api/evidence.go`:

```go
package api

import (
    "encoding/json"
    "fmt"
)

// EvidenceType is one of the typed-union tags carried on a close action.
// The set is intentionally closed; see spec §3.3.
type EvidenceType string

const (
    EvidenceCommit         EvidenceType = "commit"
    EvidencePR             EvidenceType = "pr"
    EvidenceTest           EvidenceType = "test"
    EvidenceReviewedPaths  EvidenceType = "reviewed-paths"
    EvidenceNoChangeAudit  EvidenceType = "no-change-audit"
    EvidenceDuplicateOf    EvidenceType = "duplicate-of"
    EvidenceSupersededBy   EvidenceType = "superseded-by"
)

// Evidence is a typed-union element of the close action's evidence array.
// Only the fields appropriate to Type are populated; per-reason validation
// in internal/daemon/close_validation.go enforces shape.
type Evidence struct {
    Type EvidenceType `json:"type"`

    SHA       string   `json:"sha,omitempty"`       // commit
    URL       string   `json:"url,omitempty"`       // pr
    Command   string   `json:"command,omitempty"`   // test
    Paths     []string `json:"paths,omitempty"`     // reviewed-paths
    Rationale string   `json:"rationale,omitempty"` // no-change-audit
    Issue     int64    `json:"issue,omitempty"`     // duplicate-of, superseded-by
}

var validEvidenceTypes = map[EvidenceType]struct{}{
    EvidenceCommit:        {},
    EvidencePR:            {},
    EvidenceTest:          {},
    EvidenceReviewedPaths: {},
    EvidenceNoChangeAudit: {},
    EvidenceDuplicateOf:   {},
    EvidenceSupersededBy:  {},
}

// UnmarshalJSON rejects unknown evidence types early so daemon validation
// never has to special-case malformed wire input.
func (e *Evidence) UnmarshalJSON(bs []byte) error {
    type raw Evidence
    var r raw
    if err := json.Unmarshal(bs, &r); err != nil {
        return err
    }
    if _, ok := validEvidenceTypes[r.Type]; !ok {
        return fmt.Errorf("evidence: unknown type %q", r.Type)
    }
    *e = Evidence(r)
    return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./internal/api -run TestEvidence -v
```

Expected: PASS on all four.

- [ ] **Step 5: Commit**

```bash
git add internal/api/evidence.go internal/api/evidence_test.go
git commit -m "api: add Evidence typed-union for close payloads"
```

---

## Task 3: API — extend `ActionRequest` with message, evidence, dry-run

**Files:**
- Modify: `internal/api/types.go:417-424`
- Test: `internal/api/types_test.go` (create if absent)

- [ ] **Step 1: Write failing test for the new fields**

Append to (or create) `internal/api/types_test.go`:

```go
package api

import (
    "encoding/json"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestActionRequest_RoundTripWithEvidence(t *testing.T) {
    in := `{
      "actor": "wesm",
      "reason": "done",
      "message": "Fixed Safari callback double-submit.",
      "evidence": [{"type":"commit","sha":"abc1234"}],
      "dry_run": false
    }`
    var body struct {
        Actor    string     `json:"actor"`
        Reason   string     `json:"reason"`
        Message  string     `json:"message"`
        Evidence []Evidence `json:"evidence"`
        DryRun   bool       `json:"dry_run"`
    }
    require.NoError(t, json.Unmarshal([]byte(in), &body))
    assert.Equal(t, "done", body.Reason)
    assert.Equal(t, "Fixed Safari callback double-submit.", body.Message)
    require.Len(t, body.Evidence, 1)
    assert.Equal(t, EvidenceCommit, body.Evidence[0].Type)
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/api -run TestActionRequest_RoundTripWithEvidence
```

Expected: compile failure or unmarshal error (Evidence on ActionRequest doesn't exist yet at the top-level struct — this test exercises the shape we'll add).

- [ ] **Step 3: Extend `ActionRequest`**

In `internal/api/types.go:417`, replace the existing struct:

```go
type ActionRequest struct {
    ProjectID int64 `path:"project_id" required:"true"`
    Number    int64 `path:"number" required:"true"`
    Body      struct {
        Actor    string     `json:"actor" required:"true"`
        Reason   string     `json:"reason,omitempty" enum:"done,wontfix,duplicate,superseded,audit-no-change,"`
        Message  string     `json:"message,omitempty"`
        Evidence []Evidence `json:"evidence,omitempty"`
        DryRun   bool       `json:"dry_run,omitempty"`
    }
}
```

Note: `Reason` keeps the trailing-comma form so an empty string remains acceptable (the daemon converts empty → reject; legacy clients that omit the field receive a clean error rather than schema-validation noise).

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/api -run TestActionRequest_RoundTripWithEvidence -v
```

Expected: PASS.

- [ ] **Step 5: Build the whole module**

```
go build ./...
```

Expected: any caller of the old `ActionRequest` Body shape that needed updating is now flagged. The daemon handler in `internal/daemon/handlers_actions.go` only reads `Actor` and `Reason` today and will keep compiling; the new fields are additive.

- [ ] **Step 6: Commit**

```bash
git add internal/api/types.go internal/api/types_test.go
git commit -m "api: extend ActionRequest with message, evidence, dry_run"
```

---

## Task 4: DB — `CloseIssue` accepts message + evidence; persist on event payload

**Files:**
- Modify: `internal/db/queries.go:928` (`CloseIssue`)
- Test: `internal/db/queries_issues_test.go` (extend existing tests)

- [ ] **Step 1: Write failing test for richer payload**

Append to `internal/db/queries_issues_test.go`:

```go
func TestCloseIssue_PersistsMessageAndEvidence(t *testing.T) {
    d, cleanup := openTestDB(t)
    defer cleanup()
    ctx := context.Background()

    proj := mustCreateProject(t, d, "p")
    issue := mustCreateIssue(t, d, proj.ID, "x")

    evidence := []api.Evidence{{Type: api.EvidenceCommit, SHA: "abc1234"}}
    _, evt, _, err := d.CloseIssue(ctx, issue.ID, "done", "wesm",
        "Fixed the bug and ran tests.", evidence)
    require.NoError(t, err)
    require.NotNil(t, evt)

    // Payload should contain reason, message, and evidence.
    assert.Contains(t, evt.Payload, `"reason":"done"`)
    assert.Contains(t, evt.Payload, `"message":"Fixed the bug and ran tests."`)
    assert.Contains(t, evt.Payload, `"evidence":[`)
    assert.Contains(t, evt.Payload, `"type":"commit"`)
    assert.Contains(t, evt.Payload, `"sha":"abc1234"`)
}
```

You will need to import `"github.com/wesm/kata/internal/api"` at the top of the file.

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/db -run TestCloseIssue_PersistsMessageAndEvidence
```

Expected: compile error — `CloseIssue` has the old signature.

- [ ] **Step 3: Update `CloseIssue` signature and payload assembly**

In `internal/db/queries.go:928`, replace:

```go
func (d *DB) CloseIssue(
    ctx context.Context,
    issueID int64,
    reason, actor, message string,
    evidence []api.Evidence,
) (Issue, *Event, bool, error) {
    if reason == "" {
        reason = "done"
    }
    tx, err := d.BeginTx(ctx, nil)
    if err != nil {
        return Issue{}, nil, false, err
    }
    defer func() { _ = tx.Rollback() }()

    issue, projectName, err := lookupIssueForEvent(ctx, tx, issueID)
    if err != nil {
        return Issue{}, nil, false, err
    }
    if issue.Status == "closed" {
        return issue, nil, false, tx.Commit()
    }
    if _, err := tx.ExecContext(ctx,
        `UPDATE issues
         SET status        = 'closed',
             closed_reason = ?,
             closed_at     = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
             updated_at    = strftime('%Y-%m-%dT%H:%M:%fZ','now')
         WHERE id = ?`, reason, issueID); err != nil {
        return Issue{}, nil, false, fmt.Errorf("close: %w", err)
    }

    payloadBytes, err := json.Marshal(struct {
        Reason   string         `json:"reason"`
        Message  string         `json:"message,omitempty"`
        Evidence []api.Evidence `json:"evidence,omitempty"`
    }{Reason: reason, Message: message, Evidence: evidence})
    if err != nil {
        return Issue{}, nil, false, fmt.Errorf("close payload: %w", err)
    }

    evt, err := d.insertEventTx(ctx, tx, eventInsert{
        ProjectID:   issue.ProjectID,
        ProjectName: projectName,
        IssueID:     &issue.ID,
        IssueNumber: &issue.Number,
        Type:        "issue.closed",
        Actor:       actor,
        Payload:     string(payloadBytes),
    })
    if err != nil {
        return Issue{}, nil, false, err
    }
    if err := tx.Commit(); err != nil {
        return Issue{}, nil, false, err
    }
    updated, err := d.IssueByID(ctx, issueID)
    if err != nil {
        return Issue{}, nil, false, err
    }
    return updated, &evt, true, nil
}
```

Add the import for `encoding/json` and `github.com/wesm/kata/internal/api` to `internal/db/queries.go` if not already present.

- [ ] **Step 4: Update the daemon caller**

In `internal/daemon/handlers_actions.go:33`:

```go
updated, evt, changed, err = cfg.DB.CloseIssue(ctx, issue.ID,
    in.Body.Reason, in.Body.Actor, in.Body.Message, in.Body.Evidence)
```

- [ ] **Step 5: Update any other callers in the tree**

```
grep -rn "CloseIssue(" /Users/wesm/code/kata --include="*.go" | grep -v "_test.go"
```

Any non-test caller that passes the old 4-arg form gets updated to pass `""` for message and `nil` for evidence (or real values where appropriate). The JSONL cutover path may invoke this — check `internal/jsonl/` and adjust.

- [ ] **Step 6: Update existing tests that call `CloseIssue`**

The existing `TestCloseReopen_RoundTrip` and the new Task 1 tests need the new 6-arg signature. Pass `""` for message and `nil` for evidence in tests that don't care about those fields.

- [ ] **Step 7: Run all DB and daemon tests**

```
go test ./internal/db ./internal/daemon ./internal/jsonl -count=1
```

Expected: PASS, including the new persistence test.

- [ ] **Step 8: Commit**

```bash
git add internal/db/queries.go internal/db/queries_issues_test.go internal/daemon/handlers_actions.go internal/jsonl/
git commit -m "db: CloseIssue persists message + evidence on event payload"
```

---

## Task 5: Daemon — message substance check + per-reason evidence matrix

**Files:**
- Create: `internal/daemon/close_validation.go`
- Create: `internal/daemon/close_validation_test.go`
- Modify: `internal/daemon/handlers_actions.go` (wire validation before DB call)

- [ ] **Step 1: Write failing tests for validation**

Create `internal/daemon/close_validation_test.go`:

```go
package daemon

import (
    "testing"

    "github.com/stretchr/testify/assert"

    "github.com/wesm/kata/internal/api"
)

func TestValidateCloseInput_DoneRequiresImplementationEvidence(t *testing.T) {
    err := ValidateCloseInput("done",
        "Fixed the bug and ran tests on Safari.", nil)
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "evidence required")
}

func TestValidateCloseInput_DoneAcceptsCommit(t *testing.T) {
    err := ValidateCloseInput("done",
        "Fixed the bug and ran tests on Safari.",
        []api.Evidence{{Type: api.EvidenceCommit, SHA: "abc1234"}})
    assert.NoError(t, err)
}

func TestValidateCloseInput_DoneRejectsDuplicateOfAlongside(t *testing.T) {
    err := ValidateCloseInput("done",
        "Fixed the bug and ran tests on Safari.",
        []api.Evidence{
            {Type: api.EvidenceCommit, SHA: "abc1234"},
            {Type: api.EvidenceDuplicateOf, Issue: 7},
        })
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "duplicate-of")
}

func TestValidateCloseInput_WontfixZeroEvidence(t *testing.T) {
    err := ValidateCloseInput("wontfix",
        "Decided not to fix this; doesn't match product direction.",
        nil)
    assert.NoError(t, err)
}

func TestValidateCloseInput_WontfixRejectsEvidence(t *testing.T) {
    err := ValidateCloseInput("wontfix",
        "Decided not to fix this; doesn't match product direction.",
        []api.Evidence{{Type: api.EvidenceCommit, SHA: "abc1234"}})
    assert.Error(t, err)
}

func TestValidateCloseInput_DuplicateRequiresExactlyOneDuplicateOf(t *testing.T) {
    err := ValidateCloseInput("duplicate", "Same Safari race; merge there.",
        []api.Evidence{{Type: api.EvidenceDuplicateOf, Issue: 7}})
    assert.NoError(t, err)
}

func TestValidateCloseInput_DuplicateRejectsExtraEvidence(t *testing.T) {
    err := ValidateCloseInput("duplicate", "Same Safari race; merge there.",
        []api.Evidence{
            {Type: api.EvidenceDuplicateOf, Issue: 7},
            {Type: api.EvidenceCommit, SHA: "abc1234"},
        })
    assert.Error(t, err)
}

func TestValidateCloseInput_AuditNoChangeAllowsReviewedPaths(t *testing.T) {
    err := ValidateCloseInput("audit-no-change",
        "Reviewed schema, queries, and tests; no code change required.",
        []api.Evidence{
            {Type: api.EvidenceNoChangeAudit, Rationale: "metadata-only"},
            {Type: api.EvidenceReviewedPaths, Paths: []string{"a.go", "b.go"}},
        })
    assert.NoError(t, err)
}

func TestValidateCloseInput_MessageTooShortForDone(t *testing.T) {
    err := ValidateCloseInput("done", "Fixed it",
        []api.Evidence{{Type: api.EvidenceCommit, SHA: "abc1234"}})
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "too short")
}

func TestValidateCloseInput_MessageTrivialDenied(t *testing.T) {
    // 40-char message that normalizes exactly to "done".
    msg := "   done   "
    for len(msg) < 40 {
        msg += " "
    }
    err := ValidateCloseInput("done", msg,
        []api.Evidence{{Type: api.EvidenceCommit, SHA: "abc1234"}})
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "trivial")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/daemon -run TestValidateCloseInput
```

Expected: compile error — `ValidateCloseInput` doesn't exist.

- [ ] **Step 3: Implement validation**

Create `internal/daemon/close_validation.go`:

```go
package daemon

import (
    "fmt"
    "strings"

    "github.com/wesm/kata/internal/api"
)

// messageFloor returns the minimum character count required for a close
// message under the given reason, per spec §3.4.
func messageFloor(reason string) int {
    switch reason {
    case "done", "audit-no-change":
        return 40
    case "wontfix":
        return 60
    case "duplicate", "superseded":
        return 20
    default:
        return 40
    }
}

// trivialMessages is the exact-match deny-list from spec §3.4. Kept short
// on purpose; if it grows materially, move to config.
var trivialMessages = map[string]struct{}{
    "done": {}, "fixed": {}, "complete": {}, "completed": {},
    "ok": {}, "okay": {}, "yes": {}, "no": {},
    "n/a": {}, "na": {}, "skip": {}, "nope": {},
}

// normalizeMessage applies the cheap normalization used by both the
// substance check (§3.4) and the repeated-message guard (§3.10).
func normalizeMessage(s string) string {
    s = strings.TrimSpace(s)
    s = strings.Join(strings.Fields(s), " ")
    s = strings.ToLower(s)
    s = strings.TrimRight(s, ".?!")
    return s
}

// ValidateCloseInput enforces the substance check on message and the
// per-reason evidence matrix from spec §3.5. Returns a descriptive error
// on the first violation; the daemon handler maps it to a 400 response.
func ValidateCloseInput(reason, message string, evidence []api.Evidence) error {
    norm := normalizeMessage(message)
    if len(norm) < messageFloor(reason) {
        return fmt.Errorf("message too short for reason=%s (need >=%d chars after normalization, got %d)",
            reason, messageFloor(reason), len(norm))
    }
    if _, isTrivial := trivialMessages[norm]; isTrivial {
        return fmt.Errorf("message rejected as trivial (%q)", norm)
    }

    counts := map[api.EvidenceType]int{}
    for _, e := range evidence {
        counts[e.Type]++
    }
    has := func(t api.EvidenceType) bool { return counts[t] > 0 }
    onlyAllow := func(allowed ...api.EvidenceType) error {
        permit := map[api.EvidenceType]struct{}{}
        for _, a := range allowed {
            permit[a] = struct{}{}
        }
        for t := range counts {
            if _, ok := permit[t]; !ok {
                return fmt.Errorf("evidence type %q not allowed for reason=%s", t, reason)
            }
        }
        return nil
    }

    switch reason {
    case "done":
        if !has(api.EvidenceCommit) && !has(api.EvidencePR) &&
            !has(api.EvidenceTest) && !has(api.EvidenceReviewedPaths) {
            return fmt.Errorf("evidence required for reason=done (commit, pr, test, or reviewed-paths)")
        }
        if err := onlyAllow(api.EvidenceCommit, api.EvidencePR,
            api.EvidenceTest, api.EvidenceReviewedPaths); err != nil {
            return err
        }
    case "wontfix":
        if len(evidence) > 0 {
            return fmt.Errorf("evidence not allowed for reason=wontfix")
        }
    case "duplicate":
        if counts[api.EvidenceDuplicateOf] != 1 {
            return fmt.Errorf("reason=duplicate requires exactly one duplicate-of evidence item")
        }
        if err := onlyAllow(api.EvidenceDuplicateOf); err != nil {
            return err
        }
    case "superseded":
        if counts[api.EvidenceSupersededBy] != 1 {
            return fmt.Errorf("reason=superseded requires exactly one superseded-by evidence item")
        }
        if err := onlyAllow(api.EvidenceSupersededBy); err != nil {
            return err
        }
    case "audit-no-change":
        if counts[api.EvidenceNoChangeAudit] != 1 {
            return fmt.Errorf("reason=audit-no-change requires exactly one no-change-audit evidence item")
        }
        if err := onlyAllow(api.EvidenceNoChangeAudit, api.EvidenceReviewedPaths); err != nil {
            return err
        }
    default:
        return fmt.Errorf("unknown reason %q", reason)
    }
    return nil
}
```

- [ ] **Step 4: Wire validation into the close handler**

In `internal/daemon/handlers_actions.go`, inside the `closeIssue` operation handler, after the `validateActor` call but before the DB call:

```go
if err := ValidateCloseInput(in.Body.Reason, in.Body.Message, in.Body.Evidence); err != nil {
    return nil, api.NewError(400, "validation", err.Error(), "", nil)
}
```

- [ ] **Step 5: Run validation tests**

```
go test ./internal/daemon -run TestValidateCloseInput -v
```

Expected: PASS on all cases.

- [ ] **Step 6: Run the full daemon test suite**

```
go test ./internal/daemon -count=1
```

Expected: PASS. Existing handler tests that call close will fail validation unless they provide a substantive message and (for done) evidence. Update them to pass valid input or to assert the new validation behavior.

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/close_validation.go internal/daemon/close_validation_test.go internal/daemon/handlers_actions.go
git commit -m "daemon: validate close message and per-reason evidence matrix"
```

---

## Task 6: CLI — canonical close flags

**Files:**
- Modify: `cmd/kata/close.go`
- Test: `cmd/kata/close_reopen_test.go`

- [ ] **Step 1: Write failing test for canonical flags**

Add to `cmd/kata/close_reopen_test.go`:

```go
func TestCloseCmd_CanonicalDoneRequiresEvidence(t *testing.T) {
    env, dir, _ := setupWorkspaceWithIssue(t, "test issue")

    _, stderr, err := runCLIWithErr(t, env, dir,
        "close", "1",
        "--reason", "done",
        "--message", "Fixed Safari callback double-submit and ran tests.")
    require.Error(t, err)
    assert.Contains(t, stderr, "evidence required")
}

func TestCloseCmd_CanonicalDoneWithCommitEvidence(t *testing.T) {
    env, dir, _ := setupWorkspaceWithIssue(t, "test issue")

    out := runCLI(t, env, dir,
        "close", "1",
        "--reason", "done",
        "--message", "Fixed Safari callback double-submit and ran tests.",
        "--evidence", "commit:abc1234")
    assert.Contains(t, out, "closed")
}
```

If `runCLIWithErr` doesn't exist, add it next to `runCLI` in `cmd/kata/helpers_test.go` — same signature but returns stdout, stderr, error instead of just stdout.

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./cmd/kata -run TestCloseCmd_Canonical -v
```

Expected: FAIL — `--message` and `--evidence` flags don't exist; current close accepts only `--reason`.

- [ ] **Step 3: Implement canonical flag set**

Replace `cmd/kata/close.go`:

```go
package main

import (
    "fmt"
    "net/http"
    "strings"

    "github.com/spf13/cobra"

    "github.com/wesm/kata/internal/api"
)

func newCloseCmd() *cobra.Command {
    var (
        reason   string
        message  string
        evidence []string
        dryRun   bool
    )
    cmd := &cobra.Command{
        Use:   "close <issue-ref>",
        Short: "close an issue (asserts the work is complete)",
        Long: `Closing an issue asserts that the work it describes is complete.
This is a stronger claim than a comment. Provide evidence and a
substantive message.

If you have not completed and tested this work, do not close it.
Instead, label and comment:
    kata edit <ref> --label needs-review
    kata comment <ref> --body "what was attempted, what remains"`,
        Args: cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            if reason == "" {
                return fmt.Errorf("--reason is required (one of: done, wontfix, duplicate, superseded, audit-no-change)")
            }
            parsed, err := parseEvidenceFlags(evidence)
            if err != nil {
                return err
            }
            extra := map[string]any{
                "reason":   reason,
                "message":  message,
                "evidence": parsed,
                "dry_run":  dryRun,
            }
            return runAction(cmd, args[0], "close", extra)
        },
    }
    cmd.Flags().StringVar(&reason, "reason", "",
        "one of: done, wontfix, duplicate, superseded, audit-no-change")
    cmd.Flags().StringVar(&message, "message", "",
        "substantive message describing scope and verification")
    cmd.Flags().StringSliceVar(&evidence, "evidence", nil,
        "typed evidence, repeatable: commit:<sha>, pr:<url>, test:<cmd>, "+
            "reviewed-paths:<path>, no-change-audit:<text>, duplicate-of:<N>, superseded-by:<N>")
    cmd.Flags().BoolVar(&dryRun, "dry-run", false,
        "validate without mutating; reports the would-be close event")
    return cmd
}

// parseEvidenceFlags turns CLI strings like "commit:abc1234" into the wire
// shape expected by the daemon. reviewed-paths repeats are merged into a
// single evidence item with a paths array, per spec §3.3.
func parseEvidenceFlags(raw []string) ([]api.Evidence, error) {
    var out []api.Evidence
    var reviewedPaths []string
    for _, s := range raw {
        colon := strings.Index(s, ":")
        if colon < 0 {
            return nil, fmt.Errorf("evidence %q: expected <type>:<value>", s)
        }
        kind, value := api.EvidenceType(s[:colon]), s[colon+1:]
        switch kind {
        case api.EvidenceCommit:
            out = append(out, api.Evidence{Type: kind, SHA: value})
        case api.EvidencePR:
            out = append(out, api.Evidence{Type: kind, URL: value})
        case api.EvidenceTest:
            out = append(out, api.Evidence{Type: kind, Command: value})
        case api.EvidenceReviewedPaths:
            reviewedPaths = append(reviewedPaths, value)
        case api.EvidenceNoChangeAudit:
            out = append(out, api.Evidence{Type: kind, Rationale: value})
        case api.EvidenceDuplicateOf:
            var n int64
            if _, err := fmt.Sscan(value, &n); err != nil || n <= 0 {
                return nil, fmt.Errorf("evidence duplicate-of: expected positive issue number, got %q", value)
            }
            out = append(out, api.Evidence{Type: kind, Issue: n})
        case api.EvidenceSupersededBy:
            var n int64
            if _, err := fmt.Sscan(value, &n); err != nil || n <= 0 {
                return nil, fmt.Errorf("evidence superseded-by: expected positive issue number, got %q", value)
            }
            out = append(out, api.Evidence{Type: kind, Issue: n})
        default:
            return nil, fmt.Errorf("evidence %q: unknown type %q", s, kind)
        }
    }
    if len(reviewedPaths) > 0 {
        out = append(out, api.Evidence{Type: api.EvidenceReviewedPaths, Paths: reviewedPaths})
    }
    return out, nil
}

// runAction is shared by close and reopen.
func runAction(cmd *cobra.Command, raw, action string, extra map[string]any) error {
    ctx, baseURL, pid, issue, err := resolveIssueRefForCommand(cmd, raw)
    if err != nil {
        return err
    }
    actor, _ := resolveActor(flags.As, nil)
    body := map[string]any{"actor": actor}
    for k, v := range extra {
        body[k] = v
    }
    client, err := httpClientFor(ctx, baseURL)
    if err != nil {
        return err
    }
    status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
        fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/actions/%s", baseURL, pid, issue.Number, action),
        body)
    if err != nil {
        return err
    }
    if status >= 400 {
        return apiErrFromBody(status, bs)
    }
    return printMutation(cmd, bs)
}
```

- [ ] **Step 4: Update existing close tests**

`TestCloseReopen_RoundTrip` uses `--reason wontfix` without `--message`. Update it:

```go
out := runCLI(t, env, dir, "close", "1",
    "--reason", "wontfix",
    "--message", "Decided not to fix this; doesn't match product direction.")
```

Sweep for other call sites in `cmd/kata/*_test.go` that hit `kata close` and patch them.

- [ ] **Step 5: Run all CLI close tests**

```
go test ./cmd/kata -run TestClose -v
```

Expected: PASS on existing and new.

- [ ] **Step 6: Commit**

```bash
git add cmd/kata/close.go cmd/kata/close_reopen_test.go cmd/kata/helpers_test.go
git commit -m "cli: canonical --reason / --message / --evidence on kata close"
```

---

## Task 7: CLI — sugar flags

**Files:**
- Modify: `cmd/kata/close.go`
- Test: `cmd/kata/close_reopen_test.go`

- [ ] **Step 1: Write failing tests for sugar**

Append to `cmd/kata/close_reopen_test.go`:

```go
func TestCloseCmd_SugarDoneWithCommit(t *testing.T) {
    env, dir, _ := setupWorkspaceWithIssue(t, "test issue")
    out := runCLI(t, env, dir,
        "close", "1",
        "--done",
        "--message", "Fixed Safari callback double-submit and ran tests.",
        "--commit", "abc1234")
    assert.Contains(t, out, "closed")
}

func TestCloseCmd_SugarDuplicateOf(t *testing.T) {
    env, dir, _ := setupWorkspaceWithIssue(t, "test issue")
    // Create a target issue.
    runCLI(t, env, dir, "create", "target")
    out := runCLI(t, env, dir,
        "close", "1",
        "--duplicate-of", "2",
        "--message", "Same Safari race; merge there.")
    assert.Contains(t, out, "closed")
}

func TestCloseCmd_SugarConflictsWithCanonical(t *testing.T) {
    env, dir, _ := setupWorkspaceWithIssue(t, "test issue")
    _, stderr, err := runCLIWithErr(t, env, dir,
        "close", "1",
        "--reason", "done", "--done",
        "--message", "Fixed it.",
        "--evidence", "commit:abc1234")
    require.Error(t, err)
    assert.Contains(t, stderr, "conflict")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./cmd/kata -run TestCloseCmd_Sugar -v
```

Expected: FAIL — sugar flags don't exist.

- [ ] **Step 3: Add sugar flags to `cmd/kata/close.go`**

Inside `newCloseCmd`, after registering canonical flags:

```go
var (
    sugarDone           bool
    sugarWontfix        bool
    sugarAuditNoChange  bool
    sugarDuplicateOf    int64
    sugarSupersededBy   int64
    sugarCommit         string
    sugarPR             string
    sugarTest           string
    sugarReviewed       []string
)
cmd.Flags().BoolVar(&sugarDone, "done", false, "sugar for --reason done")
cmd.Flags().BoolVar(&sugarWontfix, "wontfix", false, "sugar for --reason wontfix")
cmd.Flags().BoolVar(&sugarAuditNoChange, "audit-no-change", false, "sugar for --reason audit-no-change")
cmd.Flags().Int64Var(&sugarDuplicateOf, "duplicate-of", 0, "sugar for --reason duplicate --evidence duplicate-of:<N>")
cmd.Flags().Int64Var(&sugarSupersededBy, "superseded-by", 0, "sugar for --reason superseded --evidence superseded-by:<N>")
cmd.Flags().StringVar(&sugarCommit, "commit", "", "sugar for --evidence commit:<sha>")
cmd.Flags().StringVar(&sugarPR, "pr", "", "sugar for --evidence pr:<url>")
cmd.Flags().StringVar(&sugarTest, "test", "", "sugar for --evidence test:<command>")
cmd.Flags().StringSliceVar(&sugarReviewed, "reviewed", nil, "sugar for --evidence reviewed-paths:<path>, repeatable")
```

Rewrite the `RunE` to merge sugar into canonical, rejecting conflicts:

```go
RunE: func(cmd *cobra.Command, args []string) error {
    // Resolve sugar -> reason (with conflict checks).
    sugarReason := ""
    switch {
    case sugarDone:
        sugarReason = "done"
    case sugarWontfix:
        sugarReason = "wontfix"
    case sugarAuditNoChange:
        sugarReason = "audit-no-change"
    case sugarDuplicateOf > 0:
        sugarReason = "duplicate"
    case sugarSupersededBy > 0:
        sugarReason = "superseded"
    }
    if sugarReason != "" && reason != "" && sugarReason != reason {
        return fmt.Errorf("flag conflict: --reason=%s and sugar resolves to %s", reason, sugarReason)
    }
    if sugarReason != "" && reason != "" && sugarReason == reason {
        return fmt.Errorf("flag conflict: --reason and corresponding sugar flag both set")
    }
    if sugarReason != "" {
        reason = sugarReason
    }
    if reason == "" {
        return fmt.Errorf("--reason is required (one of: done, wontfix, duplicate, superseded, audit-no-change)")
    }

    // Resolve sugar -> evidence (appended to user-supplied --evidence values).
    if sugarCommit != "" {
        evidence = append(evidence, "commit:"+sugarCommit)
    }
    if sugarPR != "" {
        evidence = append(evidence, "pr:"+sugarPR)
    }
    if sugarTest != "" {
        evidence = append(evidence, "test:"+sugarTest)
    }
    for _, p := range sugarReviewed {
        evidence = append(evidence, "reviewed-paths:"+p)
    }
    if sugarDuplicateOf > 0 {
        evidence = append(evidence, fmt.Sprintf("duplicate-of:%d", sugarDuplicateOf))
    }
    if sugarSupersededBy > 0 {
        evidence = append(evidence, fmt.Sprintf("superseded-by:%d", sugarSupersededBy))
    }

    parsed, err := parseEvidenceFlags(evidence)
    if err != nil {
        return err
    }
    if dup := findDuplicateEvidence(parsed); dup != "" {
        return fmt.Errorf("flag conflict: duplicate evidence item %s (provided via both canonical and sugar)", dup)
    }

    extra := map[string]any{
        "reason":   reason,
        "message":  message,
        "evidence": parsed,
        "dry_run":  dryRun,
    }
    return runAction(cmd, args[0], "close", extra)
},
```

Add helper:

```go
// findDuplicateEvidence returns the first duplicate "type:value" pair, or
// "" if none. Detects user-error like `--duplicate-of 7 --evidence duplicate-of:7`.
func findDuplicateEvidence(items []api.Evidence) string {
    seen := map[string]struct{}{}
    for _, e := range items {
        key := fmt.Sprintf("%s:%v", e.Type, evidencePayloadKey(e))
        if _, dup := seen[key]; dup {
            return key
        }
        seen[key] = struct{}{}
    }
    return ""
}

func evidencePayloadKey(e api.Evidence) string {
    switch e.Type {
    case api.EvidenceCommit:
        return e.SHA
    case api.EvidencePR:
        return e.URL
    case api.EvidenceTest:
        return e.Command
    case api.EvidenceNoChangeAudit:
        return e.Rationale
    case api.EvidenceDuplicateOf, api.EvidenceSupersededBy:
        return fmt.Sprintf("%d", e.Issue)
    case api.EvidenceReviewedPaths:
        return strings.Join(e.Paths, ",")
    }
    return ""
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./cmd/kata -run TestCloseCmd_Sugar -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/kata/close.go cmd/kata/close_reopen_test.go
git commit -m "cli: sugar flags for kata close (--done, --duplicate-of, --commit, ...)"
```

---

## Task 8: CLI — `--dry-run` on close

**Files:**
- Modify: `internal/daemon/handlers_actions.go` (handle DryRun in the close handler)
- Test: `cmd/kata/close_reopen_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestCloseCmd_DryRunDoesNotMutate(t *testing.T) {
    env, dir, _ := setupWorkspaceWithIssue(t, "test issue")
    out := runCLI(t, env, dir,
        "close", "1",
        "--done",
        "--message", "Fixed Safari callback double-submit and ran tests.",
        "--commit", "abc1234",
        "--dry-run")
    assert.Contains(t, out, "dry-run")

    show := runCLI(t, env, dir, "show", "1", "--json")
    assert.Contains(t, show, `"status":"open"`)
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./cmd/kata -run TestCloseCmd_DryRunDoesNotMutate -v
```

Expected: FAIL — `--dry-run` reaches the daemon but the daemon mutates anyway.

- [ ] **Step 3: Handle DryRun in the daemon close handler**

In `internal/daemon/handlers_actions.go`, inside the close handler, after validation:

```go
if in.Body.DryRun {
    out := &api.MutationResponse{}
    out.Body.Issue = issue
    out.Body.Event = nil
    out.Body.Changed = false
    // The audit value the CLI reports comes from the response shape; the
    // CLI surface adds a "dry-run" label client-side.
    return out, nil
}
```

This step is positioned *after* all validation and *before* the DB call. Throttle/parent-completeness checks (added in later tasks) should also run before this short-circuit so dry-run reports them. Add an explicit comment to that effect — Tasks 9-11 will reuse the position.

- [ ] **Step 4: Surface "dry-run" in the CLI output**

In `cmd/kata/close.go`, inside the cobra `RunE` after `runAction`, the response is the standard MutationResponse. Update `printMutation` only if you need new copy. Simpler approach: when `dryRun=true`, the CLI prints `"close: dry-run (would close issue N)"` itself before calling `runAction`, and after `runAction` prints "no changes". Concretely, wrap the call:

```go
if dryRun {
    fmt.Fprintf(cmd.OutOrStdout(), "close: dry-run (no mutations will occur)\n")
}
if err := runAction(cmd, args[0], "close", extra); err != nil {
    return err
}
return nil
```

- [ ] **Step 5: Run tests**

```
go test ./cmd/kata -run TestCloseCmd_DryRun -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/kata/close.go internal/daemon/handlers_actions.go cmd/kata/close_reopen_test.go
git commit -m "cli: --dry-run on kata close (validation without mutation)"
```

---

## Task 9: CLI — help banner and error text

**Files:**
- Modify: `cmd/kata/close.go` (`Long` description; error wrapping)
- Modify: `internal/daemon/handlers_actions.go` (richer error responses)
- Test: `cmd/kata/close_reopen_test.go`

- [ ] **Step 1: Write failing test for the help banner**

```go
func TestCloseCmd_HelpBannerNamesObligation(t *testing.T) {
    out := runCLI(t, nil, "", "close", "--help")
    assert.Contains(t, out, "asserts that the work it describes is complete")
    assert.Contains(t, out, "do not close it")
    assert.Contains(t, out, "needs-review")
}

func TestCloseCmd_ErrorTextNamesAlternative(t *testing.T) {
    env, dir, _ := setupWorkspaceWithIssue(t, "test issue")
    _, stderr, err := runCLIWithErr(t, env, dir,
        "close", "1", "--done",
        "--message", "Fixed Safari callback double-submit and ran tests.")
    require.Error(t, err)
    assert.Contains(t, stderr, "evidence required")
    assert.Contains(t, stderr, "needs-review")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./cmd/kata -run "TestCloseCmd_(HelpBanner|ErrorText)" -v
```

Expected: FAIL — the help text and error text don't include the alternative-path guidance yet.

- [ ] **Step 3: Update the `Long` description**

The `Long` from Task 6 already includes "needs-review" guidance. Verify it matches:

```go
Long: `Closing an issue asserts that the work it describes is complete.
This is a stronger claim than a comment. Provide evidence and a
substantive message.

If you have not completed and tested this work, do not close it.
Instead, label and comment:
    kata edit <ref> --label needs-review
    kata comment <ref> --body "what was attempted, what remains"`,
```

- [ ] **Step 4: Enrich daemon error messages**

In `internal/daemon/close_validation.go`, change the "evidence required" return for `done` to:

```go
return fmt.Errorf("evidence required for reason=done. " +
    "Accepted: commit:<sha>, pr:<url>, test:<cmd>, reviewed-paths:<path>. " +
    "If the work is not actually complete, do not close — use " +
    "`kata edit <ref> --label needs-review` and comment what remains")
```

Similarly enrich the trivial-message error to point to needs-review.

- [ ] **Step 5: Run tests to verify they pass**

```
go test ./cmd/kata -run "TestCloseCmd_(HelpBanner|ErrorText)" -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/kata/close.go internal/daemon/close_validation.go cmd/kata/close_reopen_test.go
git commit -m "cli+daemon: close help banner and error text name the alternative path"
```

---

## Task 10: Daemon — parent-close completeness check

**Files:**
- Create: `internal/daemon/close_guards.go`
- Create: `internal/daemon/close_guards_test.go`
- Modify: `internal/daemon/handlers_actions.go` (wire the check)
- Modify: `internal/db/queries_links.go` (add `OpenChildrenOf` helper if absent)

- [ ] **Step 1: Write failing test**

Create `internal/daemon/close_guards_test.go`:

```go
package daemon

import (
    "context"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestParentCloseCompleteness_RefusesWhenOpenChildrenExist(t *testing.T) {
    srv, cleanup := startTestServer(t)
    defer cleanup()
    ctx := context.Background()

    proj := createTestProject(t, srv, "p")
    parent := createTestIssue(t, srv, proj, "parent")
    child := createTestIssue(t, srv, proj, "child")
    linkParent(t, srv, child, parent)

    err := closeViaAPI(ctx, srv, proj, parent,
        "done",
        "Reviewed parent scope and all children done.",
        []map[string]any{{"type": "commit", "sha": "abc1234"}})
    require.Error(t, err)
    assert.Contains(t, err.Error(), "open children")
    assert.Contains(t, err.Error(), child.Title)
}

func TestParentCloseCompleteness_AllowsWhenChildrenClosed(t *testing.T) {
    srv, cleanup := startTestServer(t)
    defer cleanup()
    ctx := context.Background()

    proj := createTestProject(t, srv, "p")
    parent := createTestIssue(t, srv, proj, "parent")
    child := createTestIssue(t, srv, proj, "child")
    linkParent(t, srv, child, parent)

    // Close the child first with done + evidence.
    require.NoError(t, closeViaAPI(ctx, srv, proj, child,
        "done",
        "Implemented schema review.",
        []map[string]any{{"type": "commit", "sha": "def5678"}}))

    require.NoError(t, closeViaAPI(ctx, srv, proj, parent,
        "done",
        "All children completed.",
        []map[string]any{{"type": "reviewed-paths", "paths": []string{"a.go"}}}))
}
```

You will need `closeViaAPI`, `startTestServer`, `createTestProject`, `createTestIssue`, `linkParent` test helpers. If similar helpers exist in `internal/daemon/handlers_instance_test.go` or the existing test setup, reuse and extend. Otherwise add them to a new `internal/daemon/testhelpers_test.go`.

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/daemon -run TestParentCloseCompleteness -v
```

Expected: FAIL — no guard exists; parent close succeeds with children open.

- [ ] **Step 3: Add `OpenChildrenOf` helper to the DB**

In `internal/db/queries_links.go`:

```go
// OpenChildrenOf returns up to limit non-deleted, non-closed children of
// parentIssueID, plus the total open-children count. Used by the parent-
// close completeness check; returns truncated children for the error
// message and the full count for the "(N more)" suffix.
func (d *DB) OpenChildrenOf(
    ctx context.Context, projectID, parentIssueID int64, limit int,
) ([]Issue, int, error) {
    var total int
    if err := d.QueryRowContext(ctx,
        `SELECT COUNT(*)
         FROM links l
         JOIN issues child ON child.id = l.from_issue_id
         WHERE l.project_id = ?
           AND child.project_id = ?
           AND l.type = 'parent'
           AND l.to_issue_id = ?
           AND child.status = 'open'
           AND child.deleted_at IS NULL`,
        projectID, projectID, parentIssueID).Scan(&total); err != nil {
        return nil, 0, fmt.Errorf("open children count: %w", err)
    }
    if total == 0 {
        return nil, 0, nil
    }
    rows, err := d.QueryContext(ctx, issueSelect+`
        JOIN links l ON l.from_issue_id = i.id
        WHERE l.project_id = ?
          AND i.project_id = ?
          AND l.type = 'parent'
          AND l.to_issue_id = ?
          AND i.status = 'open'
          AND i.deleted_at IS NULL
        ORDER BY i.number ASC
        LIMIT ?`,
        projectID, projectID, parentIssueID, limit)
    if err != nil {
        return nil, 0, fmt.Errorf("open children: %w", err)
    }
    defer func() { _ = rows.Close() }()
    var out []Issue
    for rows.Next() {
        issue, err := scanIssue(rows)
        if err != nil {
            return nil, 0, err
        }
        out = append(out, issue)
    }
    return out, total, rows.Err()
}
```

Confirm `issueSelect` is exported within the package (lowercase const, OK).

- [ ] **Step 4: Implement the guard**

Create `internal/daemon/close_guards.go`:

```go
package daemon

import (
    "context"
    "fmt"
    "strings"

    "github.com/wesm/kata/internal/db"
)

const openChildrenSampleLimit = 10

// CheckParentCloseCompleteness refuses a close on an issue with open
// children. Implements spec §3.8.
func CheckParentCloseCompleteness(
    ctx context.Context, d *db.DB, projectID, issueID int64,
) error {
    children, total, err := d.OpenChildrenOf(ctx, projectID, issueID, openChildrenSampleLimit)
    if err != nil {
        return err
    }
    if total == 0 {
        return nil
    }
    var lines []string
    for _, c := range children {
        lines = append(lines, fmt.Sprintf("  #%d  %s", c.Number, c.Title))
    }
    suffix := ""
    if total > openChildrenSampleLimit {
        suffix = fmt.Sprintf("\n  ... (%d more, see `kata show %d --json`)", total-openChildrenSampleLimit, issueID)
    }
    return fmt.Errorf("refusing — issue has %d open children:\n%s%s\nClose children first, or scope this issue differently",
        total, strings.Join(lines, "\n"), suffix)
}
```

- [ ] **Step 5: Wire into the close handler**

In `internal/daemon/handlers_actions.go`, after validation but before `DryRun` short-circuit:

```go
if err := CheckParentCloseCompleteness(ctx, cfg.DB, in.ProjectID, issue.ID); err != nil {
    return nil, api.NewError(409, "parent_has_open_children", err.Error(), "", nil)
}
```

- [ ] **Step 6: Run tests**

```
go test ./internal/daemon -run TestParentCloseCompleteness -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/db/queries_links.go internal/daemon/close_guards.go internal/daemon/close_guards_test.go internal/daemon/handlers_actions.go
git commit -m "daemon: refuse parent-close while open children remain"
```

---

## Task 11: Daemon — sibling-close throttle

**Files:**
- Modify: `internal/daemon/close_guards.go` (add `CheckSiblingCloseThrottle`)
- Modify: `internal/daemon/close_guards_test.go`
- Modify: `internal/db/queries_events.go` (add `RecentSiblingCloses` helper)
- Modify: `internal/daemon/handlers_actions.go` (wire the guard)

- [ ] **Step 1: Write failing test**

Append to `internal/daemon/close_guards_test.go`:

```go
func TestSiblingThrottle_FourthCloseUnderSameParentRefused(t *testing.T) {
    srv, cleanup := startTestServer(t)
    defer cleanup()
    ctx := context.Background()

    proj := createTestProject(t, srv, "p")
    parent := createTestIssue(t, srv, proj, "parent")
    children := make([]Issue, 0, 4)
    for i := 0; i < 4; i++ {
        c := createTestIssue(t, srv, proj, fmt.Sprintf("child %d", i+1))
        linkParent(t, srv, c, parent)
        children = append(children, c)
    }

    // First three siblings close successfully.
    for _, c := range children[:3] {
        require.NoError(t, closeViaAPI(ctx, srv, proj, c,
            "done",
            fmt.Sprintf("Implementation of %s complete and tested.", c.Title),
            []map[string]any{{"type": "commit", "sha": "abc1234"}}),
            "close %s", c.Title)
    }

    // Fourth is refused with sibling-burst.
    err := closeViaAPI(ctx, srv, proj, children[3],
        "done",
        fmt.Sprintf("Implementation of %s complete and tested.", children[3].Title),
        []map[string]any{{"type": "commit", "sha": "abc1234"}})
    require.Error(t, err)
    assert.Contains(t, err.Error(), "sibling-close throttle")
    assert.Contains(t, err.Error(), parent.Title)
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/daemon -run TestSiblingThrottle -v
```

Expected: FAIL — no throttle.

- [ ] **Step 3: Add `RecentSiblingCloses` helper**

In `internal/db/queries_events.go`:

```go
// RecentSiblingCloses returns issue.closed events in the given project by
// the given actor on direct children of parentIssueID within the time
// window. Used by the sibling-close throttle (§3.9).
func (d *DB) RecentSiblingCloses(
    ctx context.Context,
    projectID, parentIssueID int64,
    actor string,
    since time.Time,
) ([]Event, error) {
    const q = `
        SELECT e.id, e.project_id, e.project_name, e.issue_id, e.issue_number,
               e.type, e.actor, e.payload, e.created_at
        FROM events e
        JOIN links l ON l.from_issue_id = e.issue_id
        WHERE e.project_id = ?
          AND e.type = 'issue.closed'
          AND e.actor = ?
          AND e.created_at >= ?
          AND l.type = 'parent'
          AND l.to_issue_id = ?
          AND l.project_id = ?
        ORDER BY e.created_at DESC`
    rows, err := d.QueryContext(ctx, q,
        projectID, actor, since.UTC().Format("2006-01-02T15:04:05.000Z"),
        parentIssueID, projectID)
    if err != nil {
        return nil, fmt.Errorf("recent sibling closes: %w", err)
    }
    defer func() { _ = rows.Close() }()
    var out []Event
    for rows.Next() {
        ev, err := scanEvent(rows)
        if err != nil {
            return nil, err
        }
        out = append(out, ev)
    }
    return out, rows.Err()
}
```

Ensure `scanEvent` exists in the package; otherwise mirror the row-scan pattern used in `EventsAfter`.

- [ ] **Step 4: Implement `CheckSiblingCloseThrottle`**

In `internal/daemon/close_guards.go`:

```go
import (
    "time"
)

const (
    siblingThrottleWindow = 5 * time.Minute
    siblingThrottleLimit  = 3
)

// CheckSiblingCloseThrottle implements spec §3.9. Returns nil when the
// close is allowed, or a descriptive error when refused. The error
// payload is also used to emit a close.throttled event (Task 13).
func CheckSiblingCloseThrottle(
    ctx context.Context,
    d *db.DB,
    projectID, issueID int64,
    actor string,
    now time.Time,
) (refusal error, parentNumber int64, cohort []int64) {
    parentLink, err := d.ParentOf(ctx, issueID)
    if err != nil {
        // No parent => throttle does not apply.
        return nil, 0, nil
    }
    since := now.Add(-siblingThrottleWindow)
    siblings, err := d.RecentSiblingCloses(ctx, projectID, parentLink.ToIssueID, actor, since)
    if err != nil {
        return nil, 0, nil // soft-fail; do not block close on a broken lookup
    }
    if len(siblings) < siblingThrottleLimit {
        return nil, 0, nil
    }
    var ids []int64
    var lines []string
    for _, ev := range siblings {
        if ev.IssueNumber != nil {
            ids = append(ids, *ev.IssueNumber)
            lines = append(lines, fmt.Sprintf("  #%d closed %s ago",
                *ev.IssueNumber, humanizeDuration(now.Sub(ev.CreatedAt))))
        }
    }
    // Link rows store issue IDs, not issue numbers. Resolve the parent's
    // number via IssueByID for the user-facing error message.
    parentIssue, err := d.IssueByID(ctx, parentLink.ToIssueID)
    if err != nil {
        return nil, 0, nil
    }
    parentNumber = parentIssue.Number
    return fmt.Errorf("sibling-close throttle: you closed %d children of #%d in the last %s:\n%s\nSlow down and review the scope of each remaining child before closing. Wait for the throttle window to clear, or ask a human reviewer to inspect and close",
        len(siblings), parentNumber, siblingThrottleWindow, strings.Join(lines, "\n")), parentNumber, ids
}

func humanizeDuration(d time.Duration) string {
    if d < time.Minute {
        return fmt.Sprintf("%d sec", int(d.Seconds()))
    }
    return fmt.Sprintf("%d min", int(d.Minutes()))
}
```

`ToIssueNumber` may not exist on the Link struct. Check `internal/db/queries_links.go`'s scan path; if only `ToIssueID` is available, look up the issue number with `d.IssueByID`. Either approach is acceptable — choose the smaller patch.

- [ ] **Step 5: Wire into the handler**

In `internal/daemon/handlers_actions.go`, after the parent-completeness check:

```go
now := time.Now()
if refusal, parentNum, cohort := CheckSiblingCloseThrottle(ctx, cfg.DB, in.ProjectID, issue.ID, in.Body.Actor, now); refusal != nil {
    if err := emitThrottledEvent(ctx, cfg, issue, in.Body.Actor, "sibling-burst", parentNum, cohort, nil); err != nil {
        return nil, api.NewError(500, "internal", err.Error(), "", nil)
    }
    return nil, api.NewError(429, "sibling_throttle", refusal.Error(), "", nil)
}
```

`emitThrottledEvent` is defined in Task 13; for now stub it so the compile passes (`func emitThrottledEvent(...) error { return nil }`).

- [ ] **Step 6: Run tests**

```
go test ./internal/daemon -run TestSiblingThrottle -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/db/queries_events.go internal/daemon/close_guards.go internal/daemon/close_guards_test.go internal/daemon/handlers_actions.go
git commit -m "daemon: sibling-close throttle (3 closes per parent per actor per 5 min)"
```

---

## Task 12: Daemon — repeated-message guard

**Files:**
- Modify: `internal/daemon/close_guards.go`
- Modify: `internal/daemon/close_guards_test.go`
- Modify: `internal/db/queries_events.go`
- Modify: `internal/daemon/handlers_actions.go`

- [ ] **Step 1: Write failing test**

Append to `internal/daemon/close_guards_test.go`:

```go
func TestRepeatedMessageGuard_RefusesIdenticalSiblingMessage(t *testing.T) {
    srv, cleanup := startTestServer(t)
    defer cleanup()
    ctx := context.Background()

    proj := createTestProject(t, srv, "p")
    parent := createTestIssue(t, srv, proj, "parent")
    a := createTestIssue(t, srv, proj, "a")
    b := createTestIssue(t, srv, proj, "b")
    linkParent(t, srv, a, parent)
    linkParent(t, srv, b, parent)

    msg := "Schema review complete; table remains metadata-only."
    require.NoError(t, closeViaAPI(ctx, srv, proj, a, "audit-no-change", msg,
        []map[string]any{{"type": "no-change-audit", "rationale": "metadata"}}))

    err := closeViaAPI(ctx, srv, proj, b, "audit-no-change", msg,
        []map[string]any{{"type": "no-change-audit", "rationale": "metadata"}})
    require.Error(t, err)
    assert.Contains(t, err.Error(), "identical close message")
    assert.Contains(t, err.Error(), fmt.Sprintf("#%d", a.Number))
}

func TestRepeatedMessageGuard_SkipsForWontfix(t *testing.T) {
    srv, cleanup := startTestServer(t)
    defer cleanup()
    ctx := context.Background()

    proj := createTestProject(t, srv, "p")
    parent := createTestIssue(t, srv, proj, "parent")
    a := createTestIssue(t, srv, proj, "a")
    b := createTestIssue(t, srv, proj, "b")
    linkParent(t, srv, a, parent)
    linkParent(t, srv, b, parent)

    msg := "Decided not to fix; out of scope for this milestone."
    require.NoError(t, closeViaAPI(ctx, srv, proj, a, "wontfix", msg, nil))
    require.NoError(t, closeViaAPI(ctx, srv, proj, b, "wontfix", msg, nil))
}

func TestRepeatedMessageGuard_SkipsForUnparentedIssues(t *testing.T) {
    srv, cleanup := startTestServer(t)
    defer cleanup()
    ctx := context.Background()

    proj := createTestProject(t, srv, "p")
    a := createTestIssue(t, srv, proj, "a")
    b := createTestIssue(t, srv, proj, "b")
    // No parent linked.

    msg := "Fixed the issue and verified the auth tests pass cleanly."
    require.NoError(t, closeViaAPI(ctx, srv, proj, a, "done", msg,
        []map[string]any{{"type": "commit", "sha": "abc1234"}}))
    require.NoError(t, closeViaAPI(ctx, srv, proj, b, "done", msg,
        []map[string]any{{"type": "commit", "sha": "def5678"}}))
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/daemon -run TestRepeatedMessageGuard -v
```

Expected: FAIL on the first test (refusal not happening). The other two should pass once the guard is implemented (skip behavior).

- [ ] **Step 3: Add `RecentSameMessageClose` helper**

In `internal/db/queries_events.go`:

```go
// RecentSameMessageClose looks for a prior close by the same actor on a
// sibling under parentIssueID whose normalized message equals
// normalizedMessage, within the window. Returns the matching event or
// nil. Used by the repeated-message guard (§3.10); the daemon side
// normalizes both the incoming message and the stored one.
func (d *DB) RecentSameMessageClose(
    ctx context.Context,
    projectID, parentIssueID int64,
    actor, normalizedMessage string,
    since time.Time,
) (*Event, error) {
    siblings, err := d.RecentSiblingCloses(ctx, projectID, parentIssueID, actor, since)
    if err != nil {
        return nil, err
    }
    for _, ev := range siblings {
        var p struct {
            Reason  string `json:"reason"`
            Message string `json:"message"`
        }
        if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
            continue
        }
        if p.Reason != "done" && p.Reason != "audit-no-change" {
            continue
        }
        // Inline normalize without depending on daemon package.
        norm := normalizeMessageDB(p.Message)
        if norm == normalizedMessage {
            return &ev, nil
        }
    }
    return nil, nil
}

func normalizeMessageDB(s string) string {
    s = strings.TrimSpace(s)
    s = strings.Join(strings.Fields(s), " ")
    s = strings.ToLower(s)
    s = strings.TrimRight(s, ".?!")
    return s
}
```

(The duplication of `normalizeMessageDB` and `normalizeMessage` from Task 5 is deliberate: keeping the DB layer free of daemon-package imports is more important than DRY for this 7-line function.)

- [ ] **Step 4: Implement `CheckRepeatedMessageGuard`**

In `internal/daemon/close_guards.go`:

```go
const repeatedMessageWindow = 30 * time.Minute

// CheckRepeatedMessageGuard implements spec §3.10. Returns nil when the
// close is allowed, or a refusal naming the prior close. Only applies to
// done / audit-no-change closes on parented issues.
func CheckRepeatedMessageGuard(
    ctx context.Context,
    d *db.DB,
    projectID, issueID int64,
    actor, reason, message string,
    now time.Time,
) (refusal error, priorNumber int64) {
    if reason != "done" && reason != "audit-no-change" {
        return nil, 0
    }
    parentLink, err := d.ParentOf(ctx, issueID)
    if err != nil {
        return nil, 0 // no parent => guard does not apply
    }
    norm := normalizeMessage(message)
    since := now.Add(-repeatedMessageWindow)
    prior, err := d.RecentSameMessageClose(ctx, projectID, parentLink.ToIssueID, actor, norm, since)
    if err != nil || prior == nil {
        return nil, 0
    }
    priorNumber = 0
    if prior.IssueNumber != nil {
        priorNumber = *prior.IssueNumber
    }
    return fmt.Errorf("identical close message to your close of #%d at %s. Both issues share a parent; each closure should describe its specific issue. If the same prose truly applies, close as `--duplicate-of` or `--superseded-by` instead",
        priorNumber, prior.CreatedAt.Format("15:04:05")), priorNumber
}
```

- [ ] **Step 5: Wire into the handler**

In `internal/daemon/handlers_actions.go`, after the sibling-throttle check:

```go
if refusal, priorNum := CheckRepeatedMessageGuard(ctx, cfg.DB, in.ProjectID, issue.ID, in.Body.Actor, in.Body.Reason, in.Body.Message, now); refusal != nil {
    parentLink, parentErr := cfg.DB.ParentOf(ctx, issue.ID)
    parentNum := int64(0)
    if parentErr == nil {
        if parentIssue, perr := cfg.DB.IssueByID(ctx, parentLink.ToIssueID); perr == nil {
            parentNum = parentIssue.Number
        }
    }
    if err := emitThrottledEvent(ctx, cfg, issue, in.Body.Actor, "duplicate-message", parentNum, nil, &priorNum); err != nil {
        return nil, api.NewError(500, "internal", err.Error(), "", nil)
    }
    return nil, api.NewError(429, "duplicate_message", refusal.Error(), "", nil)
}
```

- [ ] **Step 6: Run tests**

```
go test ./internal/daemon -run TestRepeatedMessageGuard -v
```

Expected: PASS on all three.

- [ ] **Step 7: Commit**

```bash
git add internal/db/queries_events.go internal/daemon/close_guards.go internal/daemon/close_guards_test.go internal/daemon/handlers_actions.go
git commit -m "daemon: repeated-message guard for sibling done/audit closes"
```

---

## Task 13: Daemon — `close.throttled` event emission

**Files:**
- Modify: `internal/daemon/close_guards.go` (real implementation of `emitThrottledEvent`)
- Modify: `internal/db/queries.go` (helper `InsertCloseThrottledEvent`)
- Test: `internal/daemon/close_guards_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestThrottle_EmitsCloseThrottledEvent(t *testing.T) {
    srv, cleanup := startTestServer(t)
    defer cleanup()
    ctx := context.Background()

    proj := createTestProject(t, srv, "p")
    parent := createTestIssue(t, srv, proj, "parent")
    for i := 0; i < 4; i++ {
        c := createTestIssue(t, srv, proj, fmt.Sprintf("child %d", i+1))
        linkParent(t, srv, c, parent)
        body := []map[string]any{{"type": "commit", "sha": "abc1234"}}
        msg := fmt.Sprintf("Closing child %d after review.", i+1)
        if i < 3 {
            require.NoError(t, closeViaAPI(ctx, srv, proj, c, "done", msg, body))
        } else {
            // Fourth: refused; expect close.throttled event emitted.
            _ = closeViaAPI(ctx, srv, proj, c, "done", msg, body)
        }
    }

    events := listRecentEvents(t, srv, proj)
    var throttled *Event
    for _, ev := range events {
        if ev.Type == "close.throttled" {
            throttled = &ev
            break
        }
    }
    require.NotNil(t, throttled, "expected close.throttled event")
    assert.Contains(t, throttled.Payload, `"reason":"sibling-burst"`)
    assert.Contains(t, throttled.Payload, fmt.Sprintf(`"parent":%d`, parent.Number))
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/daemon -run TestThrottle_EmitsCloseThrottledEvent -v
```

Expected: FAIL — `emitThrottledEvent` is a stub.

- [ ] **Step 3: Implement `InsertCloseThrottledEvent`**

In `internal/db/queries.go` (near the end, with other event helpers):

```go
// CloseThrottledPayload is the structured payload for close.throttled
// events. See spec §3.9 and §3.10.
type CloseThrottledPayload struct {
    Reason        string  `json:"reason"`  // sibling-burst | duplicate-message
    Parent        int64   `json:"parent,omitempty"`
    Cohort        []int64 `json:"cohort,omitempty"`
    PriorIssue    *int64  `json:"prior,omitempty"`
}

// InsertCloseThrottledEvent records a throttle refusal without changing
// the issue state. Used by both throttle paths in §3.9 and §3.10.
func (d *DB) InsertCloseThrottledEvent(
    ctx context.Context,
    projectID int64,
    projectName string,
    issueID int64,
    issueNumber int64,
    actor string,
    payload CloseThrottledPayload,
) (Event, error) {
    bs, err := json.Marshal(payload)
    if err != nil {
        return Event{}, fmt.Errorf("throttled payload: %w", err)
    }
    tx, err := d.BeginTx(ctx, nil)
    if err != nil {
        return Event{}, err
    }
    defer func() { _ = tx.Rollback() }()
    evt, err := d.insertEventTx(ctx, tx, eventInsert{
        ProjectID:   projectID,
        ProjectName: projectName,
        IssueID:     &issueID,
        IssueNumber: &issueNumber,
        Type:        "close.throttled",
        Actor:       actor,
        Payload:     string(bs),
    })
    if err != nil {
        return Event{}, err
    }
    if err := tx.Commit(); err != nil {
        return Event{}, err
    }
    return evt, nil
}
```

- [ ] **Step 4: Implement `emitThrottledEvent` in the daemon**

In `internal/daemon/close_guards.go`:

```go
// emitThrottledEvent persists a close.throttled event, broadcasts it,
// and queues it for hook delivery — the same path real close events go
// through, minus the issue mutation.
func emitThrottledEvent(
    ctx context.Context,
    cfg ServerConfig,
    issue db.Issue,
    actor, reason string,
    parentNumber int64,
    cohort []int64,
    priorIssue *int64,
) error {
    // Look up project name for the event row.
    var projectName string
    if err := cfg.DB.QueryRowContext(ctx,
        `SELECT name FROM projects WHERE id = ?`, issue.ProjectID).Scan(&projectName); err != nil {
        return fmt.Errorf("project name for throttled event: %w", err)
    }
    payload := db.CloseThrottledPayload{
        Reason:     reason,
        Parent:     parentNumber,
        Cohort:     cohort,
        PriorIssue: priorIssue,
    }
    evt, err := cfg.DB.InsertCloseThrottledEvent(ctx, issue.ProjectID, projectName,
        issue.ID, issue.Number, actor, payload)
    if err != nil {
        return err
    }
    cfg.Broadcaster.Broadcast(StreamMsg{Kind: "event", Event: &evt, ProjectID: issue.ProjectID})
    cfg.Hooks.Enqueue(evt)
    return nil
}
```

- [ ] **Step 5: Run tests**

```
go test ./internal/daemon -run TestThrottle_EmitsCloseThrottledEvent -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/db/queries.go internal/daemon/close_guards.go internal/daemon/close_guards_test.go
git commit -m "daemon: emit close.throttled events on both throttle paths"
```

---

## Task 14: CLI — `kata events --tail` renders `close.throttled` with marker

**Files:**
- Modify: `cmd/kata/events.go`
- Modify: `cmd/kata/events_test.go`

- [ ] **Step 1: Read the current events rendering**

```
grep -n "issue.closed\|case \"\|renderEvent\|printEvent" cmd/kata/events.go
```

Locate the function that formats a single event for the default text output. Note its signature.

- [ ] **Step 2: Write failing test**

Append to `cmd/kata/events_test.go` (or create one if absent):

```go
func TestEventsTail_RendersCloseThrottledWithMarker(t *testing.T) {
    var buf bytes.Buffer
    ev := apiEventEnvelope{
        Type:   "close.throttled",
        Actor:  "codex",
        IssueNumber: ptr(int64(286)),
        Payload: json.RawMessage(`{"reason":"sibling-burst","parent":281,"cohort":[283,284,285]}`),
    }
    renderEventText(&buf, ev)
    out := buf.String()
    assert.Contains(t, out, "!! THROTTLED")
    assert.Contains(t, out, "parent=#281")
    assert.Contains(t, out, "reason=sibling-burst")
    assert.Contains(t, out, "cohort=#283,#284,#285")
}
```

The test references `apiEventEnvelope`, `renderEventText`, and `ptr` — concrete names depend on what's already in `events.go`. Adjust to the existing identifiers; the assertion content is the contract.

- [ ] **Step 3: Run test to verify it fails**

```
go test ./cmd/kata -run TestEventsTail_RendersCloseThrottled -v
```

Expected: FAIL — no marker for `close.throttled`.

- [ ] **Step 4: Implement the renderer branch**

In the event renderer in `cmd/kata/events.go`, add a `case "close.throttled":` branch that unmarshals the payload (mirror the `CloseThrottledPayload` shape inline or define a small struct in the same file) and prints:

```
<time> !! THROTTLED  #<issue>  <actor>   parent=#<parent> reason=<reason> [cohort=...] [prior=#<prior>]
```

Concretely:

```go
case "close.throttled":
    var p struct {
        Reason     string  `json:"reason"`
        Parent     int64   `json:"parent,omitempty"`
        Cohort     []int64 `json:"cohort,omitempty"`
        PriorIssue *int64  `json:"prior,omitempty"`
    }
    _ = json.Unmarshal(ev.Payload, &p)
    parts := []string{fmt.Sprintf("parent=#%d", p.Parent), "reason=" + p.Reason}
    if len(p.Cohort) > 0 {
        ids := make([]string, len(p.Cohort))
        for i, n := range p.Cohort {
            ids[i] = fmt.Sprintf("#%d", n)
        }
        parts = append(parts, "cohort="+strings.Join(ids, ","))
    }
    if p.PriorIssue != nil {
        parts = append(parts, fmt.Sprintf("prior=#%d", *p.PriorIssue))
    }
    fmt.Fprintf(w, "%s !! THROTTLED  %s  %s   %s\n",
        ev.CreatedAt.Format("15:04:05"), issueRefDisplay(ev.IssueNumber), ev.Actor, strings.Join(parts, " "))
```

JSON output (`--json`) is unchanged structurally — the `kind` and `payload` already carry the full info; downstream tools can filter on `type == "close.throttled"`.

- [ ] **Step 5: Run tests**

```
go test ./cmd/kata -run TestEventsTail_RendersCloseThrottled -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/kata/events.go cmd/kata/events_test.go
git commit -m "cli: render close.throttled events with marker in kata events --tail"
```

---

## Task 15: CLI — `kata audit closes`

**Files:**
- Create: `cmd/kata/audit.go`
- Create: `cmd/kata/audit_closes.go`
- Create: `cmd/kata/audit_closes_test.go`
- Modify: `cmd/kata/main.go` (register `audit` command)
- Modify: `internal/api/types.go` (add audit list-closes request/response)
- Modify: `internal/daemon/server.go` (register handler)
- Create: `internal/daemon/handlers_audit.go`

- [ ] **Step 1: Write failing test for happy path**

Create `cmd/kata/audit_closes_test.go`:

```go
package main

import (
    "testing"

    "github.com/stretchr/testify/assert"
)

func TestAuditCloses_ListsAllClosesInWindow(t *testing.T) {
    env, dir, _ := setupWorkspaceWithIssue(t, "issue one")
    runCLI(t, env, dir, "create", "issue two")
    runCLI(t, env, dir, "close", "1", "--done",
        "--message", "Fixed first issue and ran the auth tests.",
        "--commit", "abc1234")
    runCLI(t, env, dir, "close", "2", "--wontfix",
        "--message", "Decided not to fix; out of scope for this milestone.")

    out := runCLI(t, env, dir, "audit", "closes", "--json")
    assert.Contains(t, out, `"issue":1`)
    assert.Contains(t, out, `"issue":2`)
    assert.Contains(t, out, `"reason":"done"`)
    assert.Contains(t, out, `"reason":"wontfix"`)
}

func TestAuditCloses_FilterByActor(t *testing.T) {
    env, dir, _ := setupWorkspaceWithIssue(t, "issue one")
    runCLI(t, env, dir, "create", "issue two")
    runCLIAs(t, env, dir, "alice", "close", "1", "--done",
        "--message", "Fixed by alice and tested.",
        "--commit", "abc1234")
    runCLIAs(t, env, dir, "bob", "close", "2", "--done",
        "--message", "Fixed by bob and tested.",
        "--commit", "def5678")

    out := runCLI(t, env, dir, "audit", "closes", "--actor", "alice", "--json")
    assert.Contains(t, out, `"actor":"alice"`)
    assert.NotContains(t, out, `"actor":"bob"`)
}
```

`runCLIAs` is a thin wrapper around `runCLI` that sets `KATA_AUTHOR` for one invocation. Add it to `helpers_test.go` if not present.

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./cmd/kata -run TestAuditCloses -v
```

Expected: FAIL — `audit` command does not exist.

- [ ] **Step 3: Add API types**

In `internal/api/types.go`:

```go
type AuditClosesRequest struct {
    ProjectID int64  `query:"project_id"`
    Since     string `query:"since,omitempty"`
    Until     string `query:"until,omitempty"`
    Actor     string `query:"actor,omitempty"`
    Parent    int64  `query:"parent,omitempty"`
    Reason    string `query:"reason,omitempty"`
    NoEvidence bool  `query:"no_evidence,omitempty"`
    GroupBy   string `query:"group_by,omitempty"` // "", "actor", "parent", "actor,parent"
}

type AuditCloseRow struct {
    Time          string   `json:"time"`
    Actor         string   `json:"actor"`
    Issue         int64    `json:"issue"`
    Parent        int64    `json:"parent,omitempty"`
    Reason        string   `json:"reason"`
    EvidenceTypes []string `json:"evidence_types,omitempty"`
    Flags         []string `json:"flags,omitempty"`
    Message       string   `json:"message,omitempty"`
}

type AuditClosesResponse struct {
    Body struct {
        Rows []AuditCloseRow `json:"rows"`
    }
}
```

- [ ] **Step 4: Implement daemon handler**

Create `internal/daemon/handlers_audit.go`:

```go
package daemon

import (
    "context"
    "encoding/json"
    "time"

    "github.com/danielgtaylor/huma/v2"

    "github.com/wesm/kata/internal/api"
)

func registerAuditHandlers(humaAPI huma.API, cfg ServerConfig) {
    huma.Register(humaAPI, huma.Operation{
        OperationID: "auditCloses",
        Method:      "GET",
        Path:        "/api/v1/audit/closes",
    }, func(ctx context.Context, in *api.AuditClosesRequest) (*api.AuditClosesResponse, error) {
        const tsFmt = "2006-01-02T15:04:05.000Z"
        sinceStr := time.Time{}.UTC().Format(tsFmt)
        untilStr := time.Now().UTC().Format(tsFmt)
        if in.Since != "" {
            t, err := time.Parse(time.RFC3339, in.Since)
            if err != nil {
                return nil, api.NewError(400, "bad_since", err.Error(), "", nil)
            }
            sinceStr = t.UTC().Format(tsFmt)
        }
        if in.Until != "" {
            t, err := time.Parse(time.RFC3339, in.Until)
            if err != nil {
                return nil, api.NewError(400, "bad_until", err.Error(), "", nil)
            }
            untilStr = t.UTC().Format(tsFmt)
        }
        params := db.EventsInWindowParams{
            ProjectID: in.ProjectID,
            Since:     sinceStr,
            Until:     untilStr,
        }
        if in.Actor != "" {
            params.Actors = []string{in.Actor}
        }
        events, err := cfg.DB.EventsInWindow(ctx, params)
        if err != nil {
            return nil, api.NewError(500, "internal", err.Error(), "", nil)
        }
        var rows []api.AuditCloseRow
        for _, ev := range events {
            if ev.Type != "issue.closed" {
                continue
            }
            var p struct {
                Reason   string        `json:"reason"`
                Message  string        `json:"message,omitempty"`
                Evidence []api.Evidence `json:"evidence,omitempty"`
            }
            _ = json.Unmarshal([]byte(ev.Payload), &p)
            if in.Reason != "" && p.Reason != in.Reason {
                continue
            }
            row := api.AuditCloseRow{
                Time:    ev.CreatedAt.Format(time.RFC3339),
                Actor:   ev.Actor,
                Reason:  p.Reason,
                Message: p.Message,
            }
            if ev.IssueNumber != nil {
                row.Issue = *ev.IssueNumber
            }
            for _, e := range p.Evidence {
                row.EvidenceTypes = append(row.EvidenceTypes, string(e.Type))
            }
            if len(p.Evidence) == 0 && p.Reason != "wontfix" {
                row.Flags = append(row.Flags, "no-evidence")
            }
            if in.NoEvidence && !contains(row.Flags, "no-evidence") {
                continue
            }
            rows = append(rows, row)
        }
        out := &api.AuditClosesResponse{}
        out.Body.Rows = rows
        return out, nil
    })
}

func contains(xs []string, s string) bool {
    for _, x := range xs {
        if x == s {
            return true
        }
    }
    return false
}
```

The handler reuses `EventsInWindow` from `internal/db/queries_events.go`. If it lacks the `Actor` filter, add it inline.

- [ ] **Step 5: Register the handler**

In `internal/daemon/server.go` (or wherever the other handlers are registered), call `registerAuditHandlers(humaAPI, cfg)`.

- [ ] **Step 6: Implement the CLI**

Create `cmd/kata/audit.go`:

```go
package main

import "github.com/spf13/cobra"

func newAuditCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "audit",
        Short: "audit recent activity (close events, etc.)",
    }
    cmd.AddCommand(newAuditClosesCmd())
    return cmd
}
```

Create `cmd/kata/audit_closes.go`:

```go
package main

import (
    "encoding/json"
    "fmt"
    "net/http"
    "net/url"

    "github.com/spf13/cobra"

    "github.com/wesm/kata/internal/api"
)

func newAuditClosesCmd() *cobra.Command {
    var (
        since      string
        until      string
        actor      string
        parent     int64
        reason     string
        noEvidence bool
        groupBy    string
    )
    cmd := &cobra.Command{
        Use:   "closes",
        Short: "list close events, with filters",
        RunE: func(cmd *cobra.Command, _ []string) error {
            ctx, baseURL, pid, err := resolveProjectForCommand(cmd)
            if err != nil {
                return err
            }
            v := url.Values{}
            v.Set("project_id", fmt.Sprintf("%d", pid))
            if since != "" {
                v.Set("since", since)
            }
            if until != "" {
                v.Set("until", until)
            }
            if actor != "" {
                v.Set("actor", actor)
            }
            if parent > 0 {
                v.Set("parent", fmt.Sprintf("%d", parent))
            }
            if reason != "" {
                v.Set("reason", reason)
            }
            if noEvidence {
                v.Set("no_evidence", "true")
            }
            if groupBy != "" {
                v.Set("group_by", groupBy)
            }
            client, err := httpClientFor(ctx, baseURL)
            if err != nil {
                return err
            }
            status, bs, err := httpDoJSON(ctx, client, http.MethodGet,
                fmt.Sprintf("%s/api/v1/audit/closes?%s", baseURL, v.Encode()), nil)
            if err != nil {
                return err
            }
            if status >= 400 {
                return apiErrFromBody(status, bs)
            }
            if flags.JSON {
                _, err := fmt.Fprint(cmd.OutOrStdout(), string(bs))
                return err
            }
            return printAuditClosesTable(cmd, bs)
        },
    }
    cmd.Flags().StringVar(&since, "since", "", "RFC3339 timestamp")
    cmd.Flags().StringVar(&until, "until", "", "RFC3339 timestamp")
    cmd.Flags().StringVar(&actor, "actor", "", "filter by actor")
    cmd.Flags().Int64Var(&parent, "parent", 0, "filter by parent issue number")
    cmd.Flags().StringVar(&reason, "reason", "", "filter by close reason")
    cmd.Flags().BoolVar(&noEvidence, "no-evidence", false, "only closes that carried no evidence")
    cmd.Flags().StringVar(&groupBy, "group-by", "", "group rows: actor | parent | actor,parent")
    return cmd
}

func printAuditClosesTable(cmd *cobra.Command, bs []byte) error {
    var resp api.AuditClosesResponse
    if err := json.Unmarshal(bs, &resp); err != nil {
        return err
    }
    w := cmd.OutOrStdout()
    fmt.Fprintf(w, "%-20s %-12s %-6s %-10s %-15s %s\n", "TIME", "ACTOR", "ISSUE", "REASON", "EVIDENCE", "FLAGS")
    for _, r := range resp.Body.Rows {
        fmt.Fprintf(w, "%-20s %-12s #%-5d %-10s %-15s %s\n",
            r.Time, r.Actor, r.Issue, r.Reason,
            strings.Join(r.EvidenceTypes, ","),
            strings.Join(r.Flags, ","))
    }
    return nil
}
```

- [ ] **Step 7: Register on `main.go`**

In `cmd/kata/main.go`, where other commands register with the root cobra command:

```go
root.AddCommand(newAuditCmd())
```

- [ ] **Step 8: Run all tests**

```
go test ./cmd/kata ./internal/daemon -run "Audit" -v
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add cmd/kata/audit.go cmd/kata/audit_closes.go cmd/kata/audit_closes_test.go cmd/kata/main.go internal/daemon/handlers_audit.go internal/daemon/server.go internal/api/types.go cmd/kata/helpers_test.go
git commit -m "cli: kata audit closes (read-only close-event view with filters)"
```

---

## Task 16: CLI — `kata reopen` bulk mode

**Files:**
- Modify: `cmd/kata/reopen.go`
- Modify: `cmd/kata/close_reopen_test.go`
- Modify: `internal/api/types.go` (bulk reopen request)
- Create: `internal/daemon/handlers_bulk_reopen.go`
- Modify: `internal/daemon/server.go`

- [ ] **Step 1: Read existing reopen impl**

```
grep -n "func newReopenCmd\|runAction" cmd/kata/reopen.go
```

The current command takes one issue ref and shares `runAction` with close. We will keep the single-ref form and add a bulk path that triggers when any of the filter flags is set.

- [ ] **Step 2: Write failing tests**

Append to `cmd/kata/close_reopen_test.go`:

```go
func TestReopen_BulkRequiresConfirm(t *testing.T) {
    env, dir, _ := setupWorkspaceWithIssue(t, "issue one")
    runCLI(t, env, dir, "create", "issue two")
    runCLI(t, env, dir, "close", "1", "--done",
        "--message", "Closed first issue after review.",
        "--commit", "abc1234")
    runCLI(t, env, dir, "close", "2", "--done",
        "--message", "Closed second issue after review.",
        "--commit", "abc1234")

    _, stderr, err := runCLIWithErr(t, env, dir,
        "reopen",
        "--closed-by", "$USER",
        "--since", "1970-01-01T00:00:00Z",
        "--reason", "Bad bulk close; reverting for follow-up review.")
    require.Error(t, err)
    assert.Contains(t, stderr, "--confirm")
}

func TestReopen_BulkDryRunListsMatches(t *testing.T) {
    env, dir, _ := setupWorkspaceWithIssue(t, "issue one")
    runCLI(t, env, dir, "create", "issue two")
    runCLI(t, env, dir, "close", "1", "--done",
        "--message", "Closed first issue after review.",
        "--commit", "abc1234")
    runCLI(t, env, dir, "close", "2", "--done",
        "--message", "Closed second issue after review.",
        "--commit", "abc1234")

    out := runCLI(t, env, dir,
        "reopen",
        "--closed-by", currentUser(t),
        "--since", "1970-01-01T00:00:00Z",
        "--dry-run")
    assert.Contains(t, out, "would reopen 2 issues")
    assert.Contains(t, out, "#1")
    assert.Contains(t, out, "#2")
}

func TestReopen_BulkReopensMatches(t *testing.T) {
    env, dir, _ := setupWorkspaceWithIssue(t, "issue one")
    runCLI(t, env, dir, "create", "issue two")
    runCLI(t, env, dir, "close", "1", "--done",
        "--message", "Closed first issue after review.",
        "--commit", "abc1234")
    runCLI(t, env, dir, "close", "2", "--done",
        "--message", "Closed second issue after review.",
        "--commit", "abc1234")

    runCLI(t, env, dir,
        "reopen",
        "--closed-by", currentUser(t),
        "--since", "1970-01-01T00:00:00Z",
        "--confirm", "2",
        "--reason", "Closures invalidated; deep review required first.")

    show := runCLI(t, env, dir, "show", "1", "--json")
    assert.Contains(t, show, `"status":"open"`)
}
```

`currentUser` returns the default actor for tests (probably whatever `setupWorkspaceWithIssue` configures). Add a helper to `helpers_test.go` if needed.

- [ ] **Step 3: Run tests to verify they fail**

```
go test ./cmd/kata -run TestReopen_Bulk -v
```

Expected: FAIL — no bulk mode.

- [ ] **Step 4: Implement bulk request types**

In `internal/api/types.go`:

```go
type BulkReopenRequest struct {
    ProjectID int64 `path:"project_id" required:"true"`
    Body      struct {
        Actor    string `json:"actor" required:"true"`
        ClosedBy string `json:"closed_by,omitempty"`
        Since    string `json:"since,omitempty"`
        Until    string `json:"until,omitempty"`
        Parent   int64  `json:"parent,omitempty"`
        Reason   string `json:"reason" required:"true"`
        Confirm  int64  `json:"confirm" required:"true"`
        DryRun   bool   `json:"dry_run,omitempty"`
    }
}

type BulkReopenResponse struct {
    Body struct {
        Matched   []int64 `json:"matched"`
        Reopened  []int64 `json:"reopened"`
        DryRun    bool    `json:"dry_run"`
    }
}
```

- [ ] **Step 5: Implement daemon handler**

Create `internal/daemon/handlers_bulk_reopen.go`:

```go
package daemon

import (
    "context"
    "encoding/json"
    "fmt"
    "time"

    "github.com/danielgtaylor/huma/v2"

    "github.com/wesm/kata/internal/api"
    "github.com/wesm/kata/internal/db"
)

func registerBulkReopenHandler(humaAPI huma.API, cfg ServerConfig) {
    huma.Register(humaAPI, huma.Operation{
        OperationID: "bulkReopen",
        Method:      "POST",
        Path:        "/api/v1/projects/{project_id}/actions/bulk-reopen",
    }, func(ctx context.Context, in *api.BulkReopenRequest) (*api.BulkReopenResponse, error) {
        if err := validateActor(in.Body.Actor); err != nil {
            return nil, err
        }
        norm := normalizeMessage(in.Body.Reason)
        if len(norm) < 40 {
            return nil, api.NewError(400, "validation",
                "--reason must be at least 40 chars after normalization", "", nil)
        }
        if _, trivial := trivialMessages[norm]; trivial {
            return nil, api.NewError(400, "validation",
                "--reason rejected as trivial", "", nil)
        }
        const tsFmt = "2006-01-02T15:04:05.000Z"
        sinceStr := time.Time{}.UTC().Format(tsFmt)
        untilStr := time.Now().UTC().Format(tsFmt)
        if in.Body.Since != "" {
            t, err := time.Parse(time.RFC3339, in.Body.Since)
            if err != nil {
                return nil, api.NewError(400, "bad_since", err.Error(), "", nil)
            }
            sinceStr = t.UTC().Format(tsFmt)
        }
        if in.Body.Until != "" {
            t, err := time.Parse(time.RFC3339, in.Body.Until)
            if err != nil {
                return nil, api.NewError(400, "bad_until", err.Error(), "", nil)
            }
            untilStr = t.UTC().Format(tsFmt)
        }
        params := db.EventsInWindowParams{
            ProjectID: in.ProjectID,
            Since:     sinceStr,
            Until:     untilStr,
        }
        if in.Body.ClosedBy != "" {
            params.Actors = []string{in.Body.ClosedBy}
        }
        events, err := cfg.DB.EventsInWindow(ctx, params)
        // Type filter is applied post-query since EventsInWindow doesn't filter on type.
        filtered := events[:0]
        for _, ev := range events {
            if ev.Type == "issue.closed" {
                filtered = append(filtered, ev)
            }
        }
        events = filtered
        if err != nil {
            return nil, api.NewError(500, "internal", err.Error(), "", nil)
        }
        matched := []int64{}
        seen := map[int64]struct{}{}
        for _, ev := range events {
            if ev.IssueNumber == nil {
                continue
            }
            n := *ev.IssueNumber
            if _, dup := seen[n]; dup {
                continue
            }
            // If --parent specified, filter to children.
            if in.Body.Parent > 0 {
                parentLink, err := cfg.DB.ParentOf(ctx, *ev.IssueID)
                if err != nil {
                    continue
                }
                parentIssue, err := cfg.DB.IssueByID(ctx, parentLink.ToIssueID)
                if err != nil || parentIssue.Number != in.Body.Parent {
                    continue
                }
            }
            matched = append(matched, n)
            seen[n] = struct{}{}
        }
        if int64(len(matched)) != in.Body.Confirm {
            return nil, api.NewError(409, "confirm_mismatch",
                fmt.Sprintf("--confirm=%d but %d issues match the filters", in.Body.Confirm, len(matched)),
                "", nil)
        }
        out := &api.BulkReopenResponse{}
        out.Body.Matched = matched
        out.Body.DryRun = in.Body.DryRun
        if in.Body.DryRun {
            return out, nil
        }
        for _, ev := range events {
            if ev.IssueNumber == nil {
                continue
            }
            n := *ev.IssueNumber
            if _, ok := seen[n]; !ok {
                continue
            }
            delete(seen, n)
            _, _, _, err := cfg.DB.ReopenIssue(ctx, *ev.IssueID, in.Body.Actor, in.Body.Reason)
            if err != nil {
                return nil, api.NewError(500, "internal", err.Error(), "", nil)
            }
            out.Body.Reopened = append(out.Body.Reopened, n)
        }
        return out, nil
    })
}
```

Extend `ReopenIssue` in `internal/db/queries.go` to accept a `bulkReason` argument and persist it on the `issue.reopened` event payload, mirroring the close-payload approach from Task 4. The single-ref reopen path continues to pass an empty string for `bulkReason`, producing the same payload it does today.

```go
// In internal/db/queries.go, replace the existing ReopenIssue signature:
func (d *DB) ReopenIssue(
    ctx context.Context, issueID int64, actor, bulkReason string,
) (Issue, *Event, bool, error) {
    // ...existing setup unchanged...
    payloadBytes, err := json.Marshal(struct {
        BulkReason string `json:"bulk_reason,omitempty"`
    }{BulkReason: bulkReason})
    if err != nil {
        return Issue{}, nil, false, fmt.Errorf("reopen payload: %w", err)
    }
    // Use string(payloadBytes) where the existing implementation set Payload.
    // If the current Payload was empty/`{}`, this is a strict superset.
}
```

Update the single-ref reopen caller in `internal/daemon/handlers_actions.go` to pass `""` for the new argument. Update any other in-tree callers similarly (grep `ReopenIssue(`). Add a `TestReopenIssue_PersistsBulkReason` test in `internal/db/queries_issues_test.go` that asserts the payload contains the supplied reason when non-empty and is `{}` when empty.

- [ ] **Step 6: Implement the CLI bulk path**

Replace `cmd/kata/reopen.go`:

```go
package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"

    "github.com/spf13/cobra"
)

func newReopenCmd() *cobra.Command {
    var (
        closedBy string
        since    string
        until    string
        parent   int64
        reason   string
        confirm  int64
        dryRun   bool
    )
    cmd := &cobra.Command{
        Use:   "reopen [<issue-ref>]",
        Short: "reopen an issue, or bulk-reopen with filters",
        Args:  cobra.MaximumNArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            bulkRequested := closedBy != "" || since != "" || until != "" || parent > 0
            if !bulkRequested {
                if len(args) != 1 {
                    return fmt.Errorf("reopen: single-issue mode requires <issue-ref>; bulk mode requires at least one filter flag")
                }
                return runAction(cmd, args[0], "reopen", nil)
            }
            if len(args) > 0 {
                return fmt.Errorf("reopen: cannot mix positional <issue-ref> with bulk filters")
            }
            if !dryRun {
                if reason == "" {
                    return fmt.Errorf("reopen bulk: --reason is required (≥40 chars)")
                }
                if confirm <= 0 {
                    return fmt.Errorf("reopen bulk: --confirm <N> is required (matches expected count)")
                }
            }
            ctx, baseURL, pid, err := resolveProjectForCommand(cmd)
            if err != nil {
                return err
            }
            actor, _ := resolveActor(flags.As, nil)
            body := map[string]any{
                "actor":     actor,
                "closed_by": closedBy,
                "since":     since,
                "until":     until,
                "parent":    parent,
                "reason":    reason,
                "confirm":   confirm,
                "dry_run":   dryRun,
            }
            client, err := httpClientFor(ctx, baseURL)
            if err != nil {
                return err
            }
            status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
                fmt.Sprintf("%s/api/v1/projects/%d/actions/bulk-reopen", baseURL, pid), body)
            if err != nil {
                return err
            }
            if status >= 400 {
                return apiErrFromBody(status, bs)
            }
            if flags.JSON {
                _, err := fmt.Fprint(cmd.OutOrStdout(), string(bs))
                return err
            }
            var resp struct {
                Matched  []int64 `json:"matched"`
                Reopened []int64 `json:"reopened"`
                DryRun   bool    `json:"dry_run"`
            }
            if err := json.NewDecoder(bytes.NewReader(bs)).Decode(&resp); err != nil {
                return err
            }
            if resp.DryRun {
                fmt.Fprintf(cmd.OutOrStdout(), "would reopen %d issues: %s\n", len(resp.Matched), formatRefs(resp.Matched))
            } else {
                fmt.Fprintf(cmd.OutOrStdout(), "reopened %d issues: %s\n", len(resp.Reopened), formatRefs(resp.Reopened))
            }
            return nil
        },
    }
    cmd.Flags().StringVar(&closedBy, "closed-by", "", "filter: actor that closed the issue")
    cmd.Flags().StringVar(&since, "since", "", "filter: RFC3339 timestamp lower bound")
    cmd.Flags().StringVar(&until, "until", "", "filter: RFC3339 timestamp upper bound")
    cmd.Flags().Int64Var(&parent, "parent", 0, "filter: parent issue number")
    cmd.Flags().StringVar(&reason, "reason", "", "bulk: required justification ≥40 chars")
    cmd.Flags().Int64Var(&confirm, "confirm", 0, "bulk: required, must match matched count")
    cmd.Flags().BoolVar(&dryRun, "dry-run", false, "bulk: list matches without reopening")
    return cmd
}

func formatRefs(ns []int64) string {
    parts := make([]string, len(ns))
    for i, n := range ns {
        parts[i] = fmt.Sprintf("#%d", n)
    }
    return strings.Join(parts, ", ")
}
```

- [ ] **Step 7: Run tests**

```
go test ./cmd/kata -run TestReopen_Bulk -v
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add cmd/kata/reopen.go internal/api/types.go internal/daemon/handlers_bulk_reopen.go internal/daemon/server.go cmd/kata/close_reopen_test.go internal/db/queries.go
git commit -m "cli: kata reopen bulk mode with --closed-by / --since / --parent filters"
```

---

## Task 17: Docs — `kata quickstart` and `AGENTS.md`

**Files:**
- Modify: `cmd/kata/quickstart.go`
- Modify: `AGENTS.md`
- Test: `cmd/kata/quickstart_test.go`

- [ ] **Step 1: Write failing test for the rewritten close step**

In `cmd/kata/quickstart_test.go`:

```go
func TestQuickstart_PromotesCloseStep(t *testing.T) {
    out := runCLI(t, nil, "", "quickstart")
    // Close discipline should appear early (within first 600 chars).
    idx := strings.Index(out, "kata close")
    require.GreaterOrEqual(t, idx, 0)
    require.LessOrEqual(t, idx, 600, "close discipline should appear early in quickstart")
    assert.Contains(t, out, "asserts that the work is complete")
    assert.Contains(t, out, "--evidence")
    assert.Contains(t, out, "needs-review")
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./cmd/kata -run TestQuickstart_PromotesCloseStep -v
```

Expected: FAIL — current quickstart has close at step 9, no `--evidence` mention.

- [ ] **Step 3: Rewrite the quickstart text**

In `cmd/kata/quickstart.go`, modify `agentQuickstartText`. The new step ordering promotes close discipline. Replace the body of `agentQuickstartText` with the new outline (preserving the other sections):

- Step 1: workspace / project / author
- **Step 2 (new): close discipline.** Explains that close asserts completion. Shows the canonical and sugar forms. Names the alternative (`kata edit <ref> --label needs-review`).
- Subsequent steps shift down by one.

Concrete new step 2:

```
2. Closing an issue ASSERTS the work is complete. This is a stronger
   claim than a comment. If the work is not actually done, DO NOT close.
   Instead:

      kata edit <ref> --label needs-review
      kata comment <ref> --body "what was attempted, what remains"

   When the work IS done, close with evidence:

      kata close 12 --done \
        --message "Fixed the Safari callback double-submit and verified tests." \
        --commit <sha>

   Other close forms:

      kata close 12 --duplicate-of 7 --message "Same Safari race."
      kata close 12 --superseded-by 18 --message "Replaced by broader scope."
      kata close 12 --audit-no-change \
                    --message "Reviewed schema and queries; no change needed." \
                    --reviewed internal/db/schema.sql

   The daemon refuses parent-close while open children remain, and
   throttles rapid sibling-close bursts under the same parent. Slow
   down and review each child individually.
```

- [ ] **Step 4: Mirror in `AGENTS.md`**

Copy the new step verbatim into the corresponding section of `AGENTS.md`. The file is the persistent companion to `kata quickstart` — they must stay aligned.

- [ ] **Step 5: Run tests**

```
go test ./cmd/kata -run TestQuickstart -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/kata/quickstart.go AGENTS.md cmd/kata/quickstart_test.go
git commit -m "docs: promote close discipline in quickstart and AGENTS.md"
```

---

## Task 18: Docs — README and CLAUDE.md

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add a "Closing issues" subsection to README**

In `README.md`, near the existing "Labels, ownership, and relationships" section, add:

```markdown
### Closing issues

Closing an issue asserts that the work is complete. Provide a substantive
message and typed evidence. The cheap forms:

    kata close 12 --done --message "<what changed and how it was verified>" \
                  --commit <sha>
    kata close 12 --duplicate-of 7 --message "<short pointer>"
    kata close 12 --wontfix --message "<rationale>"

The daemon refuses these structurally dangerous patterns:

- closing a parent while its children remain open
- closing >3 siblings under the same parent within 5 minutes
- closing two siblings of the same parent with the same close message
  (within 30 minutes, for `done`/`audit-no-change`)

`kata audit closes` and `kata reopen --closed-by ... --since ... --parent ...`
recover from invalid bulk closures.
```

- [ ] **Step 2: Update `CLAUDE.md` (project)**

In `/Users/wesm/code/kata/CLAUDE.md`, the "Project management" bullet list mentions close. Update the close bullet to:

```markdown
- Close only when the work is actually complete:
  `kata close <N> --done --message "<scope + verification>" --commit <sha>`.
  Use `--duplicate-of <N>`, `--superseded-by <N>`, `--audit-no-change`, or
  `--wontfix` when those reasons fit. The daemon refuses parent-close while
  children are open and throttles sibling-close bursts.
```

- [ ] **Step 3: Add a spec-index pointer**

In the same CLAUDE.md, "Specs and plans" section, add a line under the existing spec list pointing to the new spec:
`Closure justification: docs/superpowers/specs/2026-05-10-anti-agent-justification-design.md`.

- [ ] **Step 4: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "docs: README and CLAUDE.md cover the new close discipline"
```

---

## Task 19: Final verification

- [ ] **Step 1: Run the full test suite**

```
go test ./... -count=1
```

Expected: PASS. Any failures here likely mean an upstream task introduced a regression — fix in place and add a regression test rather than papering over with skips.

- [ ] **Step 2: Run the linter**

```
golangci-lint run ./...
```

Expected: clean. Fix every warning; do not introduce inline ignores without justification.

- [ ] **Step 3: Manual end-to-end against a fresh workspace**

```
mkdir /tmp/kata-aaj && cd /tmp/kata-aaj
git init && touch README.md && git add . && git commit -m init
kata init
kata create "parent issue"        # number 1
kata create "child A" --parent 1  # number 2
kata create "child B" --parent 1  # number 3

# Refusal: parent has open children.
kata close 1 --done --message "All done." --commit abc1234 2>&1 | grep "open children"

# Refusal: trivial message.
kata close 2 --done --message "done" --commit abc1234 2>&1 | grep "trivial"

# Refusal: too short.
kata close 2 --done --message "ok fixed" --commit abc1234 2>&1 | grep "too short"

# Refusal: no evidence.
kata close 2 --done --message "Fixed Safari callback double-submit and ran tests." 2>&1 | grep "evidence required"

# Success.
kata close 2 --done \
  --message "Fixed Safari callback double-submit and ran tests." \
  --commit abc1234
kata close 3 --done \
  --message "Fixed sibling component and ran tests." \
  --commit def5678
kata close 1 --done \
  --message "All children completed; parent ready to close." \
  --reviewed internal/foo --reviewed internal/bar

# Audit view.
kata audit closes --json
```

Expected: all four refusals fire with the documented error, the three real closes succeed.

- [ ] **Step 4: Verify recovery path**

Reopen everything you just closed using the bulk filters:

```
# Preview the matched set.
kata reopen --closed-by $USER --since 1970-01-01T00:00:00Z --dry-run

# After confirming the count, do the real reopen. <N> equals the number
# of issues the dry-run reported.
kata reopen --closed-by $USER --since 1970-01-01T00:00:00Z \
            --confirm <N> \
            --reason "Verification of v1 anti-agent-justification flow."
```

Confirm `kata show 1`, `kata show 2`, `kata show 3` all report `status: open`. Confirm the `issue.reopened` events carry the bulk reason in their payload.

- [ ] **Step 5: Verify documentation**

```
kata quickstart | head -40
```

Confirm close discipline appears early and clearly.

- [ ] **Step 6: Inspect close.throttled rendering**

In one terminal: `kata events --tail`.
In another: deliberately trigger a sibling-burst (create 4 children of a fresh parent and close them in rapid succession). Confirm the throttle event appears in the tail with the `!! THROTTLED` marker.

- [ ] **Step 7: Final commit and PR**

```bash
git push origin anti-agent-justification
gh pr create --title "Closure justification and anti-agent-abuse guards" \
  --body "$(cat <<'EOF'
## Summary
- Adds typed `--evidence` + substance-checked `--message` to `kata close`.
- Daemon refuses parent-close while open children remain.
- Daemon throttles rapid sibling-close bursts and identical sibling messages.
- New `kata audit closes` view and bulk filters on `kata reopen`.

Spec: `docs/superpowers/specs/2026-05-10-anti-agent-justification-design.md`.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Only run the `gh pr create` step after the user reviews and signs off — bulk-write to the remote is a hard-to-reverse action.
