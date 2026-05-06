package tui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeListAPI is the test double for listAPI. Each method records its
// last input on the receiver and returns whatever the test seeded into
// the corresponding result fields. Counters surface "exactly N calls"
// assertions so empty-title regression tests stay direct.
type fakeListAPI struct {
	listIssuesCalls    int
	listAllCalls       int
	createCalls        int
	closeCalls         int
	reopenCalls        int
	setPriorityCalls   int
	lastListProjectID  int64
	lastListFilter     ListFilter
	lastCreateProject  int64
	lastCreateBody     CreateIssueBody
	lastCloseProjectID int64
	lastCloseNumber    int64
	lastCloseActor     string
	lastReopenProject  int64
	lastReopenNumber   int64
	lastReopenActor    string
	lastPriorityPID    int64
	lastPriorityNumber int64
	lastPriorityValue  *int64
	lastPriorityActor  string
	listIssuesResult   []Issue
	listAllResult      []Issue
	createResult       *MutationResp
	closeResult        *MutationResp
	reopenResult       *MutationResp
	setPriorityResult  *MutationResp
	listIssuesErr      error
	listAllErr         error
	createErr          error
	closeErr           error
	reopenErr          error
	setPriorityErr     error
}

func (f *fakeListAPI) ListIssues(
	_ context.Context, projectID int64, filter ListFilter,
) ([]Issue, error) {
	f.listIssuesCalls++
	f.lastListProjectID = projectID
	f.lastListFilter = filter
	return f.listIssuesResult, f.listIssuesErr
}

func (f *fakeListAPI) ListAllIssues(
	_ context.Context, filter ListFilter,
) ([]Issue, error) {
	f.listAllCalls++
	f.lastListFilter = filter
	return f.listAllResult, f.listAllErr
}

func (f *fakeListAPI) CreateIssue(
	_ context.Context, projectID int64, body CreateIssueBody,
) (*MutationResp, error) {
	f.createCalls++
	f.lastCreateProject = projectID
	f.lastCreateBody = body
	return f.createResult, f.createErr
}

func (f *fakeListAPI) Close(
	_ context.Context, projectID, number int64, actor string,
) (*MutationResp, error) {
	f.closeCalls++
	f.lastCloseProjectID = projectID
	f.lastCloseNumber = number
	f.lastCloseActor = actor
	return f.closeResult, f.closeErr
}

func (f *fakeListAPI) Reopen(
	_ context.Context, projectID, number int64, actor string,
) (*MutationResp, error) {
	f.reopenCalls++
	f.lastReopenProject = projectID
	f.lastReopenNumber = number
	f.lastReopenActor = actor
	return f.reopenResult, f.reopenErr
}

func (f *fakeListAPI) SetPriority(
	_ context.Context, projectID, number int64, priority *int64, actor string,
) (*MutationResp, error) {
	f.setPriorityCalls++
	f.lastPriorityPID = projectID
	f.lastPriorityNumber = number
	f.lastPriorityValue = priority
	f.lastPriorityActor = actor
	return f.setPriorityResult, f.setPriorityErr
}

// runeKey synthesizes a tea.KeyMsg for a single rune so tests don't
// have to repeat the struct construction. Multi-character buffers are
// fed one rune at a time to mirror real keystrokes.
func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// newListEnv returns the standard test trio for listModel.Update calls:
// a fresh fakeListAPI, a default keymap, and a single-project scope
// (projectID: 7). The api is returned by pointer so callers can seed
// result/error fields before invoking Update.
func newListEnv() (*fakeListAPI, keymap, scope) {
	return &fakeListAPI{}, newKeymap(), scope{projectID: 7}
}

// drainCmd executes the tea.Cmd returned by Update once and feeds the
// resulting message back into Update so the test sees the post-fetch
// state. Returns the second-pass model so chains stay one-line.
func drainCmd(
	t *testing.T, lm listModel, cmd tea.Cmd, km keymap, api listAPI, sc scope,
) listModel {
	t.Helper()
	if cmd == nil {
		return lm
	}
	msg := cmd()
	out, _ := lm.Update(msg, km, api, sc)
	return out
}

// TestList_StatusCycle confirms `s` cycles "" → open → closed → "" without
// refetching. Status now filters the cached all-status working set.
func TestList_StatusCycle(t *testing.T) {
	api, km, sc := newListEnv()
	lm := listModel{issues: []Issue{
		{Number: 1, Status: "open"},
		{Number: 2, Status: "closed"},
	}}

	wants := []struct {
		status  string
		visible []int64
	}{
		{status: "open", visible: []int64{1}},
		{status: "closed", visible: []int64{2}},
		{status: "", visible: []int64{1, 2}},
	}
	for i, want := range wants {
		var cmd tea.Cmd
		lm, cmd = lm.Update(runeKey('s'), km, api, sc)
		if lm.filter.Status != want.status {
			t.Fatalf("step %d: status = %q, want %q", i, lm.filter.Status, want.status)
		}
		if cmd != nil {
			t.Fatalf("step %d: expected nil cmd, got %T", i, cmd)
		}
		visible := filteredIssues(lm.issues, lm.filter)
		if len(visible) != len(want.visible) {
			t.Fatalf("step %d: visible = %+v, want numbers %v", i, visible, want.visible)
		}
		for j, wantNumber := range want.visible {
			if visible[j].Number != wantNumber {
				t.Fatalf("step %d row %d: visible #%d, want #%d",
					i, j, visible[j].Number, wantNumber)
			}
		}
	}
	if api.listIssuesCalls != 0 {
		t.Fatalf("listIssuesCalls = %d, want 0", api.listIssuesCalls)
	}
}

