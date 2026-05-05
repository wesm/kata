package jsonl_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
)

// TestV3ToV4CutoverPreservesProjects exercises the user-facing upgrade
// path: build a DB at the v3 schema, run AutoCutover, and verify
// existing projects survive with deleted_at NULL.
func TestV3ToV4CutoverPreservesProjects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	ctx := context.Background()

	writeLegacyV3DB(t, path)
	assertSchemaVersion(t, path, 3)

	require.NoError(t, jsonl.AutoCutover(ctx, path))

	assertCurrentSchemaVersion(t, path)

	d2, err := db.Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d2.Close() })

	var id int64
	var identity string
	var deletedAt sql.NullString
	require.NoError(t, d2.QueryRowContext(ctx, `SELECT id, identity, deleted_at FROM projects WHERE id=1`).Scan(&id, &identity, &deletedAt))
	assert.Equal(t, int64(1), id)
	assert.Equal(t, "proj-a", identity)
	assert.False(t, deletedAt.Valid, "deleted_at should be NULL for survived project")
}
