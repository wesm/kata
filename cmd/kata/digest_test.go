package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDigest_HumanRender(t *testing.T) {
	f := newCLIFixture(t)
	createIssueViaHTTP(t, f.env, f.dir, "first")
	createIssueViaHTTP(t, f.env, f.dir, "second")

	require.NoError(t, f.execute("digest", "--since", "1h"))
	out := f.buf.String()
	assert.Contains(t, out, "digest ")
	assert.Contains(t, out, "created=2")
	assert.Contains(t, out, "#1")
	assert.Contains(t, out, "created")
}

func TestDigest_RejectsBadSince(t *testing.T) {
	f := newCLIFixture(t)

	err := f.execute("digest", "--since", "blarg")
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
