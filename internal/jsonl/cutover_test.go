package jsonl_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
	"github.com/wesm/kata/internal/uid"
	_ "modernc.org/sqlite"
)

func TestAutoCutoverNoopsAtCurrentSchema(t *testing.T) {
	ctx, path := setupClosedTestDB(t)

	require.NoError(t, jsonl.AutoCutover(ctx, path))

	assertCurrentSchemaVersion(t, path)
	assertNoCutoverTemps(t, path)
}

func TestAutoCutoverRefusesExistingTempFiles(t *testing.T) {
	ctx, path := setupClosedTestDB(t)
	require.NoError(t, os.WriteFile(path+".import.tmp.jsonl", []byte("partial"), 0o600))

	err := jsonl.AutoCutover(ctx, path)

	require.Error(t, err)
	assert.True(t, errors.Is(err, jsonl.ErrCutoverInProgress))
}

func TestAutoCutoverFailureLeavesSourceAndRemovesTemps(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	writeVersionZeroDB(t, path)
	before, err := os.ReadFile(path) //nolint:gosec // test fixture under TempDir
	require.NoError(t, err)

	err = jsonl.AutoCutover(ctx, path)

	require.Error(t, err)
	after, readErr := os.ReadFile(path) //nolint:gosec // test fixture under TempDir
	require.NoError(t, readErr)
	assert.Equal(t, before, after)
	assertNoCutoverTemps(t, path)
}

func TestAutoCutoverUpgradesLegacyV1DB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	writeLegacyV1DB(t, path)

	require.NoError(t, jsonl.AutoCutover(ctx, path))

	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	assertCurrentSchemaVersion(t, path)
	var projectUID, issueUID, eventIssueUID string
	require.NoError(t, d.QueryRow(`SELECT uid FROM projects WHERE id = 1`).Scan(&projectUID))
	require.NoError(t, d.QueryRow(`SELECT uid FROM issues WHERE id = 1`).Scan(&issueUID))
	require.NoError(t, d.QueryRow(`SELECT issue_uid FROM events WHERE id = 1`).Scan(&eventIssueUID))
	assert.True(t, uid.Valid(projectUID))
	assert.True(t, uid.Valid(issueUID))
	assert.Equal(t, issueUID, eventIssueUID)
	assertNoCutoverTemps(t, path)
}

func TestPeekSchemaVersion(t *testing.T) {
	ctx, path := setupClosedTestDB(t)
	assertCurrentSchemaVersion(t, path)

	noMeta := filepath.Join(t.TempDir(), "empty.db")
	raw, err := sql.Open("sqlite", noMeta)
	require.NoError(t, err)
	require.NoError(t, raw.PingContext(ctx))
	require.NoError(t, raw.Close())
	ver, err := db.PeekSchemaVersion(ctx, noMeta)
	require.NoError(t, err)
	assert.Equal(t, 0, ver)
}

// TestRoundtripV4PreservesDeletedAt covers #24's projects.deleted_at column:
// an archived project in the source DB round-trips through JSONL with the
// timestamp intact, so kata-instance backups don't silently un-archive
// projects on restore.
func TestRoundtripV4PreservesDeletedAt(t *testing.T) {
	ctx := context.Background()
	src := openExportTestDB(t)
	p, err := src.CreateProject(ctx, "github.com/wesm/proj-archived", "archived")
	require.NoError(t, err)
	_, err = src.ExecContext(ctx,
		`UPDATE projects SET deleted_at = '2026-05-04T12:00:00.000Z' WHERE id = ?`, p.ID)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, src, &buf, jsonl.ExportOptions{IncludeDeleted: true}))

	dstPath := filepath.Join(t.TempDir(), "dst.db")
	dst, err := db.Open(ctx, dstPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dst.Close() })
	require.NoError(t, jsonl.Import(ctx, bytes.NewReader(buf.Bytes()), dst))

	var got sql.NullTime
	require.NoError(t, dst.QueryRowContext(ctx,
		`SELECT deleted_at FROM projects WHERE id = ?`, p.ID).Scan(&got))
	require.True(t, got.Valid, "deleted_at must round-trip as a non-null timestamp")
	assert.Equal(t, "2026-05-04T12:00:00Z", got.Time.UTC().Format("2006-01-02T15:04:05Z"))
}

func writeVersionZeroDB(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	_, err = d.ExecContext(ctx, `UPDATE meta SET value='0' WHERE key='schema_version'`)
	require.NoError(t, err)
	require.NoError(t, d.Close())
}

func assertNoCutoverTemps(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{".import.tmp.jsonl", ".import.tmp.db"} {
		_, err := os.Stat(path + suffix)
		assert.True(t, os.IsNotExist(err), path+suffix)
	}
}