func TestList_StatusOpenDoesNotAutoExpandMatchingChildren(t *testing.T) {
	api, km, sc := newListEnv()
	parent := int64(1)
	lm := listModel{issues: []Issue{
		{
			ProjectID: 7, Number: parent, Status: "open",
			Title: "parent", ChildCounts: &ChildCounts{Open: 1, Total: 1},
		},
		{
			ProjectID: 7, Number: 2, Status: "open",
			Title: "child", ParentNumber: &parent,
		},
	}}

	lm, cmd := lm.Update(runeKey('s'), km, api, sc)
	if cmd != nil {
		t.Fatalf("status toggle should not dispatch a command, got %T", cmd)
	}
	rows := lm.visibleRows()
	if len(rows) != 1 || rows[0].issue.Number != parent {
		t.Fatalf("visible rows after status=open = %+v, want collapsed parent only", rows)
	}
	if rows[0].expanded {
		t.Fatalf("parent row expanded by status filter; want collapsed")
	}
	if lm.expanded[issueKey{projectID: 7, number: parent}] {
		t.Fatalf("status filter mutated explicit expansion state: %+v", lm.expanded)
	}
}

func TestList_StatusOpenPromotesChildWhenParentClosed(t *testing.T) {
	api, km, sc := newListEnv()
	parent := int64(17)
	lm := listModel{issues: []Issue{
		{
			ProjectID: 7, Number: parent, Status: "closed",
			Title: "closed parent", ChildCounts: &ChildCounts{Open: 1, Total: 1},
		},
		{
			ProjectID: 7, Number: 90, Status: "open",
			Title: "open child", ParentNumber: &parent,
		},
	}}

	lm, cmd := lm.Update(runeKey('s'), km, api, sc)
	if cmd != nil {
		t.Fatalf("status toggle should not dispatch a command, got %T", cmd)
	}
	rows := lm.visibleRows()
	if len(rows) != 1 || rows[0].issue.Number != 90 {
		t.Fatalf("visible rows after status=open = %+v, want promoted open child only", rows)
	}
	if rows[0].depth != 0 || rows[0].context {
		t.Fatalf("open child row = %+v, want top-level non-context row", rows[0])
	}
}

func TestList_StatusOpenShowsNestedMatchingGrandchildContext(t *testing.T) {
	parent := int64(1)
	child := int64(2)
	rows := buildQueueRows([]Issue{
		{ProjectID: 7, Number: parent, Status: "open", Title: "parent"},
		{ProjectID: 7, Number: child, Status: "closed", Title: "child", ParentNumber: &parent},
		{ProjectID: 7, Number: 3, Status: "open", Title: "grandchild", ParentNumber: &child},
	}, ListFilter{Status: "open"}, nil)

	require.Len(t, rows, 3)
	assert.Equal(t, int64(1), rows[0].issue.Number)
	assert.True(t, rows[0].expanded)
	assert.False(t, rows[0].context)
	assert.Equal(t, int64(2), rows[1].issue.Number)
	assert.True(t, rows[1].expanded)
	assert.True(t, rows[1].context)
	assert.Equal(t, int64(3), rows[2].issue.Number)
	assert.False(t, rows[2].context)
}

