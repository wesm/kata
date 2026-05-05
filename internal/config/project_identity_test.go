package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
	"github.com/wesm/kata/internal/testfix"
)

func TestDiscoverPaths_FindsKataTomlAndGit(t *testing.T) {
	root := t.TempDir()
	testfix.MkDotGit(t, root)
	testfix.WriteKataToml(t, root, "x", "x")
	sub := filepath.Join(root, "a", "b")
	require.NoError(t, os.MkdirAll(sub, 0o755)) //nolint:gosec // test fixture under TempDir.

	d, err := config.DiscoverPaths(sub)
	require.NoError(t, err)
	assert.Equal(t, root, d.WorkspaceRoot)
	assert.Equal(t, root, d.GitRoot)
}

func TestDiscoverPaths_KataTomlInSubdirOfGit(t *testing.T) {
	root := t.TempDir()
	testfix.MkDotGit(t, root)
	sub := filepath.Join(root, "subproject")
	require.NoError(t, os.MkdirAll(sub, 0o755)) //nolint:gosec // test fixture under TempDir.
	testfix.WriteKataToml(t, sub, "x", "x")

	d, err := config.DiscoverPaths(sub)
	require.NoError(t, err)
	assert.Equal(t, sub, d.WorkspaceRoot)
	assert.Equal(t, root, d.GitRoot)
}

func TestDiscoverPaths_NeitherFound(t *testing.T) {
	d, err := config.DiscoverPaths(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, d.WorkspaceRoot)
	assert.Empty(t, d.GitRoot)
}

func TestDiscoverPaths_StartPathMissingErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist", "deeper")
	_, err := config.DiscoverPaths(missing)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stat")
}

func TestDiscoverPaths_StartPathIsFileWalksFromParent(t *testing.T) {
	root := t.TempDir()
	testfix.MkDotGit(t, root)
	testfix.WriteKataToml(t, root, "x", "x")
	filePath := filepath.Join(root, "README.md")
	require.NoError(t, os.WriteFile(filePath, []byte("hi"), 0o644)) //nolint:gosec // test fixture

	d, err := config.DiscoverPaths(filePath)
	require.NoError(t, err)
	assert.Equal(t, root, d.WorkspaceRoot)
	assert.Equal(t, root, d.GitRoot)
}

func TestNormalizeRemoteURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/wesm/kata.git", "github.com/wesm/kata"},
		{"https://github.com/wesm/kata", "github.com/wesm/kata"},
		{"https://user:pass@github.com/wesm/kata.git", "github.com/wesm/kata"},
		{"git@github.com:wesm/kata.git", "github.com/wesm/kata"},
		{"ssh://git@gitlab.com/team/repo.git", "gitlab.com/team/repo"},
	}
	for _, tc := range cases {
		got, err := config.NormalizeRemoteURL(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}
}

func TestComputeAliasIdentity_GitWithRemote(t *testing.T) {
	dir := testfix.InitGitRepo(t)
	testfix.RunGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	a, err := config.ComputeAliasIdentity(config.DiscoveredPaths{GitRoot: dir})
	require.NoError(t, err)
	assert.Equal(t, "github.com/wesm/kata", a.Identity)
	assert.Equal(t, "git", a.Kind)
	assert.Equal(t, dir, a.RootPath)
}

func TestComputeAliasIdentity_GitNoRemote(t *testing.T) {
	dir := testfix.InitGitRepo(t)

	a, err := config.ComputeAliasIdentity(config.DiscoveredPaths{GitRoot: dir})
	require.NoError(t, err)
	assert.Equal(t, "local://"+dir, a.Identity)
	assert.Equal(t, "local", a.Kind)
}

func TestComputeAliasIdentity_NonGitWorkspace(t *testing.T) {
	ws := t.TempDir()
	a, err := config.ComputeAliasIdentity(config.DiscoveredPaths{WorkspaceRoot: ws})
	require.NoError(t, err)
	assert.Equal(t, "local://"+ws, a.Identity)
	assert.Equal(t, "local", a.Kind)
	assert.Equal(t, ws, a.RootPath)
}

func TestComputeAliasIdentity_Neither(t *testing.T) {
	_, err := config.ComputeAliasIdentity(config.DiscoveredPaths{})
	require.Error(t, err)
}

func TestValidateIdentity(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		hint string
	}{
		{"github.com/wesm/kata", true, ""},
		{"local:///abs/path", true, ""},
		{"a_b.c-d:foo/bar", true, ""},
		{"", false, "non-empty"},
		{"  spaces in middle  ", false, "whitespace"},
		{"has space", false, "whitespace"},
		{"https://u:p@host/x", false, "credential"},
	}
	for _, tc := range cases {
		err := config.ValidateIdentity(tc.in)
		if tc.ok {
			assert.NoError(t, err, tc.in)
		} else {
			require.Error(t, err, tc.in)
			assert.Contains(t, err.Error(), tc.hint, tc.in)
		}
	}
}

func TestPickInitIdentity_KataTomlOnly(t *testing.T) {
	cfg := &config.ProjectConfig{}
	cfg.Project.Identity = "github.com/wesm/kata"
	cfg.Project.Name = "kata"

	got, err := config.PickInitIdentity(config.DiscoveredPaths{}, cfg, "", "", false)
	require.NoError(t, err)
	assert.Equal(t, "github.com/wesm/kata", got.Identity)
	assert.Equal(t, "kata", got.Name)
}

