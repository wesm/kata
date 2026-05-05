package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestExportWritesJSONLToOutput(t *testing.T) {
	home := setupKataEnv(t)
	dbPath := filepath.Join(home, "kata.db")
	d, err := db.Open(context.Background(), dbPath)
	require.NoError(t, err)
	p, err := d.CreateProject(context.Background(), "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(context.Background(), db.CreateIssueParams{
		ProjectID: p.ID,
		Title:     "exported issue",
		Author:    "tester",
	})
	require.NoError(t, err)
	require.NoError(t, d.Close())

	outPath := filepath.Join(home, "export.jsonl")
	out, err := runCmdOutput(t, nil, "export", "--output", outPath)
	require.NoError(t, err)

	bs, err := os.ReadFile(outPath) //nolint:gosec // test fixture under TempDir
	require.NoError(t, err)
	assert.Contains(t, string(bs), `"kind":"meta"`)
	assert.Contains(t, string(bs), "exported issue")
	assert.Contains(t, out, outPath)
}

func TestExportRefusesRunningDaemonUnlessAllowed(t *testing.T) {
	home := setupKataEnv(t)
	dbPath := filepath.Join(home, "kata.db")
	d, err := db.Open(context.Background(), dbPath)
	require.NoError(t, err)
	require.NoError(t, d.Close())
	addr, cleanup := pipeServer(t)
	t.Cleanup(cleanup)
	require.NoError(t, writeRuntimeFor(home, addr))

	_, err = runCmdOutput(t, nil, "export", "--output", filepath.Join(home, "export.jsonl"))
	ce := requireCLIError(t, err, ExitValidation)
	assert.Contains(t, ce.Message, "daemon is running")
}
