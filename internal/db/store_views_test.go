package db_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpressionIndexesPresent(t *testing.T) {
	d := openTestDB(t)
	want := []string{
		"issues_project_scheduled_on_open",
		"issues_project_deadline_on_open",
		"issues_project_someday_open",
		"projects_area",
	}
	for _, name := range want {
		var n int
		err := d.QueryRow(`SELECT COUNT(*) FROM sqlite_master
			WHERE type='index' AND name=?`, name).Scan(&n)
		require.NoError(t, err)
		assert.Equal(t, 1, n, "index %s missing", name)
	}
}
