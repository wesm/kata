package db_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIssuesMetadataAndRevisionColumns(t *testing.T) {
	d, ctx, _, iss := setupTestIssue(t)
	var meta string
	var rev int64
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT metadata, revision FROM issues WHERE id = ?`, iss.ID,
	).Scan(&meta, &rev))
	assert.Equal(t, "{}", meta, "metadata default must be '{}'")
	assert.Equal(t, int64(1), rev, "revision default must be 1")
}

func TestIssuesMetadataRejectsInvalidJSON(t *testing.T) {
	d, ctx, _, iss := setupTestIssue(t)
	_, err := d.ExecContext(ctx,
		`UPDATE issues SET metadata = 'not json' WHERE id = ?`, iss.ID,
	)
	require.Error(t, err, "json_valid CHECK must reject non-JSON metadata")
}

func TestProjectsMetadataAndRevisionColumns(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	var meta string
	var rev int64
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT metadata, revision FROM projects WHERE id = ?`, p.ID,
	).Scan(&meta, &rev))
	assert.Equal(t, "{}", meta, "metadata default must be '{}'")
	assert.Equal(t, int64(1), rev, "revision default must be 1")
}

func TestProjectsMetadataRejectsInvalidJSON(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	_, err := d.ExecContext(ctx,
		`UPDATE projects SET metadata = 'not json' WHERE id = ?`, p.ID,
	)
	require.Error(t, err, "json_valid CHECK must reject non-JSON metadata")
}
