package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreate_WithPriority(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)

	out := runCLI(t, env, dir, "create", "p1 issue", "--priority", "1")
	assert.Contains(t, out, "p1 issue")

	b := fetchIssueViaHTTP(t, env, pid, 1)
	require.NotNil(t, b.Issue.Priority)
	assert.Equal(t, int64(1), *b.Issue.Priority)
}

func TestCreate_PriorityOutOfRangeRejectsLocally(t *testing.T) {
	env, dir := setupCLIEnv(t)
	resetFlags(t)
	_, err := runCLICapture(t, env, dir, "create", "x", "--priority", "5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "0 and 4")
}

func TestEdit_PrioritySetClearsAndSets(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "to be prioritized")

	resetFlags(t)
	runCLI(t, env, dir, "edit", "1", "--priority", "0")
	b := fetchIssueViaHTTP(t, env, pid, 1)
	require.NotNil(t, b.Issue.Priority)
	assert.Equal(t, int64(0), *b.Issue.Priority)

	resetFlags(t)
	runCLI(t, env, dir, "edit", "1", "--priority", "-")
	b = fetchIssueViaHTTP(t, env, pid, 1)
	assert.Nil(t, b.Issue.Priority)
}

func TestEdit_PriorityCombinedWithTitle(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "old title")

	resetFlags(t)
	runCLI(t, env, dir, "edit", "1", "--title", "new title", "--priority", "2")

	b := fetchIssueViaHTTP(t, env, pid, 1)
	require.NotNil(t, b.Issue.Priority)
	assert.Equal(t, int64(2), *b.Issue.Priority)
}

func TestEdit_PriorityInvalidValueRejected(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "x")

	resetFlags(t)
	_, err := runCLICapture(t, env, dir, "edit", "1", "--priority", "9")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "0 and 4")

	resetFlags(t)
	_, err = runCLICapture(t, env, dir, "edit", "1", "--priority", "abc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "integer 0..4 or '-'")
}

func TestList_FiltersByPriority(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "p0 issue")
	createIssue(t, env, pid, "p2 issue")
	createIssue(t, env, pid, "no prio issue")

	// Set priorities via edit so the list query exercises the priority column.
	resetFlags(t)
	runCLI(t, env, dir, "edit", "1", "--priority", "0")
	resetFlags(t)
	runCLI(t, env, dir, "edit", "2", "--priority", "2")

	resetFlags(t)
	out := runCLI(t, env, dir, "list", "--priority", "0")
	assert.Contains(t, out, "p0 issue")
	assert.NotContains(t, out, "p2 issue")
	assert.NotContains(t, out, "no prio issue")

	resetFlags(t)
	out = runCLI(t, env, dir, "list", "--max-priority", "2")
	assert.Contains(t, out, "p0 issue")
	assert.Contains(t, out, "p2 issue")
	assert.NotContains(t, out, "no prio issue", "max-priority requires priority IS NOT NULL")
}

func TestList_PriorityFlagOutOfRangeRejected(t *testing.T) {
	env, dir := setupCLIEnv(t)
	resetFlags(t)
	_, err := runCLICapture(t, env, dir, "list", "--priority", "5")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "0 and 4"))
}
