package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
)

func TestReadDaemonConfig_Missing(t *testing.T) {
	t.Setenv("KATA_HOME", t.TempDir())
	cfg, err := config.ReadDaemonConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Empty(t, cfg.Listen)
}

func TestReadDaemonConfig_ReadsListen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"),
		[]byte(`listen = "100.64.0.5:7777"`+"\n"), 0o600))

	cfg, err := config.ReadDaemonConfig()
	require.NoError(t, err)
	assert.Equal(t, "100.64.0.5:7777", cfg.Listen)
}

func TestReadDaemonConfig_TrimsWhitespace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"),
		[]byte(`listen = "  127.0.0.1:7777  "`+"\n"), 0o600))

	cfg, err := config.ReadDaemonConfig()
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:7777", cfg.Listen)
}

func TestReadDaemonConfig_ReadsTUIMouse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"),
		[]byte("[tui]\nmouse = true\n"), 0o600))

	cfg, err := config.ReadDaemonConfig()
	require.NoError(t, err)
	assert.True(t, cfg.TUI.Mouse)
}

func TestReadDaemonConfig_RejectsMalformed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"),
		[]byte(`listen = `+"\n"), 0o600)) // unterminated

	_, err := config.ReadDaemonConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config.toml")
}
