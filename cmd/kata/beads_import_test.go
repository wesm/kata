package main

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseBeadsExportAndBuildImportRequest(t *testing.T) {
	export := strings.NewReader(`{"id":"b1","title":"Blocker","description":"blocker body","status":"open","priority":1,"issue_type":"task","owner":"alice","created_at":"2026-05-01T10:00:00Z","created_by":"Alice","updated_at":"2026-05-01T10:00:00Z","labels":["Needs Review","bad label!","` + strings.Repeat("Very Long Label ", 8) + `"],"dependency_count":0,"dependent_count":1,"comment_count":0}
{"id":"b2","title":"Blocked","description":"blocked body","status":"closed","priority":2,"issue_type":"bug","owner":"bob","created_at":"2026-05-01T11:00:00Z","created_by":"Bob","updated_at":"2026-05-01T12:00:00Z","closed_at":"2026-05-01T12:00:00Z","close_reason":"fixed elsewhere","labels":[],"dependencies":[{"issue_id":"b2","depends_on_id":"b1","type":"blocks","created_at":"2026-05-01T11:30:00Z","created_by":"Bob","metadata":"{}"}],"comment_count":1}
`)
	commentJSON := strings.NewReader(`[{"id":"c1","issue_id":"b2","author":"Bob","text":"comment","created_at":"2026-05-01T11:45:00Z"}]`)
	parsedComments, err := parseBeadsCommentsJSON(commentJSON)
	require.NoError(t, err)
	comments := map[string][]beadsComment{"b2": parsedComments}

	req, err := buildBeadsImportRequest(export, comments, "importer")
	require.NoError(t, err)
	assert.Equal(t, "importer", req.Actor)
	assert.Equal(t, "beads", req.Source)
	require.Len(t, req.Items, 2)

	blocker := req.Items[0]
	assert.Equal(t, "b1", blocker.ExternalID)
	assert.Equal(t, "Blocker", blocker.Title)
	assert.Equal(t, "blocker body", strings.Split(blocker.Body, "\n---\n")[0])
	assert.Equal(t, "open", blocker.Status)
	assert.Equal(t, "Alice", blocker.Author)
	require.NotNil(t, blocker.Owner)
	assert.Equal(t, "alice", *blocker.Owner)
	assert.Equal(t, mustParseTime(t, "2026-05-01T10:00:00Z"), blocker.CreatedAt)
	assert.Equal(t, mustParseTime(t, "2026-05-01T10:00:00Z"), blocker.UpdatedAt)
	assert.Nil(t, blocker.ClosedAt)
	assert.Nil(t, blocker.ClosedReason)
	assert.Contains(t, blocker.Labels, "source:beads")
	assert.Contains(t, blocker.Labels, "beads-id:b1")
	assert.Contains(t, blocker.Labels, "needs-review")
	assert.Contains(t, blocker.Labels, "bad-label")
	for _, label := range blocker.Labels {
		assert.LessOrEqual(t, len(label), 64, "label %q should satisfy kata max length", label)
	}
	assert.Contains(t, blocker.Body, "Imported from Beads")
	assert.Contains(t, blocker.Body, "beads_id: b1")
	assert.Contains(t, blocker.Body, "beads_type: task")
	assert.Contains(t, blocker.Body, "beads_priority: 1")
	assert.Contains(t, blocker.Body, `beads_original_labels: ["Needs Review","bad label!"`)
	assert.NotContains(t, blocker.Body, "beads_dependencies")
	assert.NotContains(t, req.Items[1].Body, "beads_dependencies")
	assert.NotContains(t, req.Items[1].Body, "metadata")
	assert.NotContains(t, req.Items[1].Body, "created_by")
	assert.Contains(t, blocker.Body, "beads_created_at: 2026-05-01T10:00:00Z")
	assert.Contains(t, blocker.Body, "beads_updated_at: 2026-05-01T10:00:00Z")
	assert.Contains(t, blocker.Body, "beads_comment_count: 0")
	require.Len(t, blocker.Links, 1)
	assert.Equal(t, "blocks", blocker.Links[0].Type)
	assert.Equal(t, "b2", blocker.Links[0].TargetExternalID, "Beads A depends on B imports as B blocks A")

	blocked := req.Items[1]
	assert.Equal(t, "b2", blocked.ExternalID)
	assert.Equal(t, "closed", blocked.Status)
	require.NotNil(t, blocked.ClosedReason)
	assert.Equal(t, "done", *blocked.ClosedReason)
	require.NotNil(t, blocked.ClosedAt)
	assert.Equal(t, mustParseTime(t, "2026-05-01T12:00:00Z"), *blocked.ClosedAt)
	assert.Contains(t, blocked.Body, "beads_close_reason: fixed elsewhere")
	assert.Contains(t, blocked.Labels, "source:beads")
	assert.Contains(t, blocked.Labels, "beads-id:b2")
	require.Len(t, blocked.Comments, 1)
	assert.Equal(t, "c1", blocked.Comments[0].ExternalID)
	assert.Equal(t, "Bob", blocked.Comments[0].Author)
	assert.Equal(t, "comment", blocked.Comments[0].Body)
	assert.Equal(t, mustParseTime(t, "2026-05-01T11:45:00Z"), blocked.Comments[0].CreatedAt)
}

func TestNormalizeKataLabel(t *testing.T) {
	assert.Equal(t, "needs-review", normalizeKataLabel("Needs Review!"))
	assert.Equal(t, "imported", normalizeKataLabel("!!!"))
	assert.Equal(t, "source:beads", normalizeKataLabel("source:beads"))
	long := normalizeKataLabel(strings.Repeat("x", 100))
	assert.LessOrEqual(t, len(long), 64)
	assert.Regexp(t, `^[a-z0-9._:-]+$`, long)
}

func TestBeadsRejectsUnsupportedStatus(t *testing.T) {
	export := strings.NewReader(`{"id":"b1","title":"Bad","description":"body","status":"in_progress","created_at":"2026-05-01T10:00:00Z","created_by":"Alice","updated_at":"2026-05-01T10:00:00Z"}`)
	_, err := buildBeadsImportRequest(export, nil, "importer")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported beads status")
}

func TestBeadsRejectsDependencyTargetMissingFromExport(t *testing.T) {
	export := strings.NewReader(`{"id":"b2","title":"Blocked","description":"body","status":"open","created_at":"2026-05-01T10:00:00Z","created_by":"Alice","updated_at":"2026-05-01T10:00:00Z","dependencies":[{"issue_id":"b2","depends_on_id":"missing","type":"blocks"}]}`)
	_, err := buildBeadsImportRequest(export, nil, "importer")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dependency target")
}

func TestBeadsRejectsOversizedCommentsJSON(t *testing.T) {
	_, err := parseBeadsCommentsJSON(io.LimitReader(repeatedByteReader(' '), maxBeadsCommentsJSONBytes+1))
	require.Error(t, err)
	var ce *cliError
	require.True(t, errors.As(err, &ce), "expected validation cliError, got %T", err)
	assert.Equal(t, kindValidation, ce.Kind)
	assert.Equal(t, ExitValidation, ce.ExitCode)
	assert.Contains(t, err.Error(), "beads comments JSON exceeds")
}

type repeatedByteReader byte

func (r repeatedByteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, s)
	require.NoError(t, err)
	return ts
}
