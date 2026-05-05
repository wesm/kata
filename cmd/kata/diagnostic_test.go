package main

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/testenv"
)

func TestWhoami_FlagOverride(t *testing.T) {
	out := requireCmdOutput(t, nil, "whoami", "--as", "claude-4.7")
	assert.Contains(t, out, "claude-4.7")
	assert.Contains(t, out, "flag")
}

func TestHealth_PrintsSchemaVersion(t *testing.T) {
	env := testenv.New(t)
	out := requireCmdOutput(t, env, "health")
	assert.Contains(t, out, "schema_version="+strconv.Itoa(db.CurrentSchemaVersion()))
}

func TestProjectsList_PrintsKnown(t *testing.T) {
	env := testenv.New(t)
	_ = initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")

	out := requireCmdOutput(t, env, "projects", "list")
	assert.True(t, strings.Contains(out, "github.com/wesm/kata"))
}

func TestProjectsRename_RenamesProject(t *testing.T) {
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	projectID := resolvePIDViaHTTP(t, env.URL, dir)

	out := requireCmdOutput(t, env, "projects", "rename", itoa(projectID), "Kata Tracker")
	assert.Contains(t, out, "renamed project #"+itoa(projectID)+" to Kata Tracker")

	show := requireCmdOutput(t, env, "projects", "show", itoa(projectID))
	assert.Contains(t, show, "(Kata Tracker, next #")
}

func TestProjectsRename_AcceptsProjectSelector(t *testing.T) {
	env := testenv.New(t)
	_ = initBoundWorkspace(t, env.URL, "https://github.com/wesm/kenn.git")

	out := requireCmdOutput(t, env, "projects", "rename", "kenn", "steward")
	assert.Contains(t, out, "renamed project #")
	assert.Contains(t, out, "to steward")
}

func TestProjectsMerge_MergesSourceSelectorIntoSurvivingTarget(t *testing.T) {
	env := testenv.New(t)
	kenn, steward := setupMergeProjects(t, env)
	_, _, err := env.DB.CreateIssue(context.Background(), db.CreateIssueParams{
		ProjectID: kenn.ID, Title: "carry history forward", Author: "tester",
	})
	require.NoError(t, err)

	out := requireCmdOutput(t, env, "projects", "merge", "kenn", "steward")
	assert.Contains(t, out, "merged project #"+itoa(kenn.ID)+" into #"+itoa(steward.ID))
	assert.Contains(t, out, "moved 1 issue")

	issue, err := env.DB.IssueByNumber(context.Background(), steward.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, "carry history forward", issue.Title)
	_, err = env.DB.ProjectByID(context.Background(), kenn.ID)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestProjectsMerge_RewritesWorkspaceBindingFromSourceToTarget(t *testing.T) {
	env := testenv.New(t)
	setupMergeProjects(t, env)

	dir := t.TempDir()
	require.NoError(t, config.WriteProjectConfig(dir, "github.com/wesm/kenn", "steward"))

	_ = requireCmdOutput(t, env, "--workspace", dir, "projects", "merge", "kenn", "steward")

	cfg, err := config.ReadProjectConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, "github.com/wesm/steward", cfg.Project.Identity)
	assert.Equal(t, "steward", cfg.Project.Name)
}

func TestProjectsRename_RejectsBlankName(t *testing.T) {
	_, err := runCmdOutput(t, nil, "projects", "rename", "1", "   ")
	ce := requireCLIError(t, err, ExitValidation)
	assert.Contains(t, ce.Message, "project name must be non-empty")
}

func TestPluralCount_PluralizesAlias(t *testing.T) {
	assert.Equal(t, "1 alias", pluralCount(1, "alias"))
	assert.Equal(t, "2 aliases", pluralCount(2, "alias"))
}
