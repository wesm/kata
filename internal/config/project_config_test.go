package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
)

func setupKataProjectDir(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".kata.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644)) //nolint:gosec // test fixture matches production .kata.toml mode
	return dir
}

func writeAndReadConfig(t *testing.T, identity, name string) *config.ProjectConfig {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, config.WriteProjectConfig(dir, identity, name))
	cfg, err := config.ReadProjectConfig(dir)
	require.NoError(t, err)
	return cfg
}

func TestReadProjectConfig_Roundtrip(t *testing.T) {
	dir := setupKataProjectDir(t, `version = 1

[project]
identity = "github.com/wesm/kata"
name     = "kata"
`)

	cfg, err := config.ReadProjectConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.Version)
	assert.Equal(t, "github.com/wesm/kata", cfg.Project.Identity)
	assert.Equal(t, "kata", cfg.Project.Name)
}

func TestReadProjectConfig_Missing(t *testing.T) {
	cfg, err := config.ReadProjectConfig(t.TempDir())
	assert.Nil(t, cfg)
	assert.ErrorIs(t, err, config.ErrProjectConfigMissing)
}

func TestReadProjectConfig_RejectsBadVersion(t *testing.T) {
	dir := setupKataProjectDir(t, `version = 2

[project]
identity = "x"
name = "y"
`)

	_, err := config.ReadProjectConfig(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported .kata.toml version")
}

func TestReadProjectConfig_RejectsBlankIdentity(t *testing.T) {
	dir := setupKataProjectDir(t, `version = 1

[project]
identity = "   "
name = "x"
`)

	_, err := config.ReadProjectConfig(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project.identity")
}

func TestWriteProjectConfig_DerivesNameFromLastSegment(t *testing.T) {
	cfg := writeAndReadConfig(t, "github.com/wesm/kata", "")
	assert.Equal(t, "kata", cfg.Project.Name)
}

func TestWriteProjectConfig_PreservesExplicitName(t *testing.T) {
	cfg := writeAndReadConfig(t, "github.com/wesm/kata", "Kata Tracker")
	assert.Equal(t, "Kata Tracker", cfg.Project.Name)
}

func TestReadProjectConfig_AcceptsOptionalServerBlock(t *testing.T) {
	dir := setupKataProjectDir(t, `version = 1

[project]
identity = "github.com/wesm/kata"
name     = "kata"

[server]
url = "http://127.0.0.1:7777"
`)
	cfg, err := config.ReadProjectConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:7777", cfg.Server.URL)
}

func TestReadProjectConfig_NoServerBlockYieldsZeroValue(t *testing.T) {
	dir := setupKataProjectDir(t, `version = 1

[project]
identity = "github.com/wesm/kata"
name     = "kata"
`)
	cfg, err := config.ReadProjectConfig(dir)
	require.NoError(t, err)
	assert.Empty(t, cfg.Server.URL)
}

func TestFindProjectConfig_FromSubdirectory(t *testing.T) {
	root := setupKataProjectDir(t, `version = 1

[project]
identity = "github.com/wesm/kata"
name     = "kata"
`)
	sub := filepath.Join(root, "internal", "tui")
	require.NoError(t, os.MkdirAll(sub, 0o755)) //nolint:gosec // test fixture under TempDir

	cfg, foundDir, err := config.FindProjectConfig(sub)
	require.NoError(t, err)
	assert.Equal(t, root, foundDir)
	assert.Equal(t, "github.com/wesm/kata", cfg.Project.Identity)
}

func TestFindProjectConfig_FromExactDir(t *testing.T) {
	root := setupKataProjectDir(t, `version = 1

[project]
identity = "github.com/wesm/kata"
`)
	cfg, foundDir, err := config.FindProjectConfig(root)
	require.NoError(t, err)
	assert.Equal(t, root, foundDir)
	assert.Equal(t, "github.com/wesm/kata", cfg.Project.Identity)
}

func TestFindProjectConfig_MissingReturnsSentinel(t *testing.T) {
	root := t.TempDir()
	cfg, foundDir, err := config.FindProjectConfig(root)
	assert.Nil(t, cfg)
	assert.Empty(t, foundDir)
	assert.ErrorIs(t, err, config.ErrProjectConfigMissing)
}

func TestFindProjectConfig_PropagatesParseError(t *testing.T) {
	root := setupKataProjectDir(t, "this is not toml = = =")
	cfg, foundDir, err := config.FindProjectConfig(root)
	assert.Nil(t, cfg)
	assert.Empty(t, foundDir)
	require.Error(t, err)
	assert.NotErrorIs(t, err, config.ErrProjectConfigMissing)
}