func TestPickInitIdentity_KataTomlMatchingInputIdentity(t *testing.T) {
	cfg := &config.ProjectConfig{}
	cfg.Project.Identity = "github.com/wesm/kata"
	cfg.Project.Name = "kata"

	got, err := config.PickInitIdentity(config.DiscoveredPaths{}, cfg,
		"github.com/wesm/kata", "", false)
	require.NoError(t, err)
	assert.Equal(t, "github.com/wesm/kata", got.Identity)
	assert.Equal(t, "kata", got.Name)
}

func TestPickInitIdentity_KataTomlConflictWithoutReplace(t *testing.T) {
	cfg := &config.ProjectConfig{}
	cfg.Project.Identity = "github.com/wesm/kata"
	cfg.Project.Name = "kata"

	_, err := config.PickInitIdentity(config.DiscoveredPaths{}, cfg,
		"github.com/wesm/other", "", false)
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrIdentityConflict)
}

func TestPickInitIdentity_KataTomlConflictWithReplace(t *testing.T) {
	cfg := &config.ProjectConfig{}
	cfg.Project.Identity = "github.com/wesm/kata"
	cfg.Project.Name = "kata"

	got, err := config.PickInitIdentity(config.DiscoveredPaths{}, cfg,
		"github.com/wesm/other", "", true)
	require.NoError(t, err)
	assert.Equal(t, "github.com/wesm/other", got.Identity)
	assert.Equal(t, "other", got.Name)
}

func TestPickInitIdentity_InputIdentityWithExplicitName(t *testing.T) {
	got, err := config.PickInitIdentity(config.DiscoveredPaths{}, nil,
		"github.com/wesm/kata", "custom", false)
	require.NoError(t, err)
	assert.Equal(t, "github.com/wesm/kata", got.Identity)
	assert.Equal(t, "custom", got.Name)
}

func TestPickInitIdentity_FromGitRoot(t *testing.T) {
	dir := testfix.InitGitRepo(t)
	testfix.RunGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	got, err := config.PickInitIdentity(
		config.DiscoveredPaths{GitRoot: dir}, nil, "", "", false)
	require.NoError(t, err)
	assert.Equal(t, "github.com/wesm/kata", got.Identity)
	assert.Equal(t, "kata", got.Name)
}

func TestPickInitIdentity_NoSource(t *testing.T) {
	_, err := config.PickInitIdentity(config.DiscoveredPaths{}, nil, "", "", false)
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrNoIdentitySource)
}

func TestPickInitIdentity_KataTomlEmptyName(t *testing.T) {
	cfg := &config.ProjectConfig{}
	cfg.Project.Identity = "github.com/wesm/kata"
	cfg.Project.Name = ""

	got, err := config.PickInitIdentity(config.DiscoveredPaths{}, cfg, "", "", false)
	require.NoError(t, err)
	assert.Equal(t, "github.com/wesm/kata", got.Identity)
	assert.Equal(t, "kata", got.Name)
}

func TestValidateAliasInfo(t *testing.T) {
	cases := []struct {
		name string
		info config.AliasInfo
		ok   bool
		hint string
	}{
		{
			name: "git alias passes ValidateIdentity rules",
			info: config.AliasInfo{Identity: "github.com/wesm/kata", Kind: "git", RootPath: "/repo"},
			ok:   true,
		},
		{
			name: "local alias with spaces in path is allowed",
			info: config.AliasInfo{Identity: "local:///Users/me/My Project", Kind: "local", RootPath: "/Users/me/My Project"},
			ok:   true,
		},
		{
			name: "local alias with unicode is allowed",
			info: config.AliasInfo{Identity: "local:///私の/プロジェクト", Kind: "local", RootPath: "/私の/プロジェクト"},
			ok:   true,
		},
		{
			name: "git alias with whitespace is rejected",
			info: config.AliasInfo{Identity: "has space", Kind: "git", RootPath: "/repo"},
			ok:   false,
			hint: "whitespace",
		},
		{
			name: "unknown kind is rejected",
			info: config.AliasInfo{Identity: "github.com/wesm/kata", Kind: "bogus", RootPath: "/repo"},
			ok:   false,
			hint: "kind",
		},
		{
			name: "empty kind is rejected",
			info: config.AliasInfo{Identity: "github.com/wesm/kata", Kind: "", RootPath: "/repo"},
			ok:   false,
			hint: "kind",
		},
		{
			name: "empty root_path is rejected",
			info: config.AliasInfo{Identity: "github.com/wesm/kata", Kind: "git", RootPath: ""},
			ok:   false,
			hint: "root_path",
		},
		{
			name: "empty identity is rejected",
			info: config.AliasInfo{Identity: "", Kind: "git", RootPath: "/repo"},
			ok:   false,
			hint: "identity",
		},
		{
			name: "local alias missing prefix is rejected",
			info: config.AliasInfo{Identity: "/Users/me/proj", Kind: "local", RootPath: "/Users/me/proj"},
			ok:   false,
			hint: "local://",
		},
		{
			name: "local alias bare prefix is rejected",
			info: config.AliasInfo{Identity: "local://", Kind: "local", RootPath: "/Users/me/proj"},
			ok:   false,
			hint: "local://",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := config.ValidateAliasInfo(tc.info)
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.hint)
		})
	}
}