// TestList_Search_AccumulatesAndCommits drives /, then "abc", then
// Enter through Model.Update so the M3a inline command bar handles
// the keys. The buffer mirrors live into filter.Search on every
// keystroke; Enter closes the bar leaving the filter applied.
//
// The filter changes are *client-side* (filteredIssues), so no API
// refetch fires for Search/Owner — only Status filter changes
// dispatch a refetch.
func TestList_Search_AccumulatesAndCommits(t *testing.T) {
	m := mFixtureForBar()
	m, _ = stepModel(m, runeKey('/'))
	// Drive openInputMsg through the model so the bar opens.
	m = openBarFromCmd(t, m, '/')
	if m.input.kind != inputSearchBar {
		t.Fatalf("expected inputSearchBar active, got kind=%v", m.input.kind)
	}
	for _, r := range "abc" {
		m, _ = stepModel(m, runeKey(r))
	}
	if m.list.filter.Search != "abc" {
		t.Fatalf("filter.Search = %q, want abc (live mirror)", m.list.filter.Search)
	}
	m, _ = stepModel(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.input.kind != inputNone {
		t.Fatalf("input.kind = %v, want inputNone after Enter", m.input.kind)
	}
	if m.list.filter.Search != "abc" {
		t.Fatalf("filter.Search = %q, want abc (preserved on commit)", m.list.filter.Search)
	}
}

// TestList_Search_EscCancels confirms Esc reverts filter.Search to
// the pre-open snapshot and closes the bar.
//
// The bar pre-fills with the existing filter value so the user can
// refine an active search without retyping; appending "xyz" to a
// pre-filled "previous" produces "previousxyz" live, then Esc
// restores "previous".
func TestList_Search_EscCancels(t *testing.T) {
	m := mFixtureForBar()
	m.list.filter.Search = "previous"
	m = openBarFromCmd(t, m, '/')
	for _, r := range "xyz" {
		m, _ = stepModel(m, runeKey(r))
	}
	if m.list.filter.Search != "previousxyz" {
		t.Fatalf("filter.Search = %q, want previousxyz (live during edit)",
			m.list.filter.Search)
	}
	m, _ = stepModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.input.kind != inputNone {
		t.Fatal("input.kind must be inputNone after Esc")
	}
	if m.list.filter.Search != "previous" {
		t.Fatalf("filter.Search not restored: got %q, want %q",
			m.list.filter.Search, "previous")
	}
}

// mFixtureForBar builds a minimal Model with the bare-minimum state
// the M3a bar tests need: a list, a keymap, no api/sse goroutine, no
// scope. Used by every M3a-style test that drives Model.Update for
// search/owner bar behavior.
func mFixtureForBar() Model {
	return Model{
		view:   viewList,
		keymap: newKeymap(),
		list:   listModel{actor: "tester"},
		cache:  newIssueCache(),
	}
}

// stepModel is the test-side equivalent of dispatching one tea.Msg
// through Model.Update. Returns the new Model + any tea.Cmd the
// dispatch produced.
func stepModel(m Model, msg tea.Msg) (Model, tea.Cmd) {
	out, cmd := m.Update(msg)
	return out.(Model), cmd
}

// openBarFromCmd presses key, expects an openInputCmd to come back,
// invokes that cmd to obtain openInputMsg, and feeds the message
// back into Model.Update so the bar actually opens. Returns the
// resulting Model with the bar active.
func openBarFromCmd(t *testing.T, m Model, key rune) Model {
	t.Helper()
	out, cmd := m.Update(runeKey(key))
	m = out.(Model)
	if cmd == nil {
		t.Fatalf("press %q produced no cmd; expected openInputCmd", string(key))
	}
	msg := cmd()
	out, _ = m.Update(msg)
	return out.(Model)
}

// TestList_ClearFilters_ZeroesEveryField: `c` zeroes every filter slot
// and does not dispatch a refetch. There is no IncludeDeleted slot today (see
// ListFilter doc) so the post-state is the zero value.
func TestList_ClearFilters_ZeroesEveryField(t *testing.T) {
	api, km, sc := newListEnv()
	lm := listModel{filter: ListFilter{
		Status: "open", Owner: "wes", Search: "bug",
		Labels: []string{"prio-1"},
	}}
	lm, cmd := lm.Update(runeKey('c'), km, api, sc)
	if lm.filter.Status != "" || lm.filter.Owner != "" || lm.filter.Search != "" ||
		len(lm.filter.Labels) != 0 {
		t.Fatalf("filters not cleared: %+v", lm.filter)
	}
	if cmd != nil {
		t.Fatalf("expected nil cmd on clear, got %T", cmd)
	}
}

func TestList_ApplyFetched_SetsTruncatedAboveWorkingSetLimitAndTrims(t *testing.T) {
	issues := make([]Issue, queueFetchLimit)
	for i := range issues {
		issues[i] = Issue{Number: int64(i + 1)}
	}

	lm := listModel{}.applyFetched(initialFetchMsg{issues: issues})
	if !lm.truncated {
		t.Fatal("truncated = false, want true for sentinel row")
	}
	if len(lm.issues) != queueWorkingSetLimit {
		t.Fatalf("len(lm.issues) = %d, want %d", len(lm.issues), queueWorkingSetLimit)
	}

	exact := make([]Issue, queueWorkingSetLimit)
	lm = listModel{}.applyFetched(initialFetchMsg{issues: exact})
	if lm.truncated {
		t.Fatal("truncated = true, want false when sentinel row is absent")
	}
	if len(lm.issues) != queueWorkingSetLimit {
		t.Fatalf("exact len(lm.issues) = %d, want %d", len(lm.issues), queueWorkingSetLimit)
	}
}

// TestList_Cursor_MovesInFilteredSpace: with a filter active, j/k
// moves the cursor through filtered rows. Regression for finding 29:
// previously j moved through all issues and the marker landed on the
// wrong (sometimes invisible) row.
func TestList_Cursor_MovesInFilteredSpace(t *testing.T) {
	api, km, sc := newListEnv()
	lm := listModel{
		filter: ListFilter{Owner: "alice"},
		issues: []Issue{
			{Number: 1, Owner: ptrString("alice"), Title: "a"},
			{Number: 2, Owner: ptrString("bob"), Title: "b"},
			{Number: 3, Owner: ptrString("alice"), Title: "c"},
			{Number: 4, Owner: ptrString("bob"), Title: "d"},
		},
	}
	// Two filtered rows (1 and 3). j once → cursor=1 (the second
	// filtered row, #3). j again clamps at len(filtered)-1=1.
	lm, _ = lm.Update(runeKey('j'), km, api, sc)
	if lm.cursor != 1 {
		t.Fatalf("after j: cursor = %d, want 1", lm.cursor)
	}
	lm, _ = lm.Update(runeKey('j'), km, api, sc)
	if lm.cursor != 1 {
		t.Fatalf("after second j: cursor = %d, want 1 (clamped)", lm.cursor)
	}
	// targetRow must point at filtered[1] = issue #3.
	iss, ok := lm.targetRow()
	if !ok || iss.Number != 3 {
		t.Fatalf("targetRow = (%+v, %v), want #3", iss, ok)
	}
}

func TestList_ExpandCollapse(t *testing.T) {
	api, km, sc := newListEnv()
	lm := listModel{
		issues: []Issue{
			{ProjectID: 7, Number: 1, ChildCounts: &ChildCounts{Open: 1, Total: 1}},
			{ProjectID: 7, Number: 2, ParentNumber: int64Ptr(1)},
		},
	}

	lm, cmd := lm.Update(tea.KeyMsg{Type: tea.KeySpace}, km, api, sc)
	if cmd != nil {
		t.Fatalf("space should not dispatch a command, got %T", cmd)
	}
	if len(lm.visibleRows()) != 2 {
		t.Fatalf("expanded visible rows = %+v, want parent+child", lm.visibleRows())
	}
	lm, _ = lm.Update(tea.KeyMsg{Type: tea.KeySpace}, km, api, sc)
	if len(lm.visibleRows()) != 1 {
		t.Fatalf("collapsed visible rows = %+v, want parent only", lm.visibleRows())
	}
}

func TestList_ExpandCollapse_LeafNoOp(t *testing.T) {
	api, km, sc := newListEnv()
	lm := listModel{issues: []Issue{{ProjectID: 7, Number: 1}}}

	next, cmd := lm.Update(tea.KeyMsg{Type: tea.KeySpace}, km, api, sc)
	if cmd != nil {
		t.Fatalf("space on leaf should not dispatch a command, got %T", cmd)
	}
	if len(next.expanded) != 0 {
		t.Fatalf("expanded = %+v, want empty on leaf", next.expanded)
	}
}

func TestList_SelectionPreservedAcrossRefetchWithParentInsertion(t *testing.T) {
	parentNumber := int64(1)
	lm := listModel{
		issues:            []Issue{{ProjectID: 7, Number: 2, Title: "child"}},
		selectedNumber:    2,
		selectedProjectID: 7,
	}

	lm = lm.applyFetched(refetchedMsg{issues: []Issue{
		{ProjectID: 7, Number: 1, Title: "parent", ChildCounts: &ChildCounts{Open: 1, Total: 1}},
		{ProjectID: 7, Number: 2, Title: "child", ParentNumber: &parentNumber},
	}})
	iss, ok := lm.targetRow()
	if !ok || iss.Number != 2 {
		t.Fatalf("targetRow = (%+v, %v), want selected child #2", iss, ok)
	}
	if !lm.expanded[issueKey{projectID: 7, number: 1}] {
		t.Fatalf("parent was not auto-expanded: %+v", lm.expanded)
	}
}

func TestList_SelectionClampsWhenFilterHidesSelectedChild(t *testing.T) {
	lm := listModel{
		filter:            ListFilter{Status: "closed"},
		cursor:            1,
		selectedNumber:    2,
		selectedProjectID: 7,
	}

	lm = lm.applyFetched(refetchedMsg{issues: []Issue{
		{ProjectID: 7, Number: 1, Status: "closed", Title: "visible"},
		{ProjectID: 7, Number: 2, Status: "open", Title: "hidden"},
	}})
	iss, ok := lm.targetRow()
	if !ok || iss.Number != 1 {
		t.Fatalf("targetRow = (%+v, %v), want fallback visible #1", iss, ok)
	}
	if lm.selectedNumber != 1 {
		t.Fatalf("selectedNumber = %d, want 1 after clamp", lm.selectedNumber)
	}
}

// TestList_NewIssue_AllProjectsModeIsNoOp: in cross-project view there
// is no projectID to create against, so 'n' should not open the prompt
// and should leave a status hint.
func TestList_NewIssue_AllProjectsModeIsNoOp(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{allProjects: true}

	lm, cmd := lmFromUpdate(listModel{}, runeKey('n'), km, api, sc)
	if cmd != nil {
		t.Fatalf("expected no openInputCmd in all-projects mode, got %T", cmd)
	}
	if lm.status == "" {
		t.Fatal("expected status hint explaining the no-op")
	}
	if api.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0", api.createCalls)
	}
}

