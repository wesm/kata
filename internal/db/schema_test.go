package db_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/uid"
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

func TestEventsOriginSeqEqualsIDOnLocalInsert(t *testing.T) {
	d, ctx, _, _ := setupTestIssue(t) // creates project + issue + issue.created event

	var id, origSeq int64
	var origUID string
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT id, origin_instance_uid, origin_seq FROM events
		   ORDER BY id DESC LIMIT 1`,
	).Scan(&id, &origUID, &origSeq))
	assert.Equal(t, id, origSeq,
		"origin_seq must equal id for locally-originated events")
	assert.Equal(t, d.InstanceUID(), origUID,
		"origin_instance_uid must be this daemon's instance_uid")
}

func TestEventsOriginSeqPartialIndexAllowsMultipleNulls(t *testing.T) {
	// Two events with origin_seq IS NULL must coexist — the partial index
	// excludes them. This simulates a federated-replay code path that supplies
	// origin_seq post-hoc; the local insertEventTx stamps the value before
	// commit, so locally-originated events never exercise this state.
	d, ctx, p := setupTestProject(t)

	insertWithNullSeq := func() error {
		eventUID, err := uid.New()
		require.NoError(t, err)
		_, err = d.ExecContext(ctx,
			`INSERT INTO events
			   (uid, origin_instance_uid, project_id, project_name, type, actor, payload, origin_seq)
			 VALUES (?, ?, ?, ?, 'test.event', 'tester', '{}', NULL)`,
			eventUID, "FEDORIGIN0000000000000000A", p.ID, p.Name)
		return err
	}
	require.NoError(t, insertWithNullSeq())
	require.NoError(t, insertWithNullSeq(),
		"two NULL origin_seq rows must be allowed by the partial index")
}
