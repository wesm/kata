package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestDigest_HumanRender(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "first")
	createIssue(t, env, pid, "second")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "digest", "--since", "1h"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.Contains(t, out, "digest ")
	assert.Contains(t, out, "created=2")
	assert.Contains(t, out, "#1")
	assert.Contains(t, out, "created")
}

func TestDigest_RejectsBadSince(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	_ = resolvePIDViaHTTP(t, env.URL, dir)

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "digest", "--since", "blarg"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid time spec")
}

func TestDigest_ParseSinceUntil(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)

	got, err := parseSinceUntil("24h", now)
	require.NoError(t, err)
	assert.Equal(t, now.Add(-24*time.Hour), got)

	got, err = parseSinceUntil("7d", now)
	require.NoError(t, err)
	assert.Equal(t, now.Add(-7*24*time.Hour), got)

	got, err = parseSinceUntil("2026-05-01T00:00:00Z", now)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), got)

	_, err = parseSinceUntil("garbage", now)
	require.Error(t, err)
}