// TestList_NewIssueCreateSeedsSelectionToNewIssue: after a successful
// create, the next refetch must land the cursor on the newly-created
// issue (which lands at index 0 in a recency-sorted list) instead of
// snapping back to whatever was selected before. Regression for
// roborev #113 finding 2; preserved across the inline-row → centered-
// form refactor because the seed lives in lm.applyMutation, not the
// inline-row code path.
func TestList_NewIssueCreateSeedsSelectionToNewIssue(t *testing.T) {
	api := &fakeListAPI{}
	lm := listModel{
		issues:         []Issue{{Number: 5}, {Number: 4}, {Number: 3}},
		cursor:         2,
		selectedNumber: 3,
	}
	mut := mutationDoneMsg{
		origin: "list", kind: "create",
		resp: &MutationResp{Issue: &Issue{Number: 99}},
	}
	out, _ := lm.applyMutation(mut, api, scope{projectID: 7})
	if out.selectedNumber != 99 {
		t.Fatalf("selectedNumber = %d, want 99 (seeded to new issue)",
			out.selectedNumber)
	}
	if out.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (new issue at top of recency list)",
			out.cursor)
	}
}

// TestList_OKey_NoLongerOpensOwnerBar (Plan 8 commit 5a): pressing
// `o` from the list view must NOT open any input shell. The owner
// bar was retired when `f` (filter modal) subsumed the owner-by-itself
// gesture; `o` is now a plain unhandled key on the list. Regression
// catch for accidentally re-binding `o` to inputSearchBar or any other
// shell.
func TestList_OKey_NoLongerOpensOwnerBar(t *testing.T) {
	m := mFixtureForBar()
	m, _ = stepModel(m, runeKey('o'))
	if m.input.kind != inputNone {
		t.Fatalf("'o' opened input shell: kind=%v", m.input.kind)
	}
}

