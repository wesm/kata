package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIssueKeysAreRegistered(t *testing.T) {
	for _, key := range []string{"scheduled_on", "deadline_on", "someday", "today_bucket", "checklist", "timezone"} {
		kind, ok := IssueRegistry[key]
		assert.True(t, ok, "issue key %s missing from registry", key)
		assert.NotZero(t, kind.Type, "issue key %s has zero-value Type", key)
	}
}

func TestProjectKeysAreRegistered(t *testing.T) {
	for _, key := range []string{"area", "sidebar_order", "icon", "timezone"} {
		kind, ok := ProjectRegistry[key]
		assert.True(t, ok, "project key %s missing from registry", key)
		assert.NotZero(t, kind.Type, "project key %s has zero-value Type", key)
	}
}

func TestUnknownKeyNotPresent(t *testing.T) {
	_, ok := IssueRegistry["definitely_not_a_key"]
	assert.False(t, ok)
}

func TestTodayBucketEnum(t *testing.T) {
	entry, ok := IssueRegistry["today_bucket"]
	assert.True(t, ok)
	assert.ElementsMatch(t, []string{"day", "evening"}, entry.Enum)
}
