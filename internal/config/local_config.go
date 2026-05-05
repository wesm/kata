package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// LocalConfigFilename is the per-developer override file. Gitignored.
const LocalConfigFilename = ".kata.local.toml"

// ErrLocalConfigMissing is returned by ReadLocalConfig when the file
// is absent. Other I/O and parse errors are returned as-is.
var ErrLocalConfigMissing = errors.New(".kata.local.toml not found")

// ReadLocalConfig parses <workspaceRoot>/.kata.local.toml and validates
// version == 1. Unlike ReadProjectConfig, [project] is optional — a
// developer may set only [server]. Empty [server].url is treated as
// the zero value.
func ReadLocalConfig(workspaceRoot string) (*ProjectConfig, error) {
	path := filepath.Join(workspaceRoot, LocalConfigFilename)
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrLocalConfigMissing
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg ProjectConfig
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Version != 1 {
		return nil, fmt.Errorf("unsupported .kata.local.toml version %d (expected 1)", cfg.Version)
	}
	cfg.Project.Identity = strings.TrimSpace(cfg.Project.Identity)
	cfg.Project.Name = strings.TrimSpace(cfg.Project.Name)
	cfg.Server.URL = strings.TrimSpace(cfg.Server.URL)
	return &cfg, nil
}

// MergeLocal overlays non-empty fields from local onto a copy of base.
// Pass nil for "no local file" — base is returned unchanged. Identity
// from .kata.toml is canonical: a divergent local identity is ignored
// with a one-line warning to stderr (use MergeLocalWithStderr in tests).
func MergeLocal(base, local *ProjectConfig) *ProjectConfig {
	return MergeLocalWithStderr(base, local, os.Stderr)
}

// MergeLocalWithStderr is MergeLocal with an explicit warning sink so
// tests can capture the divergent-identity warning.
func MergeLocalWithStderr(base, local *ProjectConfig, stderr io.Writer) *ProjectConfig {
	merged := *base
	if local == nil {
		return &merged
	}
	if local.Project.Identity != "" && local.Project.Identity != base.Project.Identity {
		_, _ = fmt.Fprintf(stderr, "kata: ignoring divergent project.identity %q in .kata.local.toml (canonical is %q in .kata.toml)\n",
			local.Project.Identity, base.Project.Identity)
	}
	if local.Project.Name != "" {
		merged.Project.Name = local.Project.Name
	}
	if local.Server.URL != "" {
		merged.Server.URL = local.Server.URL
	}
	return &merged
}
