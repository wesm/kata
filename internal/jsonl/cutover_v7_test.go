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

// TestCutoverV7_RewritesLinkEventPayload pins the event-payload rewrite for
// issue.linked / issue.unlinked. v7 events carried (from_number, to_number);
// v8 readers expect (from_short_id, from_uid, to_short_id, to_uid). Without
// the rewrite, post-cutover the TUI's Enter-on-event jump would silently
// no-op because to_short_id is missing.
func TestCutoverV7_RewritesLinkEventPayload(t *testing.T) {
	ctx := context.Background()
	target := openImportTargetDB(t)

	// Two issues, both in project 1; their ULIDs differ so each gets a
	// distinct length-4 short_id.
	uidA := "01HZNQ7VFPK1XGD8R5MABCD4EX"
	uidB := "01HZNQ7VFPK1XGD8R5MABCDXYZ"
	jsonlInput := buildJSONL(
		`{"kind":"meta","data":{"key":"export_version","value":"7"}}`,
		`{"kind":"project","data":{"id":1,"uid":"01HZZZZZZZZZZZZZZZZZZZZZZZ","name":"demo","created_at":"2026-05-03T00:00:00.000Z","next_issue_number":3}}`,
		`{"kind":"issue","data":{"id":1,"uid":"`+uidA+`","project_id":1,"number":1,"title":"a","body":"","status":"open","closed_reason":null,"owner":null,"author":"tester","created_at":"2026-05-03T00:00:01.000Z","updated_at":"2026-05-03T00:00:01.000Z","closed_at":null,"deleted_at":null}}`,
		`{"kind":"issue","data":{"id":2,"uid":"`+uidB+`","project_id":1,"number":2,"title":"b","body":"","status":"open","closed_reason":null,"owner":null,"author":"tester","created_at":"2026-05-03T00:00:02.000Z","updated_at":"2026-05-03T00:00:02.000Z","closed_at":null,"deleted_at":null}}`,
		`{"kind":"event","data":{"id":1,"project_id":1,"project_name":"demo","issue_id":1,"issue_uid":"`+uidA+`","related_issue_id":2,"related_issue_uid":"`+uidB+`","type":"issue.linked","actor":"tester","payload":{"link_id":42,"type":"blocks","from_number":1,"to_number":2},"created_at":"2026-05-03T00:00:03.000Z","uid":"01HZZZZZZZZZZZZZZZZZZZZZE1","origin_instance_uid":"01HZZZZZZZZZZZZZZZZZZZZZ00"}}`,
	)
	require.NoError(t, jsonl.Import(ctx, strings.NewReader(jsonlInput), target))

	var payload string
	require.NoError(t, target.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE type='issue.linked'`).Scan(&payload))
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &got))

	assert.Equal(t, "d4ex", got["from_short_id"])
	assert.Equal(t, uidA, got["from_uid"])
	assert.Equal(t, "dxyz", got["to_short_id"])
	assert.Equal(t, uidB, got["to_uid"])
	_, hasFromNumber := got["from_number"]
	assert.False(t, hasFromNumber, "legacy from_number must be stripped post-cutover")
	_, hasToNumber := got["to_number"]
	assert.False(t, hasToNumber, "legacy to_number must be stripped post-cutover")
	assert.EqualValues(t, 42, got["link_id"], "link_id and type carry through")
	assert.Equal(t, "blocks", got["type"])
}

// TestCutoverV7_RewritesCreatedEventLinks pins the rewrite for issue.created
// payloads whose `links` array carries v7 {type, to_number} entries: each
// entry must end up with to_short_id and to_issue_uid, with to_number
// stripped. Other payload fields (labels, idempotency_key) are untouched.
func TestCutoverV7_RewritesCreatedEventLinks(t *testing.T) {
	ctx := context.Background()
	target := openImportTargetDB(t)

	uidParent := "01HZNQ7VFPK1XGD8R5MABCD4EX"
	uidChild := "01HZNQ7VFPK1XGD8R5MABCDXYZ"
	jsonlInput := buildJSONL(
		`{"kind":"meta","data":{"key":"export_version","value":"7"}}`,
		`{"kind":"project","data":{"id":1,"uid":"01HZZZZZZZZZZZZZZZZZZZZZZZ","name":"demo","created_at":"2026-05-03T00:00:00.000Z","next_issue_number":3}}`,
		`{"kind":"issue","data":{"id":1,"uid":"`+uidParent+`","project_id":1,"number":1,"title":"parent","body":"","status":"open","closed_reason":null,"owner":null,"author":"tester","created_at":"2026-05-03T00:00:01.000Z","updated_at":"2026-05-03T00:00:01.000Z","closed_at":null,"deleted_at":null}}`,
		`{"kind":"issue","data":{"id":2,"uid":"`+uidChild+`","project_id":1,"number":2,"title":"child","body":"","status":"open","closed_reason":null,"owner":null,"author":"tester","created_at":"2026-05-03T00:00:02.000Z","updated_at":"2026-05-03T00:00:02.000Z","closed_at":null,"deleted_at":null}}`,
		`{"kind":"event","data":{"id":1,"project_id":1,"project_name":"demo","issue_id":2,"issue_uid":"`+uidChild+`","related_issue_id":null,"related_issue_uid":null,"type":"issue.created","actor":"tester","payload":{"labels":["bug"],"links":[{"type":"parent","to_number":1}],"idempotency_key":"k"},"created_at":"2026-05-03T00:00:02.000Z","uid":"01HZZZZZZZZZZZZZZZZZZZZZE2","origin_instance_uid":"01HZZZZZZZZZZZZZZZZZZZZZ00"}}`,
	)
	require.NoError(t, jsonl.Import(ctx, strings.NewReader(jsonlInput), target))

	var payload string
	require.NoError(t, target.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE type='issue.created'`).Scan(&payload))
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &got))

	links, ok := got["links"].([]any)
	require.True(t, ok, "links must remain an array")
	require.Len(t, links, 1)
	link := links[0].(map[string]any)
	assert.Equal(t, "parent", link["type"])
	assert.Equal(t, "d4ex", link["to_short_id"])
	assert.Equal(t, uidParent, link["to_issue_uid"])
	_, hasNumber := link["to_number"]
	assert.False(t, hasNumber, "legacy to_number must be stripped from each link entry")

	// Top-level fields untouched.
	assert.Equal(t, "k", got["idempotency_key"])
	labels, _ := got["labels"].([]any)
	assert.Equal(t, []any{"bug"}, labels)
}

