package daemon_test

import (
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/hooks"
)

// recordingSink captures every Enqueue for assertion. Lives only in tests;
// production paths use the real *hooks.Dispatcher.
type recordingSink struct {
	mu     sync.Mutex
	events []db.Event
}

func (r *recordingSink) Enqueue(evt db.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, evt)
}

func (r *recordingSink) snapshot() []db.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]db.Event, len(r.events))
	copy(out, r.events)
	return out
}

var _ hooks.Sink = (*recordingSink)(nil)

// TestHooks_IssueCreate_EnqueuesSibling exercises the create-issue handler
// end-to-end and asserts the sibling Hooks.Enqueue fires alongside the
// Broadcaster.Broadcast — proving the integration point exists and matches
// the persisted event row's Type.
func TestHooks_IssueCreate_EnqueuesSibling(t *testing.T) {
	sink := &recordingSink{}
	h, pid := bootstrapProject(t, withHooksSink(sink))
	ts := h.ts.(*httptest.Server)

	// Pre-condition: project init does not enqueue anything.
	require.Empty(t, sink.snapshot(), "project init should not emit hook events")

	resp, body := postJSON(t, ts, issuesURL(pid),
		map[string]any{"actor": "agent-1", "title": "first", "body": "details"})
	require.Equal(t, 200, resp.StatusCode, string(body))

	captured := sink.snapshot()
	require.Len(t, captured, 1, "exactly one hook event should have been enqueued")
	assert.Equal(t, "issue.created", captured[0].Type)
	assert.Equal(t, "agent-1", captured[0].Actor)
	assert.NotZero(t, captured[0].ID, "captured event should carry the persisted row id")
}
