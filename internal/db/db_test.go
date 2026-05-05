package db_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/uid"
	_ "modernc.org/sqlite"
)

func TestOpen_AppliesPragmasAndMigrations(t *testing.T) {
	d := openTestDB(t)

	var fk int
	require.NoError(t, d.QueryRow("PRAGMA foreign_keys").Scan(&fk))
	assert.Equal(t, 1, fk)

	var mode string
	require.NoError(t, d.QueryRow("PRAGMA journal_mode").Scan(&mode))
	assert.Equal(t, "wal", mode)

	assertSchemaVersion(t, d, db.CurrentSchemaVersion())
}

func TestOpen_RecordsCurrentSchemaVersion(t *testing.T) {
	assert.Equal(t, 4, db.CurrentSchemaVersion())
	d := openTestDB(t)
	assertSchemaVersion(t, d, db.CurrentSchemaVersion())
}

func TestOpen_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kata.db")
	d1, err := db.Open(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, d1.Close())

	d2, err := db.Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d2.Close() })

	assertSchemaVersion(t, d2, db.CurrentSchemaVersion())
}

func TestOpen_RejectsOlderSchemaNeedingJSONLCutover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kata.db")
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO meta(key, value) VALUES('schema_version', '1')`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	_, err = db.Open(context.Background(), path)

	require.Error(t, err)
	assert.True(t, errors.Is(err, db.ErrSchemaCutoverRequired), err)
}

func TestOpen_TimestampColumnsScanIntoTime(t *testing.T) {
	d := openTestDB(t)

	projectUID, err := uid.New()
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO projects(uid, identity, name) VALUES(?,'x','x')`, projectUID)
	require.NoError(t, err)

	rows, err := d.Query(`SELECT created_at FROM projects`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	require.True(t, rows.Next())
	var ts interface{}
	require.NoError(t, rows.Scan(&ts))
	// modernc.org/sqlite returns time.Time for DATETIME columns
	_, ok := ts.(interface{ Year() int })
	assert.True(t, ok, "expected time.Time, got %T", ts)
}
