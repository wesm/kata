package jsonl_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
)

// TestCutoverV7_AssignsShortIDsInULIDOrder pins spec §4.2 and §8 for the
// v7→v8 step: two issues whose ULIDs share their last 4 chars are replayed in
// ULID-ascending order; the lexically-earlier ULID wins length-4 and the
// later one extends to length 5.
func TestCutoverV7_AssignsShortIDsInULIDOrder(t *testing.T) {
	ctx := context.Background()
	target := openImportTargetDB(t)

	// UIDs share the same last 4 chars ("d4ex"). The lexically-earlier UID
	// (no extra 'x' before the suffix) should win the length-4 short_id.
	jsonlInput := buildV7Fixture(t, []v7Issue{
		{ProjectID: 1, ProjectName: "demo", UID: "01HZNQ7VFPK1XGD8R5MABCD4EX", Number: 1, Title: "first"},
		{ProjectID: 1, ProjectName: "demo", UID: "01HZNQ7VFPK1XGD8R5MABXD4EX", Number: 2, Title: "second"},
	})
	require.NoError(t, jsonl.Import(ctx, strings.NewReader(jsonlInput), target))

	proj, err := target.ProjectByName(ctx, "demo")
	require.NoError(t, err)
	rows, err := target.ListIssues(ctx, db.ListIssuesParams{ProjectID: proj.ID})
	require.NoError(t, err)
	require.Len(t, rows, 2)

	byUID := map[string]db.Issue{}
	for _, r := range rows {
		byUID[r.UID] = r
	}
	first := byUID["01HZNQ7VFPK1XGD8R5MABCD4EX"]
	second := byUID["01HZNQ7VFPK1XGD8R5MABXD4EX"]
	assert.Equal(t, "d4ex", first.ShortID, "lexically-earlier UID wins length-4")
	assert.Equal(t, "xd4ex", second.ShortID, "second issue extends to length 5")
}

// TestCutoverV7_RejectsProjectNameWithHash pins spec §3: project names cannot
// contain '#' after cutover. The cutover must fail before any inserts with an
// error that names the offending project.
func TestCutoverV7_RejectsProjectNameWithHash(t *testing.T) {
	ctx := context.Background()
	target := openImportTargetDB(t)

	jsonlInput := buildV7Fixture(t, []v7Issue{
		{ProjectID: 1, ProjectName: "has#hash", UID: "01HZNQ7VFPK1XGD8R5MABCD4EX", Number: 1, Title: "ok"},
	})
	err := jsonl.Import(ctx, strings.NewReader(jsonlInput), target)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "has#hash")
	assert.Contains(t, err.Error(), "must not contain '#'")

	// Cutover must fail BEFORE any inserts.
	var n int
	require.NoError(t, target.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&n))
	assert.Equal(t, 0, n, "cutover must fail before mutating target")
}

