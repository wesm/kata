package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
)

// setupTestHome creates an isolated KATA_HOME for the test and returns its
// path. Using t.TempDir keeps parallel tests from colliding on a shared dir.
func setupTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	return home
}

func TestKataHome_PrefersEnvOverDefault(t *testing.T) {
	home := setupTestHome(t)

	got, err := config.KataHome()
	require.NoError(t, err)
	assert.Equal(t, home, got)
}

func TestKataHome_DefaultsToUserHomeDotKata(t *testing.T) {
	t.Setenv("KATA_HOME", "")
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	got, err := config.KataHome()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".kata"), got)
}

func TestKataDB_PrefersEnvOverHomeJoin(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("KATA_DB", filepath.Join(home, "custom.db"))

	got, err := config.KataDB()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "custom.db"), got)
}

func TestKataDB_DefaultsToHomeKataDB(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("KATA_DB", "")

	got, err := config.KataDB()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "kata.db"), got)
}

func TestDBHash_StableTwelveLowerHex(t *testing.T) {
	a := config.DBHash("/Users/foo/.kata/kata.db")
	b := config.DBHash("/Users/foo/.kata/kata.db")
	c := config.DBHash("/Users/foo/.kata/other.db")

	assert.Len(t, a, 12)
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)
	assert.Equal(t, strings.ToLower(a), a)
}

func TestRuntimeDir_NamespaceIsDBHashUnderHome(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("KATA_DB", filepath.Join(home, "kata.db"))

	got, err := config.RuntimeDir()
	require.NoError(t, err)
	hash := config.DBHash(filepath.Join(home, "kata.db"))
	assert.Equal(t, filepath.Join(home, "runtime", hash), got)
}

const testDBHash = "abc123def456"

func TestHookConfigPath_HonorsKataHome(t *testing.T) {
	home := setupTestHome(t)

	got, err := config.HookConfigPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "hooks.toml"), got)
}

func TestHookRootDir_NamespacedByDBHash(t *testing.T) {
	home := setupTestHome(t)

	got, err := config.HookRootDir(testDBHash)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "hooks", testDBHash), got)
}

func TestHookOutputDir_UnderHookRoot(t *testing.T) {
	setupTestHome(t)

	got, err := config.HookOutputDir(testDBHash)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(got, filepath.Join("hooks", testDBHash, "output")),
		"HookOutputDir = %q, want suffix hooks/%s/output", got, testDBHash)
}

func TestHookRunsPath_UnderHookRoot(t *testing.T) {
	home := setupTestHome(t)

	got, err := config.HookRunsPath(testDBHash)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "hooks", testDBHash, "runs.jsonl"), got)
}

// TestHookRootDir_RejectsNonHash pins that path helpers refuse to join
// any string that isn't a 12-char lower-hex DBHash, so a corrupted state
// file or test typo can't escape <KataHome>/hooks via path traversal.
func TestHookRootDir_RejectsNonHash(t *testing.T) {
	setupTestHome(t)
	cases := []string{
		"",                   // empty
		"../escape",          // traversal
		"with/slash",         // separator
		"abc123def45",        // 11 chars
		"abc123def4567",      // 13 chars
		"ABC123DEF456",       // upper-case
		"abc123def45g",       // non-hex
		string([]byte{0, 1}), // control bytes
	}
	for _, c := range cases {
		_, err := config.HookRootDir(c)
		assert.Errorf(t, err, "HookRootDir(%q) should error", c)
		_, err = config.HookOutputDir(c)
		assert.Errorf(t, err, "HookOutputDir(%q) should error", c)
		_, err = config.HookRunsPath(c)
		assert.Errorf(t, err, "HookRunsPath(%q) should error", c)
	}
}