// TestCutoverV7_RewritesLinksChangedPayload pins the rewrite for the
// aggregated edit event: v7 numeric parent_set / blocks_added become v8
// string short_ids paired with parallel *_uid / *_uids fields.
func TestCutoverV7_RewritesLinksChangedPayload(t *testing.T) {
	ctx := context.Background()
	target := openImportTargetDB(t)

	uidA := "01HZNQ7VFPK1XGD8R5MABCD4EX"
	uidB := "01HZNQ7VFPK1XGD8R5MABCDXYZ"
	uidC := "01HZNQ7VFPK1XGD8R5MABCDQ99"
	jsonlInput := buildJSONL(
		`{"kind":"meta","data":{"key":"export_version","value":"7"}}`,
		`{"kind":"project","data":{"id":1,"uid":"01HZZZZZZZZZZZZZZZZZZZZZZZ","name":"demo","created_at":"2026-05-03T00:00:00.000Z","next_issue_number":4}}`,
		`{"kind":"issue","data":{"id":1,"uid":"`+uidA+`","project_id":1,"number":1,"title":"a","body":"","status":"open","closed_reason":null,"owner":null,"author":"tester","created_at":"2026-05-03T00:00:01.000Z","updated_at":"2026-05-03T00:00:01.000Z","closed_at":null,"deleted_at":null}}`,
		`{"kind":"issue","data":{"id":2,"uid":"`+uidB+`","project_id":1,"number":2,"title":"b","body":"","status":"open","closed_reason":null,"owner":null,"author":"tester","created_at":"2026-05-03T00:00:02.000Z","updated_at":"2026-05-03T00:00:02.000Z","closed_at":null,"deleted_at":null}}`,
		`{"kind":"issue","data":{"id":3,"uid":"`+uidC+`","project_id":1,"number":3,"title":"c","body":"","status":"open","closed_reason":null,"owner":null,"author":"tester","created_at":"2026-05-03T00:00:03.000Z","updated_at":"2026-05-03T00:00:03.000Z","closed_at":null,"deleted_at":null}}`,
		`{"kind":"event","data":{"id":1,"project_id":1,"project_name":"demo","issue_id":1,"issue_uid":"`+uidA+`","related_issue_id":null,"related_issue_uid":null,"type":"issue.links_changed","actor":"tester","payload":{"parent_set":2,"blocks_added":[3]},"created_at":"2026-05-03T00:00:04.000Z","uid":"01HZZZZZZZZZZZZZZZZZZZZZE3","origin_instance_uid":"01HZZZZZZZZZZZZZZZZZZZZZ00"}}`,
	)
	require.NoError(t, jsonl.Import(ctx, strings.NewReader(jsonlInput), target))

	var payload string
	require.NoError(t, target.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE type='issue.links_changed'`).Scan(&payload))
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &got))

	assert.Equal(t, "dxyz", got["parent_set"])
	assert.Equal(t, uidB, got["parent_set_uid"])
	assert.Equal(t, []any{"dq99"}, got["blocks_added"])
	assert.Equal(t, []any{uidC}, got["blocks_added_uids"])
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