// TestList_LabelKey_NoLongerOpensPrompt: pressing 'l' from the list
// must NOT open any input shell. The label-filter UI was retired
// because the wire doesn't carry Labels yet (matchesFilter could not
// honor it). Regression catch for accidentally rebinding 'l' before
// the wire surface lands.
func TestList_LabelKey_NoLongerOpensPrompt(t *testing.T) {
	m := mFixtureForBar()
	m, _ = stepModel(m, runeKey('l'))
	if m.input.kind != inputNone {
		t.Fatalf("'l' opened input shell: kind=%v", m.input.kind)
	}
}

// TestList_BackspaceTrimsBuffer: backspace inside the active inline
// command bar deletes the last rune. The bubbles textinput handles
// the actual edit; Model.routeInputKey forwards the key through
// inputState.Update.
func TestList_BackspaceTrimsBuffer(t *testing.T) {
	m := mFixtureForBar()
	m = openBarFromCmd(t, m, '/')
	for _, r := range "abc" {
		m, _ = stepModel(m, runeKey(r))
	}
	m, _ = stepModel(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if got := m.input.activeField().value(); got != "ab" {
		t.Fatalf("bar value = %q, want ab", got)
	}
	if m.list.filter.Search != "ab" {
		t.Fatalf("filter.Search = %q, want ab (mirrored after backspace)", m.list.filter.Search)
	}
}

// TestList_QuitGate_RoutesQuitToBuffer covers the model-level gate: a
// 'q' keystroke while the inline command bar is open must reach the
// bar's buffer instead of triggering tea.Quit. After M3a, the bar
// lives on m.input — canQuit() returns false when m.input.kind !=
// inputNone so routeGlobalKey doesn't match.
func TestList_QuitGate_RoutesQuitToBuffer(t *testing.T) {
	m := initialModel(Options{})
	m.scope = scope{projectID: 7}
	m.list.loading = false
	m = openBarFromCmd(t, m, '/')
	if m.input.kind != inputSearchBar {
		t.Fatalf("bar did not open, got kind=%v", m.input.kind)
	}
	out, cmd := m.Update(runeKey('q'))
	m = out.(Model)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, isQuit := msg.(tea.QuitMsg); isQuit {
				t.Fatalf("q produced tea.Quit; should have reached the bar buffer")
			}
		}
	}
	if got := m.input.activeField().value(); got != "q" {
		t.Fatalf("bar buffer = %q, want q (q routed to input)", got)
	}
}

// TestList_RefetchError_PutsErrOnModel ensures fetch failures surface in
// lm.err so View renders the error state and the user can retry.
func TestList_RefetchError_PutsErrOnModel(t *testing.T) {
	api, km, sc := newListEnv()
	api.listIssuesErr = errors.New("boom")

	lm := listModel{}
	cmd := lm.refetchCmd(api, sc)
	if cmd == nil {
		t.Fatal("expected refetch")
	}
	lm = drainCmd(t, lm, cmd, km, api, sc)
	if lm.err == nil || lm.err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", lm.err)
	}
}

