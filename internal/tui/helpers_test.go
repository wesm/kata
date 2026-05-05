package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// int64Ptr returns a pointer to v, the canonical helper for populating
// optional *int64 fields (e.g. Issue.ParentNumber) in test fixtures.
func int64Ptr(v int64) *int64 { return &v }

// testIssueProjectID is the ProjectID used by the testIssue builder. It
// matches the projectID on the scope returned by newTestModel/homedScope so
// fixtures and model state agree by default.
const testIssueProjectID int64 = 7

// issueOpt mutates an Issue built by testIssue. Use the with* helpers
// below rather than constructing issueOpt values directly.
type issueOpt func(*Issue)

// testIssue builds an Issue with ProjectID=7 and a default title of
// "issue <number>". Tests that care about the title set it explicitly via
// withTitle.
func testIssue(number int64, opts ...issueOpt) Issue {
	i := Issue{
		ProjectID: testIssueProjectID,
		Number:    number,
		Title:     fmt.Sprintf("issue %d", number),
	}
	for _, opt := range opts {
		opt(&i)
	}
	return i
}

func withTitle(title string) issueOpt {
	return func(i *Issue) { i.Title = title }
}

func withParent(parent int64) issueOpt {
	return func(i *Issue) { i.ParentNumber = &parent }
}

func withCounts(open, total int) issueOpt {
	return func(i *Issue) { i.ChildCounts = &ChildCounts{Open: open, Total: total} }
}

func withBlocks(blocks ...int64) issueOpt {
	return func(i *Issue) { i.Blocks = blocks }
}

func withStatus(status string) issueOpt {
	return func(i *Issue) { i.Status = status }
}

func withLabels(labels ...string) issueOpt {
	return func(i *Issue) { i.Labels = labels }
}

// mockDaemon builds an httptest.Server that dispatches by URL path. The
// server sets Content-Type: application/json before invoking each handler,
// so handlers only need to write status + body. Unmapped paths return 404.
// The server is closed via t.Cleanup.
func mockDaemon(t *testing.T, routes map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if h, ok := routes[r.URL.Path]; ok {
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// projectNotInitializedHandler responds to /api/v1/projects/resolve with
// the 404 + project_not_initialized envelope used to drive the boot path
// into the empty-state / projects-view branches.
func projectNotInitializedHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": 404,
		"error":  map[string]any{"code": "project_not_initialized"},
	})
}

// useNoColor switches the package-level styles to colorNone for the
// duration of the test process. Tests that need ANSI-free output call
// this so assertions can use plain string matching.
func useNoColor(_ *testing.T) {
	applyColorMode(colorNone, io.Discard)
}

// renderBodyNoColor renders lm's body and strips ANSI escapes, the
// shape every list-render test wants when asserting on plain text.
func renderBodyNoColor(lm listModel, width, height int, chrome viewChrome) string {
	return stripANSI(lm.renderBody(width, height, chrome))
}

// newTestModel returns a Model wired with a stub Client, single-project
// scope (projectID=7), and list.loading=false — the baseline shared by
// most state-machine tests in this package.
func newTestModel() Model {
	m := initialModel(Options{})
	m.api = &Client{}
	m.scope = scope{projectID: 7}
	m.list.loading = false
	return m
}

// updateModel pipes msg through m.Update and unwraps the returned
// tea.Model interface back to the concrete Model struct.
func updateModel(m Model, msg tea.Msg) (Model, tea.Cmd) {
	out, cmd := m.Update(msg)
	return out.(Model), cmd
}

// sendRune dispatches a single-rune KeyMsg through m.Update and returns
// the resulting Model, collapsing the verbose tea.KeyMsg + type-assert
// boilerplate to one call.
func sendRune(m Model, r rune) Model {
	nm, _ := updateModel(m, keyRune(r))
	return nm
}

