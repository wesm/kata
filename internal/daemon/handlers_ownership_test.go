package daemon_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestAssign_HappyPath(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	resp, out := postAssign(t, env, pid, n, "tester", "alice")
	require.Equal(t, 200, resp.StatusCode)
	require.NotNil(t, out.Issue.Owner)
	assert.Equal(t, "alice", *out.Issue.Owner)
	require.NotNil(t, out.Event)
	assert.Equal(t, "issue.assigned", out.Event.Type)
	assert.True(t, out.Changed)
}

func TestAssign_SameOwnerIsNoOp(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	resp, _ := postAssign(t, env, pid, n, "tester", "alice")
	require.Equal(t, 200, resp.StatusCode)

	resp, out := postAssign(t, env, pid, n, "tester", "alice")
	require.Equal(t, 200, resp.StatusCode)
	assert.Nil(t, out.Event)
	assert.False(t, out.Changed)
}

func TestAssign_BlankOwnerIs400(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	resp, _ := postAssign(t, env, pid, n, "tester", "   ")
	assert.Equal(t, 400, resp.StatusCode)
}

func TestAssign_BlankActorIs400(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	resp, _ := postAssign(t, env, pid, n, "   ", "alice")
	assert.Equal(t, 400, resp.StatusCode)
}

func TestUnassign_BlankActorIs400(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	resp, _ := postUnassign(t, env, pid, n, "   ")
	assert.Equal(t, 400, resp.StatusCode)
}

func TestUnassign_HappyPath(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	resp, _ := postAssign(t, env, pid, n, "tester", "alice")
	require.Equal(t, 200, resp.StatusCode)

	resp, out := postUnassign(t, env, pid, n, "tester")
	require.Equal(t, 200, resp.StatusCode)
	assert.Nil(t, out.Issue.Owner)
	require.NotNil(t, out.Event)
	assert.Equal(t, "issue.unassigned", out.Event.Type)
	assert.True(t, out.Changed)
}
