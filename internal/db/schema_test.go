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

func TestEventsCarryOriginInstanceUID(t *testing.T) {
	d, ctx, _, _ := setupTestIssue(t) // creates project + issue + issue.created event

	var id int64
	var origUID string
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT id, origin_instance_uid FROM events
		   ORDER BY id DESC LIMIT 1`,
	).Scan(&id, &origUID))
	assert.Equal(t, d.InstanceUID(), origUID,
		"origin_instance_uid must be this daemon's instance_uid")
}

func TestRecurrencesTableAndIssueLinkage(t *testing.T) {
	d, ctx, p, _ := setupTestIssue(t)

	_, err := d.ExecContext(ctx, `INSERT INTO recurrences
        (uid, project_id, rrule, dtstart, timezone, template_title, author)
        VALUES ('01J0000000000000000000REC1', ?, 'FREQ=WEEKLY', '2026-05-15',
                'America/New_York', 'Pay rent', 'tester')`, p.ID)
	require.NoError(t, err)

	// recurrence_id + occurrence_key columns exist on issues
	var n int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('issues')
           WHERE name IN ('recurrence_id','occurrence_key')`,
	).Scan(&n))
	assert.Equal(t, 2, n)

	// unique index exists
	var idxn int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master
           WHERE type='index' AND name='issues_recurrence_occurrence_uniq'`,
	).Scan(&idxn))
	assert.Equal(t, 1, idxn)
}

func TestSchemaVersionAt10(t *testing.T) {
	d := openTestDB(t)
	assertSchemaVersion(t, d, 10)
}