func TestList_RefetchUsesQueueFetchFilter(t *testing.T) {
	api := &fakeListAPI{}
	sc := scope{projectID: 7}
	lm := listModel{
		filter: ListFilter{Status: "closed", Owner: "alice", Search: "bug", Labels: []string{"ux"}},
	}

	cmd := lm.refetchCmd(api, sc)
	if cmd == nil {
		t.Fatal("expected refetch cmd")
	}
	_ = cmd()
	if api.lastListFilter.Limit != queueFetchLimit {
		t.Fatalf("Limit = %d, want %d", api.lastListFilter.Limit, queueFetchLimit)
	}
	if api.lastListFilter.Status != "" {
		t.Fatalf("Status = %q, want empty all-status fetch", api.lastListFilter.Status)
	}
	if api.lastListFilter.Owner != "" || api.lastListFilter.Search != "" || len(api.lastListFilter.Labels) != 0 {
		t.Fatalf("render filters leaked into fetch filter: %+v", api.lastListFilter)
	}
}

// lmFromUpdate is a one-line wrapper around lm.Update so the test code
// that doesn't care about the cmd doesn't have to declare extra vars.
// The signature mirrors lm.Update so callers can drop in whichever they
// need.
func lmFromUpdate(
	lm listModel, msg tea.Msg, km keymap, api listAPI, sc scope,
) (listModel, tea.Cmd) {
	return lm.Update(msg, km, api, sc)
}

// TestList_OwnerFilter_NarrowsDisplay confirms filteredIssues drops
// rows whose Owner does not match. The fixture exercises the *string
// branch (alice matches twice, bob is filtered out, nil-owner case is
// covered by TestList_NoFilter_PassThrough).
func TestList_OwnerFilter_NarrowsDisplay(t *testing.T) {
	issues := []Issue{
		{Number: 1, Owner: ptrString("alice"), Title: "a"},
		{Number: 2, Owner: ptrString("bob"), Title: "b"},
		{Number: 3, Owner: ptrString("alice"), Title: "c"},
	}
	out := filteredIssues(issues, ListFilter{Owner: "alice"})
	if len(out) != 2 {
		t.Fatalf("got %d, want 2", len(out))
	}
	if out[0].Number != 1 || out[1].Number != 3 {
		t.Fatalf("wrong issues filtered: %+v", out)
	}
}

// TestList_OwnerFilter_NilOwnerNeverMatches: a row with no owner can
// never satisfy a set Owner filter, even if the filter string is
// empty. (Empty filter is the no-filter fast path; non-empty plus nil
// owner is the case under test here.)
func TestList_OwnerFilter_NilOwnerNeverMatches(t *testing.T) {
	issues := []Issue{
		{Number: 1, Title: "no owner"},
		{Number: 2, Owner: ptrString("alice"), Title: "owned"},
	}
	out := filteredIssues(issues, ListFilter{Owner: "alice"})
	if len(out) != 1 || out[0].Number != 2 {
		t.Fatalf("expected only #2, got %+v", out)
	}
}

// TestList_SearchFilter_CaseInsensitive: the search box is forgiving
// about case so users typing "login" find "LOGIN bug" and vice versa.
func TestList_SearchFilter_CaseInsensitive(t *testing.T) {
	issues := []Issue{
		{Number: 1, Title: "Fix LOGIN bug"},
		{Number: 2, Title: "deploy"},
	}
	out := filteredIssues(issues, ListFilter{Search: "login"})
	if len(out) != 1 || out[0].Number != 1 {
		t.Fatalf("expected #1 only, got %+v", out)
	}
}

// TestList_NoFilter_PassThrough: with no client-side filter set the
// fast path returns the input unchanged so the steady state pays no
// per-render allocation.
func TestList_NoFilter_PassThrough(t *testing.T) {
	issues := []Issue{
		{Number: 1, Owner: ptrString("alice"), Title: "a"},
		{Number: 2, Title: "b"},
	}
	out := filteredIssues(issues, ListFilter{})
	if len(out) != 2 {
		t.Fatalf("expected pass-through, got %d", len(out))
	}
}

// TestFilteredIssues_FastPathIncludesLabels (Plan 8 commit 5b
// hard invariant): filteredIssues' fast-path returns the input slice
// unchanged ONLY when every client-side filter is empty — Labels
// included. Pre-fix the early-return ignored f.Labels, so a
// label-only filter narrowed nothing on the steady path.
func TestFilteredIssues_FastPathIncludesLabels(t *testing.T) {
	f := ListFilter{Labels: []string{"bug"}}
	issues := []Issue{
		{Number: 1, Labels: []string{"bug"}},
		{Number: 2, Labels: []string{"feature"}},
	}
	got := filteredIssues(issues, f)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (label filter must narrow)", len(got))
	}
	if got[0].Number != 1 {
		t.Fatalf("got #%d, want #1 (the labeled-bug row)", got[0].Number)
	}
}

