package daemon_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/wesm/kata/internal/db"
)

func TestHealth_ReportsSchemaAndUptime(t *testing.T) {
	ts, _ := startDefaultTestServer(t)

	var body struct {
		OK            bool   `json:"ok"`
		SchemaVersion int    `json:"schema_version"`
		Uptime        string `json:"uptime"`
		DBPath        string `json:"db_path"`
	}
	getAndUnmarshal(t, ts, "/api/v1/health", http.StatusOK, &body)
	assert.True(t, body.OK)
	assert.Equal(t, db.CurrentSchemaVersion(), body.SchemaVersion)
	assert.NotEmpty(t, body.Uptime)
	assert.NotEmpty(t, body.DBPath)
}
