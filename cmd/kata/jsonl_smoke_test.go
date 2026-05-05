package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wesm/kata/internal/testenv"
)

func TestSmoke_ExportImport(t *testing.T) {
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")

	runCLI(t, env, dir, "create", "export smoke one", "--body", "orchid body", "--label", "bug")
	runCLI(t, env, dir, "create", "export smoke two", "--body", "blocks first", "--blocks", "1")
	runCLI(t, env, dir, "comment", "1", "--body", "watermelon note")
	runCLI(t, env, dir, "label", "add", "2", "backend")

	before := runCLI(t, env, dir, "--json", "list")
	beforeShow := runCLI(t, env, dir, "--json", "show", "1")
	assert.Contains(t, beforeShow, "watermelon note")
	exportPath := filepath.Join(env.Home, "smoke.jsonl")
	runCLI(t, env, dir, "export", "--output", exportPath)
	targetPath := filepath.Join(t.TempDir(), "imported.db")
	runCLI(t, env, dir, "import", "--input", exportPath, "--target", targetPath)

	imported := testenv.NewFromDB(t, targetPath)
	after := runCLI(t, imported, dir, "--json", "list")
	afterShow := runCLI(t, imported, dir, "--json", "show", "1")

	assert.JSONEq(t, before, after)
	assert.JSONEq(t, beforeShow, afterShow)
}
