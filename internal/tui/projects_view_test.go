package tui

import (
	"strings"
	"testing"
)

// TestProjectsView_RendersWithoutPanic confirms the view renders a
// non-empty frame for a model in viewProjects state, even with no
// projects loaded yet. Required for boot landing where the fetch is
// still in flight.
func TestProjectsView_RendersWithoutPanic(t *testing.T) {
	m := initialModel(Options{})
	m.view = viewProjects
	m.width = 80
	m.height = 24

	out := m.View()
	if out == "" {
		t.Fatal("viewProjects must render a non-empty frame")
	}
	if !strings.Contains(out, "projects") {
		t.Errorf("expected 'projects' in viewProjects output:\n%s", out)
	}
}
