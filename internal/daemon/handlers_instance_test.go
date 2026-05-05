package daemon_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/uid"
)

// TestInstance_ReturnsLocalUID covers spec §8.8: GET /api/v1/instance returns
// the value db.Open seeded into meta.instance_uid.
func TestInstance_ReturnsLocalUID(t *testing.T) {
	ts, d := startDefaultTestServer(t)

	var body struct {
		InstanceUID string `json:"instance_uid"`
	}
	getAndUnmarshal(t, ts, "/api/v1/instance", http.StatusOK, &body)
	assert.Equal(t, d.db.InstanceUID(), body.InstanceUID)
	assert.True(t, uid.Valid(body.InstanceUID), "instance_uid %q invalid", body.InstanceUID)
}

// TestInstance_503WhenUIDUnset covers spec §8.8 second bullet: the handler
// returns 503 instance_uid_unset when the *db.DB's cached InstanceUID() is
// empty. In production this is theoretical (db.Open always seeds the row);
// the test reaches it by routing the server through OpenReadOnly, which
// skips the seed step and yields a *DB with empty cached value.
func TestInstance_503WhenUIDUnset(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")

	// Materialize a real DB file so OpenReadOnly has something to attach to.
	primary, err := db.Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, primary.Close())

	// Read-only handle bypasses ensureInstanceUID; cached InstanceUID() is "".
	ro, err := db.OpenReadOnly(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ro.Close() })
	require.Empty(t, ro.InstanceUID(), "OpenReadOnly must yield empty cached InstanceUID")

	ts := startTestServer(t, daemon.ServerConfig{DB: ro, StartedAt: time.Now().UTC()})

	resp, bs := getStatusBody(t, ts, "/api/v1/instance")
	assertAPIError(t, resp.StatusCode, bs, http.StatusServiceUnavailable, "instance_uid_unset")
}
