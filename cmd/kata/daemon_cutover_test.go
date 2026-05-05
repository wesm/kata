package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestDaemonStartRunsAutoCutoverBeforeOpen(t *testing.T) {
	dbPath := filepath.Join(setupKataEnv(t), "kata.db")
	d, err := db.Open(context.Background(), dbPath)
	require.NoError(t, err)
	_, err = d.ExecContext(context.Background(), `UPDATE meta SET value='0' WHERE key='schema_version'`)
	require.NoError(t, err)
	require.NoError(t, d.Close())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = runDaemon(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "export_version")
	ver, peekErr := db.PeekSchemaVersion(context.Background(), dbPath)
	require.NoError(t, peekErr)
	assert.Equal(t, 0, ver)
}
