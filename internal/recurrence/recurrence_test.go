package recurrence

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWalk_WeeklyDailyMonthly(t *testing.T) {
	cases := []struct {
		rule    string
		dtstart string
		tz      string
		after   string
		want    string
	}{
		{"FREQ=DAILY", "2026-05-15", "UTC", "2026-05-15", "2026-05-16"},
		{"FREQ=WEEKLY", "2026-05-15", "UTC", "2026-05-15", "2026-05-22"},
		{"FREQ=MONTHLY;BYMONTHDAY=15", "2026-05-15", "UTC", "2026-05-15", "2026-06-15"},
		{"FREQ=WEEKLY;BYDAY=MO,WE,FR", "2026-05-11", "UTC", "2026-05-11", "2026-05-13"},
		{"FREQ=MONTHLY;BYDAY=-1FR", "2026-05-29", "UTC", "2026-05-29", "2026-06-26"},
		{"FREQ=WEEKLY;INTERVAL=2", "2026-05-15", "UTC", "2026-05-15", "2026-05-29"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.rule, func(t *testing.T) {
			next, err := Walk(c.rule, c.dtstart, c.tz, c.after)
			require.NoError(t, err)
			require.NotNil(t, next)
			assert.Equal(t, c.want, *next)
		})
	}
}

func TestWalk_ExhaustedReturnsNil(t *testing.T) {
	next, err := Walk("FREQ=DAILY;COUNT=2", "2026-05-15", "UTC", "2026-05-16")
	require.NoError(t, err)
	assert.Nil(t, next, "after COUNT exhausted, Walk returns (nil, nil)")
}

func TestWalk_RespectsUNTIL(t *testing.T) {
	next, err := Walk("FREQ=DAILY;UNTIL=20260520T000000Z", "2026-05-15", "UTC", "2026-05-20")
	require.NoError(t, err)
	assert.Nil(t, next)
}

func TestWalk_InvalidRRuleReturnsError(t *testing.T) {
	_, err := Walk("FREQ=BOGUS", "2026-05-15", "UTC", "2026-05-15")
	assert.Error(t, err)
}

func TestWalk_HonorsTimezoneForBoundaries(t *testing.T) {
	next, err := Walk("FREQ=WEEKLY;BYDAY=MO", "2026-05-11", "America/New_York", "2026-05-11")
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, "2026-05-18", *next)
}
