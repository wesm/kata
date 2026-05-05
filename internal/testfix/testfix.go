// Package testfix collects small filesystem and git fixtures shared across
// test packages: writing a stock .kata.toml, faking a .git directory, and
// running real git for tests that need a working repository.
package testfix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// WriteKataToml writes a minimal v1 .kata.toml under dir with the given
// identity and name. The world-readable file mode mirrors how users commit
// .kata.toml in real projects.
func WriteKataToml(t *testing.T, dir, identity, name string) {
	t.Helper()
	body := fmt.Sprintf("version = 1\n\n[project]\nidentity = %q\nname     = %q\n", identity, name)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.toml"), []byte(body), 0o644)) //nolint:gosec // test fixture mirrors production .kata.toml mode
}

// MkDotGit creates an empty .git directory under dir so that walk-up
// discovery code can detect a git workspace without a real repository.
func MkDotGit(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755)) //nolint:gosec // test fixture under TempDir
}

// InitGitRepo creates a fresh temp directory, runs git init, and configures
// a deterministic author so commit-producing tests don't depend on the host
// git config. Returns the repo path.
func InitGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	RunGit(t, dir, "init", "--quiet")
	RunGit(t, dir, "config", "user.email", "x@example.com")
	RunGit(t, dir, "config", "user.name", "x")
	return dir
}

// RunGit invokes the system git binary inside dir with args and fails the
// test, surfacing combined output, if the command errors.
func RunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	//nolint:gosec // git binary is fixed; args are test-supplied subcommand flags.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}
