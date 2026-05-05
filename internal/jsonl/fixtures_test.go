package jsonl_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
)

// validExportVersion is the standard export_version meta envelope tests
// prepend to bypass the decoder's initial version check.
const validExportVersion = `{"kind":"meta","data":{"key":"export_version","value":"1"}}`

// validV1ProjectRow is a canonical v1 project envelope with next_issue_number=1
// used by import tests that just need a referenceable parent project.
const validV1ProjectRow = `{"kind":"project","data":{"id":1,"identity":"github.com/wesm/kata","name":"kata","created_at":"2026-05-03T00:00:00.000Z","next_issue_number":1}}`

// buildJSONL joins envelope lines into valid JSONL: newline-separated with a
// trailing newline. Callers should not include trailing newlines in inputs.
func buildJSONL(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// importJSONL builds JSONL from lines and runs jsonl.Import against d,
// returning the import error so callers can assert success or failure.
func importJSONL(ctx context.Context, d *db.DB, lines ...string) error {
	return jsonl.Import(ctx, strings.NewReader(buildJSONL(lines...)), d)
}

// setupClosedTestDB seeds a database file at the current schema version and
// closes it, returning a context and the on-disk path. Tests that exercise
// path-based entry points (AutoCutover, PeekSchemaVersion) need a database
// file with no open handles.
func setupClosedTestDB(t *testing.T) (context.Context, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, d.Close())
	return ctx, path
}

// assertCurrentSchemaVersion fails the test unless the database at path
// reports the schema version baked into this binary.
func assertCurrentSchemaVersion(t *testing.T, path string) {
	t.Helper()
	assertSchemaVersion(t, path, db.CurrentSchemaVersion())
}

// assertSchemaVersion fails the test unless the database at path reports
// the expected schema version.
func assertSchemaVersion(t *testing.T, path string, expected int) {
	t.Helper()
	ver, err := db.PeekSchemaVersion(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, expected, ver, "schema version mismatch")
}

// writeLegacyDBFromFixture creates a SQLite database at path by executing the
// SQL in testdata/<fixtureName>.
func writeLegacyDBFromFixture(t *testing.T, path, fixtureName string) {
	t.Helper()
	schema, err := os.ReadFile(filepath.Join("testdata", fixtureName)) //nolint:gosec // testdata path is constant per call site
	require.NoError(t, err)
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()
	_, err = raw.Exec(string(schema))
	require.NoError(t, err)
}

func writeLegacyV1DB(t *testing.T, path string) {
	t.Helper()
	writeLegacyDBFromFixture(t, path, "legacy_v1.sql")
}

func writeLegacyV2DB(t *testing.T, path string) {
	t.Helper()
	writeLegacyDBFromFixture(t, path, "legacy_v2.sql")
}

func writeLegacyV3DB(t *testing.T, path string) {
	t.Helper()
	writeLegacyDBFromFixture(t, path, "legacy_v3.sql")
}

// openCutoverDB writes a legacy database at a fresh temp path using seedFn,
// runs jsonl.AutoCutover, and opens the upgraded database. The opened handle
// is closed via t.Cleanup.
func openCutoverDB(ctx context.Context, t *testing.T, seedFn func(*testing.T, string)) *db.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kata.db")
	seedFn(t, path)
	require.NoError(t, jsonl.AutoCutover(ctx, path))
	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// fetchInstanceUID returns the raw meta.instance_uid value stored in d.
func fetchInstanceUID(ctx context.Context, t *testing.T, d *db.DB) string {
	t.Helper()
	var s string
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='instance_uid'`).Scan(&s))
	return s
}

// exportToBuffer runs jsonl.Export with IncludeDeleted:true and returns the
// encoded bytes.
func exportToBuffer(ctx context.Context, t *testing.T, src *db.DB) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, src, &buf, jsonl.ExportOptions{IncludeDeleted: true}))
	return &buf
}

type richJSONLFixture struct {
	DB      *db.DB
	Project db.Project
}

// createTesterIssue creates an issue authored by "tester" with the given title,
// body, and labels. Used to cut the CreateIssue + require.NoError boilerplate
// from fixture builders that share the "tester" author convention.
func createTesterIssue(ctx context.Context, t *testing.T, d *db.DB, projectID int64, title, body string, labels ...string) db.Issue {
	t.Helper()
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: projectID,
		Title:     title,
		Body:      body,
		Author:    "tester",
		Labels:    labels,
	})
	require.NoError(t, err)
	return issue
}

// addTesterComment posts a comment on issueID authored by "tester".
func addTesterComment(ctx context.Context, t *testing.T, d *db.DB, issueID int64, body string) {
	t.Helper()
	_, _, err := d.CreateComment(ctx, db.CreateCommentParams{
		IssueID: issueID,
		Author:  "tester",
		Body:    body,
	})
	require.NoError(t, err)
}

// attachAlias attaches an alias to projectID, asserting no error. The kind
// argument lets callers choose "git", "local", etc.
func attachAlias(ctx context.Context, t *testing.T, d *db.DB, projectID int64, identity, kind, path string) {
	t.Helper()
	_, err := d.AttachAlias(ctx, projectID, identity, kind, path)
	require.NoError(t, err)
}

func buildRichJSONLFixture(t *testing.T) richJSONLFixture {
	t.Helper()
	ctx := context.Background()
	d := openExportTestDB(t)

	p1, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	p2, err := d.CreateProject(ctx, "github.com/wesm/other", "other")
	require.NoError(t, err)
	attachAlias(ctx, t, d, p1.ID, "github.com/wesm/kata", "git", "/tmp/kata")
	attachAlias(ctx, t, d, p1.ID, "kata-local", "local", "/work/kata")
	attachAlias(ctx, t, d, p2.ID, "github.com/wesm/other", "git", "/tmp/other")

	login := createTesterIssue(ctx, t, d, p1.ID, "orchid login regression", "Safari login fails after the orchid rollout", "bug", "frontend")
	blocker := createTesterIssue(ctx, t, d, p1.ID, "api blocker", "Backend response blocks login", "backend")
	softDeleted := createTesterIssue(ctx, t, d, p1.ID, "soft deleted keeps FTS", "deleted but still exportable")
	purged := createTesterIssue(ctx, t, d, p1.ID, "purged audit trail", "purged body should leave purge_log only", "audit")
	createTesterIssue(ctx, t, d, p2.ID, "other project orchid", "cross project export coverage")

	addTesterComment(ctx, t, d, login.ID, "watermelon comment text")
	addTesterComment(ctx, t, d, purged.ID, "purged comment text")

	_, _, err = d.CreateLinkAndEvent(ctx, db.CreateLinkParams{
		ProjectID:   p1.ID,
		FromIssueID: blocker.ID,
		ToIssueID:   login.ID,
		Type:        "blocks",
		Author:      "tester",
	}, db.LinkEventParams{
		EventType: "issue.linked", EventIssueID: blocker.ID, EventIssueNumber: blocker.Number,
		FromNumber: blocker.Number, ToNumber: login.Number, Actor: "tester",
	})
	require.NoError(t, err)
	_, _, _, err = d.SoftDeleteIssue(ctx, softDeleted.ID, "tester")
	require.NoError(t, err)
	reason := "roundtrip fixture"
	pl, err := d.PurgeIssue(ctx, purged.ID, "tester", &reason)
	require.NoError(t, err)
	require.NotNil(t, pl.PurgeResetAfterEventID)

	return richJSONLFixture{DB: d, Project: p1}
}
