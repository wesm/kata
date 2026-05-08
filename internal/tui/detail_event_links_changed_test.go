package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestLinksChangedDesc covers the TUI describer for the aggregated
// issue.links_changed event from the PATCH (`kata edit`) path. Without
// a typed describer the events tab fell back to the raw "links_changed"
// fragment and dropped the actual add/remove detail (kata#1 follow-up).
func TestLinksChangedDesc(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{
			name: "parent_set_only",
			payload: map[string]any{
				"parent_set": float64(10),
			},
			want: "links: +parent #10",
		},
		{
			name: "parent_removed_only",
			payload: map[string]any{
				"parent_removed": float64(7),
			},
			want: "links: -parent #7",
		},
		{
			name: "parent_replace",
			payload: map[string]any{
				"parent_set":     float64(10),
				"parent_removed": float64(7),
			},
			want: "links: parent #7→#10",
		},
		{
			name: "blocks_added",
			payload: map[string]any{
				"blocks_added": []any{float64(50), float64(51)},
			},
			want: "links: +blocks #50, +blocks #51",
		},
		{
			name: "blocked_by_removed",
			payload: map[string]any{
				"blocked_by_removed": []any{float64(15)},
			},
			want: "links: -blocked_by #15",
		},
		{
			name: "mixed",
			payload: map[string]any{
				"parent_set":      float64(2),
				"blocks_added":    []any{float64(50)},
				"related_removed": []any{float64(9)},
			},
			want: "links: +parent #2, +blocks #50, -related #9",
		},
		{
			name:    "empty",
			payload: map[string]any{},
			want:    "links unchanged",
		},
		{
			name:    "nil_payload",
			payload: nil,
			want:    "links changed",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			e := EventLogEntry{Type: "issue.links_changed", Payload: tc.payload}
			got := eventDescription(e)
			assert.Equal(t, tc.want, got)
		})
	}
}