// keyRune builds a single-rune tea.KeyMsg for tests that route keys
// directly (e.g. routeProjectsViewKey) rather than through m.Update.
func keyRune(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// sendKey dispatches a typed-key KeyMsg (e.g. tea.KeyDown, tea.KeyTab,
// tea.KeyEnter) through m.Update and returns the resulting Model.
func sendKey(m Model, kt tea.KeyType) Model {
	nm, _ := updateModel(m, tea.KeyMsg{Type: kt})
	return nm
}

// resizeModel sends a WindowSizeMsg through m.Update and returns the
// resulting Model, collapsing the WindowSizeMsg + type-assert
// boilerplate that every layout/chrome test repeats.
func resizeModel(m Model, width, height int) Model {
	nm, _ := updateModel(m, tea.WindowSizeMsg{Width: width, Height: height})
	return nm
}

// makeTestIssues returns count Issues numbered 1..count with a stable
// "row" title, suitable for pagination and viewport boundary tests.
func makeTestIssues(count int) []Issue {
	issues := make([]Issue, count)
	for i := range issues {
		issues[i] = Issue{Number: int64(i + 1), Title: "row"}
	}
	return issues
}

// mockProject bundles the three parallel maps the projects view consults
// (projectsByID, projectIdentByID, projectStats) into one record so test
// fixtures can list projects without juggling three coordinated literals.
type mockProject struct {
	ID    int64
	Name  string
	Ident string
	Stats ProjectStatsSummary
}

// injectProjects populates the model's three parallel project maps from
// the given mockProject records.
func injectProjects(m *Model, projects ...mockProject) {
	for _, p := range projects {
		m.projectsByID[p.ID] = p.Name
		m.projectIdentByID[p.ID] = p.Ident
		m.projectStats[p.ID] = p.Stats
	}
}

// setupProjectsView returns a Model in viewProjects with standard 120×24
// dimensions, optionally pre-populated with the given mockProject rows.
func setupProjectsView(projects ...mockProject) Model {
	m := initialModel(Options{})
	m.view = viewProjects
	m.width, m.height = 120, 24
	injectProjects(&m, projects...)
	return m
}

// setupEmptyView returns a Model in viewEmpty with standard 80×24
// dimensions — the baseline for empty-state rendering and key-handling
// tests.
func setupEmptyView() Model {
	m := initialModel(Options{})
	m.view = viewEmpty
	m.width, m.height = 80, 24
	return m
}

// assertCmdQuit fails t if cmd is nil or its result is not tea.QuitMsg.
// The shape every quit-path assertion repeats: a nil cmd means the
// keystroke didn't trigger Bubble Tea's quit at all.
func assertCmdQuit(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("cmd = nil, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd = %T, want tea.QuitMsg", cmd())
	}
}

// homedScope builds a scope where the active project equals the home
// project — the common shape for tests that need a single-project scope
// already settled (i.e. came from a list, not a fresh boot).
func homedScope(id int64, name string) scope {
	return scope{projectID: id, projectName: name, homeProjectID: id, homeProjectName: name}
}

// assertContains fails t if subject does not contain substr. failMsg
// describes the assertion (the helper appends the missing substring and
// the full subject to keep diagnostics consistent across tests).
func assertContains(t *testing.T, subject, substr, failMsg string) {
	t.Helper()
	if !strings.Contains(subject, substr) {
		t.Fatalf("%s. Expected to find %q in:\n%s", failMsg, substr, subject)
	}
}

// assertNotContains is the inverse of assertContains.
func assertNotContains(t *testing.T, subject, substr, failMsg string) {
	t.Helper()
	if strings.Contains(subject, substr) {
		t.Fatalf("%s. Did not expect to find %q in:\n%s", failMsg, substr, subject)
	}
}

// assertStyleForeground fails t if style's foreground is not a
// lipgloss.Color whose string value equals want. Collapses the recurring
// type-assert + string-compare block in style tests.
func assertStyleForeground(t *testing.T, style lipgloss.Style, label, want string) {
	t.Helper()
	fg, ok := style.GetForeground().(lipgloss.Color)
	if !ok {
		t.Fatalf("%s foreground = %T, want lipgloss.Color", label, style.GetForeground())
	}
	if string(fg) != want {
		t.Fatalf("%s foreground = %q, want %q", label, string(fg), want)
	}
}

// assertTerminalColor fails t if tc is not a lipgloss.Color whose string
// value equals want. Used for raw lipgloss.TerminalColor vars (e.g.
// panel border colors) that aren't wrapped in a Style.
func assertTerminalColor(t *testing.T, tc lipgloss.TerminalColor, label, want string) {
	t.Helper()
	c, ok := tc.(lipgloss.Color)
	if !ok {
		t.Fatalf("%s = %T, want lipgloss.Color", label, tc)
	}
	if string(c) != want {
		t.Fatalf("%s = %q, want %q", label, string(c), want)
	}
}

// assertSelection fails t if either the cursor row or the recorded
// selectedNumber identity differs from the wants.
func assertSelection(t *testing.T, m Model, wantCursor int, wantNumber int64) {
	t.Helper()
	if m.list.cursor != wantCursor {
		t.Fatalf("cursor = %d, want %d", m.list.cursor, wantCursor)
	}
	if m.list.selectedNumber != wantNumber {
		t.Fatalf("selectedNumber = %d, want %d", m.list.selectedNumber, wantNumber)
	}
}