// TestMatchesFilter_LabelsAnyOfSemantics (Plan 8 commit 5b hard
// invariant): the Labels axis on the filter modal uses any-of
// semantics — an issue matches if ANY of its labels is present in
// the filter's Labels slice. Empty filter Labels is the no-filter
// case (every issue matches).
func TestMatchesFilter_LabelsAnyOfSemantics(t *testing.T) {
	iss := Issue{Number: 1, Labels: []string{"bug", "prio-1"}}
	cases := []struct {
		name   string
		filter ListFilter
		want   bool
	}{
		{"single matching label", ListFilter{Labels: []string{"bug"}}, true},
		{"any-of mixed match/miss", ListFilter{Labels: []string{"bug", "foo"}}, true},
		{"no overlap", ListFilter{Labels: []string{"foo"}}, false},
		{"empty filter is no filter", ListFilter{Labels: []string{}}, true},
		{"nil filter is no filter", ListFilter{Labels: nil}, true},
	}
	for _, c := range cases {
		got := matchesFilter(iss, c.filter)
		if got != c.want {
			t.Errorf("%s: matchesFilter = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestMatchesFilter_LabelsAnyOf_EmptyIssueLabels: an issue with no
// labels can never match a non-empty Labels filter (the any-of set is
// empty, so no overlap with any non-empty filter slice).
func TestMatchesFilter_LabelsAnyOf_EmptyIssueLabels(t *testing.T) {
	iss := Issue{Number: 1, Labels: nil}
	if matchesFilter(iss, ListFilter{Labels: []string{"bug"}}) {
		t.Fatal("issue with no labels must not match any non-empty Labels filter")
	}
}

// TestList_AuthorFilter_NarrowsDisplay covers the Author branch even
// though there's no key binding for it yet — ListFilter.Author is on
// the struct (Task 6 left it in for forward compat) and matchesFilter
// honors it. When a future task adds an `a` keystroke to filter by
// author, this test guards the wiring.
func TestList_AuthorFilter_NarrowsDisplay(t *testing.T) {
	issues := []Issue{
		{Number: 1, Author: "wes", Title: "a"},
		{Number: 2, Author: "claude", Title: "b"},
		{Number: 3, Author: "wes", Title: "c"},
	}
	out := filteredIssues(issues, ListFilter{Author: "wes"})
	if len(out) != 2 || out[0].Number != 1 || out[1].Number != 3 {
		t.Fatalf("wrong issues filtered: %+v", out)
	}
}

// TestList_Close_DispatchesAPI: j to row 2, 'x' calls api.Close with
// the row 2 issue's number, threading the actor through. The fixture
// uses two rows so cursor!=0 is observable.
func TestList_Close_DispatchesAPI(t *testing.T) {
	api, km, sc := newListEnv()
	api.closeResult = &MutationResp{Issue: &Issue{Number: 2, Status: "closed"}}
	lm := listModel{
		actor: "tester",
		issues: []Issue{
			{ProjectID: 7, Number: 1, Title: "first"},
			{ProjectID: 7, Number: 2, Title: "second"},
		},
	}

	lm, _ = lm.Update(runeKey('j'), km, api, sc)
	if lm.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 after j", lm.cursor)
	}
	lm, cmd := lm.Update(runeKey('x'), km, api, sc)
	if cmd == nil {
		t.Fatal("expected close cmd from x")
	}
	_ = drainCmd(t, lm, cmd, km, api, sc)
	if api.closeCalls != 1 {
		t.Fatalf("closeCalls = %d, want 1", api.closeCalls)
	}
	if api.lastCloseProjectID != 7 || api.lastCloseNumber != 2 {
		t.Fatalf("close args wrong: pid=%d num=%d",
			api.lastCloseProjectID, api.lastCloseNumber)
	}
	if api.lastCloseActor != "tester" {
		t.Fatalf("lastCloseActor = %q, want tester", api.lastCloseActor)
	}
}

// TestList_Reopen_DispatchesAPI mirrors TestList_Close_DispatchesAPI for
// the 'r' binding.
func TestList_Reopen_DispatchesAPI(t *testing.T) {
	api, km, sc := newListEnv()
	api.reopenResult = &MutationResp{Issue: &Issue{Number: 1, Status: "open"}}
	lm := listModel{
		actor: "tester",
		issues: []Issue{
			{ProjectID: 7, Number: 1, Title: "first"},
		},
	}

	lm, cmd := lm.Update(runeKey('r'), km, api, sc)
	if cmd == nil {
		t.Fatal("expected reopen cmd from r")
	}
	_ = drainCmd(t, lm, cmd, km, api, sc)
	if api.reopenCalls != 1 {
		t.Fatalf("reopenCalls = %d, want 1", api.reopenCalls)
	}
	if api.lastReopenNumber != 1 || api.lastReopenActor != "tester" {
		t.Fatalf("reopen args wrong: num=%d actor=%q",
			api.lastReopenNumber, api.lastReopenActor)
	}
}

// TestList_SetPriority_PendingThenDigit: pressing `!` arms pending
// mode; the next digit (0..4) dispatches SetPriority for the cursor row
// with the corresponding *int64. The pending hint must clear after the
// dispatch so the success status text is what the user sees.
func TestList_SetPriority_PendingThenDigit(t *testing.T) {
	api, km, sc := newListEnv()
	api.setPriorityResult = &MutationResp{Issue: &Issue{Number: 1, Status: "open"}}
	lm := listModel{
		actor: "tester",
		issues: []Issue{
			{ProjectID: 7, Number: 1, Title: "issue"},
		},
	}

	lm, cmd := lm.Update(runeKey('!'), km, api, sc)
	if cmd != nil {
		t.Fatalf("`!` must not return a cmd (just arms pending), got %T", cmd)
	}
	if !lm.pendingPriority {
		t.Fatal("pendingPriority must be true after `!`")
	}
	if lm.status == "" {
		t.Fatal("pending hint must be set so user can see what's expected")
	}

	lm, cmd = lm.Update(runeKey('2'), km, api, sc)
	if cmd == nil {
		t.Fatal("expected SetPriority cmd from `2`")
	}
	if lm.pendingPriority {
		t.Fatal("pendingPriority must clear once the digit is consumed")
	}
	_ = drainCmd(t, lm, cmd, km, api, sc)
	if api.setPriorityCalls != 1 {
		t.Fatalf("setPriorityCalls = %d, want 1", api.setPriorityCalls)
	}
	if api.lastPriorityNumber != 1 {
		t.Fatalf("lastPriorityNumber = %d, want 1", api.lastPriorityNumber)
	}
	if api.lastPriorityValue == nil || *api.lastPriorityValue != 2 {
		t.Fatalf("lastPriorityValue = %v, want *int64=2", api.lastPriorityValue)
	}
	if api.lastPriorityActor != "tester" {
		t.Fatalf("lastPriorityActor = %q, want tester", api.lastPriorityActor)
	}
}

// TestList_SetPriority_ClearWithDash: `!` followed by `-` clears the
// priority via a nil pointer.
func TestList_SetPriority_ClearWithDash(t *testing.T) {
	api, km, sc := newListEnv()
	api.setPriorityResult = &MutationResp{Issue: &Issue{Number: 1, Status: "open"}}
	priority := int64(3)
	lm := listModel{
		actor: "tester",
		issues: []Issue{
			{ProjectID: 7, Number: 1, Title: "issue", Priority: &priority},
		},
	}

	lm, _ = lm.Update(runeKey('!'), km, api, sc)
	lm, cmd := lm.Update(runeKey('-'), km, api, sc)
	if cmd == nil {
		t.Fatal("expected SetPriority cmd from `-`")
	}
	_ = drainCmd(t, lm, cmd, km, api, sc)
	if api.setPriorityCalls != 1 {
		t.Fatalf("setPriorityCalls = %d, want 1", api.setPriorityCalls)
	}
	if api.lastPriorityValue != nil {
		t.Fatalf("lastPriorityValue = %v, want nil (clear)", api.lastPriorityValue)
	}
}

// TestList_SetPriority_EscCancels: `!` then esc cancels pending mode
// without dispatching anything.
func TestList_SetPriority_EscCancels(t *testing.T) {
	api, km, sc := newListEnv()
	lm := listModel{
		actor: "tester",
		issues: []Issue{
			{ProjectID: 7, Number: 1, Title: "issue"},
		},
	}

	lm, _ = lm.Update(runeKey('!'), km, api, sc)
	lm, cmd := lm.Update(tea.KeyMsg{Type: tea.KeyEsc}, km, api, sc)
	if cmd != nil {
		t.Fatalf("esc must not produce a cmd, got %T", cmd)
	}
	if lm.pendingPriority {
		t.Fatal("pendingPriority must clear on esc")
	}
	if api.setPriorityCalls != 0 {
		t.Fatalf("setPriorityCalls = %d, want 0", api.setPriorityCalls)
	}
}

// TestList_SetPriority_EmptyListNoOp: `!` on an empty list does not
// arm pending mode (no row to act on).
func TestList_SetPriority_EmptyListNoOp(t *testing.T) {
	api, km, sc := newListEnv()
	lm := listModel{actor: "tester"}

	lm, cmd := lm.Update(runeKey('!'), km, api, sc)
	if cmd != nil {
		t.Fatalf("expected nil cmd on empty list, got %T", cmd)
	}
	if lm.pendingPriority {
		t.Fatal("pendingPriority must not arm on empty list")
	}
}

// TestList_Close_EmptyListNoOp: 'x' on an empty list does not call
// api.Close and does not panic.
func TestList_Close_EmptyListNoOp(t *testing.T) {
	api, km, sc := newListEnv()
	lm := listModel{actor: "tester"}

	_, cmd := lm.Update(runeKey('x'), km, api, sc)
	if cmd != nil {
		t.Fatalf("expected nil cmd on empty list, got %T", cmd)
	}
	if api.closeCalls != 0 {
		t.Fatalf("closeCalls = %d, want 0", api.closeCalls)
	}
}
