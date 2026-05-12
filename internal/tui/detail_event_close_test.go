package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEventChunkLines_CloseDetail covers the events-tab rendering for
// issue.closed events. Before this change the tab showed only
// "closed (done)" with no message or evidence; reviewers had to drop to
// `kata audit closes` to see what was actually closed.
func TestEventChunkLines_CloseDetail(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    []string
	}{
		{
			name: "message_and_commit",
			payload: map[string]any{
				"reason":  "done",
				"message": "Fixed Safari callback double-submit.",
				"evidence": []any{
					map[string]any{"type": "commit", "sha": "a1b2c3d"},
				},
			},
			want: []string{
				"  message: Fixed Safari callback double-submit.",
				"  evidence: commit a1b2c3d",
			},
		},
		{
			name: "every_evidence_type",
			payload: map[string]any{
				"reason":  "done",
				"message": "Closing.",
				"evidence": []any{
					map[string]any{"type": "commit", "sha": "abc1234"},
					map[string]any{"type": "pr", "url": "https://example.com/pr/1"},
					map[string]any{"type": "test", "command": "go test ./..."},
					map[string]any{"type": "reviewed-paths", "paths": []any{"a.go", "b.go"}},
					map[string]any{"type": "no-change-audit", "rationale": "schema unchanged"},
					map[string]any{"type": "duplicate-of", "issue_ref": "kata#d4ex"},
					map[string]any{"type": "superseded-by", "issue_ref": "kata#sxyz"},
				},
			},
			want: []string{
				"  message: Closing.",
				"  evidence: commit abc1234",
				"  evidence: pr https://example.com/pr/1",
				"  evidence: test go test ./...",
				"  evidence: reviewed-paths a.go, b.go",
				"  evidence: no-change-audit schema unchanged",
				"  evidence: duplicate-of kata#d4ex",
				"  evidence: superseded-by kata#sxyz",
			},
		},
		{
			name: "message_only_no_evidence",
			payload: map[string]any{
				"reason":  "wontfix",
				"message": "Not in scope for this milestone.",
			},
			want: []string{
				"  message: Not in scope for this milestone.",
			},
		},
		{
			name: "tui_bypass_no_message_no_evidence",
			payload: map[string]any{
				"reason": "done",
			},
			want: nil,
		},
		{
			name:    "nil_payload",
			payload: nil,
			want:    nil,
		},
		{
			name: "unknown_evidence_type_falls_back_to_tag",
			payload: map[string]any{
				"reason": "done",
				"evidence": []any{
					map[string]any{"type": "future-thing", "data": "ignored"},
				},
			},
			want: []string{
				"  evidence: future-thing",
			},
		},
		{
			name: "malformed_evidence_array_dropped",
			payload: map[string]any{
				"reason":   "done",
				"evidence": "not-an-array",
			},
			want: nil,
		},
		{
			name: "non_map_evidence_items_skipped",
			payload: map[string]any{
				"reason": "done",
				"evidence": []any{
					"a string in the array",
					map[string]any{"type": "commit", "sha": "abc"},
				},
			},
			want: []string{
				"  evidence: commit abc",
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			e := EventLogEntry{Type: "issue.closed", Payload: tc.payload}
			got := closeDetailLines(e)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestEventChunkLines_HeaderShape pins down the chunk structure for
// closed and non-closed events. The header line is always present;
// closed events get the close-detail tail, others don't.
func TestEventChunkLines_HeaderShape(t *testing.T) {
	t.Run("non_close_event_single_line", func(t *testing.T) {
		e := EventLogEntry{
			Type:    "issue.commented",
			Actor:   "wes",
			Payload: map[string]any{"comment_id": float64(7)},
		}
		lines := eventChunkLines(e, false)
		require.Len(t, lines, 1)
		assert.Contains(t, lines[0], "[issue.commented]")
		assert.Contains(t, lines[0], "wes")
		assert.Contains(t, lines[0], "added comment")
	})

	t.Run("close_event_with_detail_multi_line", func(t *testing.T) {
		e := EventLogEntry{
			Type:  "issue.closed",
			Actor: "wes",
			Payload: map[string]any{
				"reason":  "done",
				"message": "shipped",
				"evidence": []any{
					map[string]any{"type": "commit", "sha": "abc1234"},
				},
			},
		}
		lines := eventChunkLines(e, true)
		require.GreaterOrEqual(t, len(lines), 3)
		assert.Contains(t, lines[0], "> ", "first line carries cursor marker")
		assert.Contains(t, lines[0], "[issue.closed]")
		assert.Contains(t, lines[0], "closed (done)")
		assert.Equal(t, "  message: shipped", lines[1])
		assert.Equal(t, "  evidence: commit abc1234", lines[2])
	})

	t.Run("close_event_no_detail_single_line", func(t *testing.T) {
		e := EventLogEntry{
			Type:    "issue.closed",
			Actor:   "wes",
			Payload: map[string]any{"reason": "done"},
		}
		lines := eventChunkLines(e, false)
		require.Len(t, lines, 1)
		assert.Contains(t, lines[0], "closed (done)")
	})
}
