package jsonl_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

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

	// Build a v3-shape DB: open at current schema, then strip deleted_at
	// and roll meta.schema_version back to 3.
	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	_, err = d.ExecContext(ctx, `INSERT INTO projects(id, uid, identity, name, next_issue_number) VALUES(1,'01HZZZZZZZZZZZZZZZZZZZZ001','proj-a','Proj A',1)`)
	require.NoError(t, err)
	require.NoError(t, d.Close())

	raw, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	for _, s := range []string{
		`DROP INDEX IF EXISTS idx_projects_active`,
		`CREATE TABLE projects_v3(id INTEGER PRIMARY KEY, uid TEXT, identity TEXT, name TEXT, created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')), next_issue_number INTEGER NOT NULL DEFAULT 1)`,
		`INSERT INTO projects_v3 SELECT id,uid,identity,name,created_at,next_issue_number FROM projects`,
		`DROP TABLE projects`,
		`ALTER TABLE projects_v3 RENAME TO projects`,
		`UPDATE meta SET value='3' WHERE key='schema_version'`,
	} {
		_, err := raw.ExecContext(ctx, s)
		require.NoError(t, err, s)
	}
	require.NoError(t, raw.Close())

	v, err := db.PeekSchemaVersion(ctx, path)
	require.NoError(t, err)
	require.Equal(t, 3, v, "precondition: DB should report v3")

	require.NoError(t, jsonl.AutoCutover(ctx, path))

	v2, err := db.PeekSchemaVersion(ctx, path)
	require.NoError(t, err)
	assert.Equal(t, 4, v2, "schema should upgrade to v4")

	d2, err := db.Open(ctx, path)
	require.NoError(t, err)
	defer d2.Close()

	var id int64
	var identity string
	var deletedAt sql.NullString
	require.NoError(t, d2.QueryRowContext(ctx, `SELECT id, identity, deleted_at FROM projects WHERE id=1`).Scan(&id, &identity, &deletedAt))
	assert.Equal(t, int64(1), id)
	assert.Equal(t, "proj-a", identity)
	assert.False(t, deletedAt.Valid, "deleted_at should be NULL for survived project")
}