// importEventPayload runs the cutover via jsonl.Import and returns the
// rewritten payload for the single matching event type. Each cutover
// event-rewrite test imports a small fixture of issues + one event and
// verifies the post-cutover payload shape.
func importEventPayload(t *testing.T, issues []v7Issue, ev v7Event) map[string]any {
	t.Helper()
	ctx := context.Background()
	target := openImportTargetDB(t)
	require.NoError(t, jsonl.Import(ctx,
		strings.NewReader(buildV7FixtureWith(t, issues, []v7Event{ev})), target))
	var payload string
	require.NoError(t, target.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE type=?`, ev.Type).Scan(&payload))
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &got))
	return got
}

// Shared v7 issue UIDs for the event-payload rewrite tests. The last-4
// suffixes are unique so each issue lands at L=4 post-cutover.
const (
	v7UIDA = "01HZNQ7VFPK1XGD8R5MABCD4EX" // → short_id "d4ex"
	v7UIDB = "01HZNQ7VFPK1XGD8R5MABCDXYZ" // → "dxyz"
	v7UIDC = "01HZNQ7VFPK1XGD8R5MABCDQ99" // → "dq99"
)

func twoV7Issues() []v7Issue {
	return []v7Issue{
		{ProjectID: 1, ProjectName: "demo", UID: v7UIDA, Number: 1, Title: "a"},
		{ProjectID: 1, ProjectName: "demo", UID: v7UIDB, Number: 2, Title: "b"},
	}
}

// TestCutoverV7_RewritesLinkEventPayload pins the event-payload rewrite for
// issue.linked / issue.unlinked. v7 events carried (from_number, to_number);
// v8 readers expect (from_short_id, from_uid, to_short_id, to_uid). Without
// the rewrite, post-cutover the TUI's Enter-on-event jump would silently
// no-op because to_short_id is missing.
func TestCutoverV7_RewritesLinkEventPayload(t *testing.T) {
	got := importEventPayload(t, twoV7Issues(), v7Event{
		ID: 1, ProjectID: 1, ProjectName: "demo", Type: "issue.linked",
		IssueID: 1, IssueUID: v7UIDA,
		PayloadJSON: `{"link_id":42,"type":"blocks","from_number":1,"to_number":2}`,
	})
	assert.Equal(t, "d4ex", got["from_short_id"])
	assert.Equal(t, v7UIDA, got["from_uid"])
	assert.Equal(t, "dxyz", got["to_short_id"])
	assert.Equal(t, v7UIDB, got["to_uid"])
	assert.NotContains(t, got, "from_number", "legacy from_number must be stripped")
	assert.NotContains(t, got, "to_number", "legacy to_number must be stripped")
	assert.EqualValues(t, 42, got["link_id"])
	assert.Equal(t, "blocks", got["type"])
}

// TestCutoverV7_RewritesCreatedEventLinks pins the rewrite for issue.created
// payloads whose `links` array carries v7 {type, to_number} entries: each
// entry must end up with to_short_id and to_issue_uid, with to_number
// stripped. Other payload fields (labels, idempotency_key) are untouched.
func TestCutoverV7_RewritesCreatedEventLinks(t *testing.T) {
	got := importEventPayload(t, twoV7Issues(), v7Event{
		ID: 1, ProjectID: 1, ProjectName: "demo", Type: "issue.created",
		IssueID: 2, IssueUID: v7UIDB,
		PayloadJSON: `{"labels":["bug"],"links":[{"type":"parent","to_number":1}],"idempotency_key":"k"}`,
	})
	links, ok := got["links"].([]any)
	require.True(t, ok, "links must remain an array")
	require.Len(t, links, 1)
	link := links[0].(map[string]any)
	assert.Equal(t, "parent", link["type"])
	assert.Equal(t, "d4ex", link["to_short_id"])
	assert.Equal(t, v7UIDA, link["to_issue_uid"])
	assert.NotContains(t, link, "to_number")
	// Untouched top-level fields.
	assert.Equal(t, "k", got["idempotency_key"])
	assert.Equal(t, []any{"bug"}, got["labels"])
}

// TestCutoverV7_RewritesLinksChangedPayload pins the rewrite for the
// aggregated edit event: v7 numeric parent_set / blocks_added become v8
// string short_ids paired with parallel *_uid / *_uids fields.
func TestCutoverV7_RewritesLinksChangedPayload(t *testing.T) {
	issues := append(twoV7Issues(),
		v7Issue{ProjectID: 1, ProjectName: "demo", UID: v7UIDC, Number: 3, Title: "c"})
	got := importEventPayload(t, issues, v7Event{
		ID: 1, ProjectID: 1, ProjectName: "demo", Type: "issue.links_changed",
		IssueID: 1, IssueUID: v7UIDA,
		PayloadJSON: `{"parent_set":2,"blocks_added":[3]}`,
	})
	assert.Equal(t, "dxyz", got["parent_set"])
	assert.Equal(t, v7UIDB, got["parent_set_uid"])
	assert.Equal(t, []any{"dq99"}, got["blocks_added"])
	assert.Equal(t, []any{v7UIDC}, got["blocks_added_uids"])
}

// TestCutover_PreservesStoredShortIDs pins spec §8.1: a round-trip on a v8
// database preserves stored short_ids byte-for-byte. The current-version
// import path must honor the source's short_id rather than re-deriving it.
func TestCutover_PreservesStoredShortIDs(t *testing.T) {
	ctx := context.Background()

	src := openExportTestDB(t)
	p, err := src.CreateProject(ctx, "demo")
	require.NoError(t, err)
	a, _, err := src.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID,
		UID:       "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title:     "a",
		Author:    "tester",
	})
	require.NoError(t, err)
	b, _, err := src.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID,
		UID:       "01HZNQ7VFPK1XGD8R5MABXD4EX",
		Title:     "b",
		Author:    "tester",
	})
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, src, &buf, jsonl.ExportOptions{}))

	dst := openImportTargetDB(t)
	require.NoError(t, jsonl.Import(ctx, bytes.NewReader(buf.Bytes()), dst))

	got, err := dst.IssueByUID(ctx, a.UID, db.IncludeDeletedNo)
	require.NoError(t, err)
	assert.Equal(t, a.ShortID, got.ShortID)
	got, err = dst.IssueByUID(ctx, b.UID, db.IncludeDeletedNo)
	require.NoError(t, err)
	assert.Equal(t, b.ShortID, got.ShortID)
}
