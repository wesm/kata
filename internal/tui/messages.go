package tui

import "time"

// initialFetchMsg is delivered after the first ListIssues call returns.
// dispatchKey captures the scope/filter at dispatch time so
// Model.populateCache can drop stale responses that arrive after a
// scope toggle or filter change.
type initialFetchMsg struct {
	dispatchKey cacheKey
	issues      []Issue
	err         error
}

// refetchedMsg carries the result of a debounced or scope-change
// refetch. dispatchKey captures the scope/filter at dispatch time so
// Model.populateCache can drop stale responses — see initialFetchMsg.
type refetchedMsg struct {
	dispatchKey cacheKey
	issues      []Issue
	err         error
}

// detailFetchedMsg carries the result of a single-issue refetch. It is
// produced by the Enter-jump path (Task 8) when a user navigates to a
// referenced issue, and will also be produced by the SSE-driven refetch
// in Task 11. gen tags the detail-open generation that dispatched the
// fetch — applyFetched discards messages whose gen no longer matches
// dm.gen so a fetch in flight when the user pops/jumps cannot pollute
// the new view with stale data.
type detailFetchedMsg struct {
	gen      int64
	issue    *Issue
	parent   *IssueRef
	children []Issue
	err      error
}

// commentsFetchedMsg, eventsFetchedMsg, and linksFetchedMsg carry the
// per-tab fetch results dispatched in parallel by openDetailMsg. gen
// is the detail-open generation; see detailFetchedMsg for the rationale.
type commentsFetchedMsg struct {
	gen      int64
	comments []CommentEntry
	err      error
}

type eventsFetchedMsg struct {
	gen    int64
	events []EventLogEntry
	err    error
}

type linksFetchedMsg struct {
	gen   int64
	links []LinkEntry
	err   error
}

// openDetailMsg is emitted by the list view when Enter selects a row.
// The top-level Model handles it: switches m.view to viewDetail, seeds
// m.detail.issue, and dispatches the three concurrent tab fetches.
type openDetailMsg struct {
	issue Issue
}

// popDetailMsg reverts the top-level Model from viewDetail back to
// viewList. The list cursor and filter state are preserved because
// listModel is held by value and never reset on the round trip.
type popDetailMsg struct{}

// openInputMsg asks the top-level Model to open an input shell of
// the given kind. Sub-views emit this rather than constructing the
// inputState directly so the centralised Model.Update routes the
// open through the shared input pipeline (snapshot/restore, focus
// gating, render integration).
type openInputMsg struct {
	kind         inputKind
	parentNumber *int64
}

// jumpDetailMsg asks the top-level Model to jump the detail view to
// the named issue. detail.handleEnter emits this message rather than
// performing the jump itself so the new generation comes from
// Model.nextGen — a counter that monotonically increases across the
// model's lifetime, never reused even after handleBack restores an
// older detailModel snapshot from the navStack. Without the hoist,
// jumpTo's `dm.gen+1` could collide with an in-flight fetch from a
// previously-jumped issue once the user backs to the smaller-gen
// snapshot and re-jumps.
type jumpDetailMsg struct {
	number int64
}

// mutationDoneMsg is the result of any single mutation (create now,
// close/reopen/label/owner in Task 9). kind names which mutation so the
// list/detail Update can route to the right post-success behavior.
//
// origin discriminates which view dispatched the mutation: "list"
// mutations land in listModel.applyMutation, "detail" mutations land in
// detailModel.applyMutation, "form" mutations land in routeFormMutation.
// Without this tag, a list-side close completing after the user opened
// detail (or a detail close that arrives after Esc) would route the
// response to the wrong view, churn the wrong status line, and (for
// detail) trigger an unwanted refetch.
//
// gen is the detail-open generation that dispatched the mutation, set
// only when origin == "detail". The detail Update path drops responses
// whose gen does not match dm.gen so a mutation in flight when the user
// jumps or pops cannot apply to the new view.
//
// formGen is the centered-form generation captured at dispatch time
// (set only when origin == "form"). routeFormMutation drops responses
// whose formGen does not match m.input.formGen — guards against a
// stale form-A response landing on a different form-B that the user
// opened in the meantime. Without this guard, the stale response could
// close form B, misroute as origin=detail, or fire an unrelated
// batchLabelRefresh against form A's project. Jobs 242/244.
type mutationDoneMsg struct {
	origin  string
	gen     int64
	formGen int64
	kind    string
	resp    *MutationResp
	err     error
}

// editorReturnedMsg carries the result of a $EDITOR suspend/resume
// cycle. kind discriminates which mutation should run on the trimmed
// content: "edit" (issue body) or "comment" (new comment). err is
// non-nil when the editor exited with a non-zero status or the
// tmpfile read-back failed.
//
// formGen is the M4 stale-handoff guard. The Model.openBodyEditForm /
// openCommentForm path stamps a monotonic ID into the form state and
// rides it through editorCmd. Routing checks the return's formGen
// against the currently-open form's formGen; a mismatch (form was
// closed, or re-opened on a different issue while the editor ran)
// drops the content rather than writing it into a different form's
// textarea. formGen=0 means "legacy editor return" and follows the
// pre-M4 detail-side dispatch path.
type editorReturnedMsg struct {
	kind, content string
	err           error
	formGen       int64
}

// eventReceivedMsg is the per-frame SSE message forwarded to the TEA
// loop by startSSE. issueNumber is zero when the event has no
// associated issue (project-level events).
type eventReceivedMsg struct {
	eventType              string
	projectID, issueNumber int64
	projectUID             string
	issueUID               string
	relatedIssueUID        string
	link                   *linkPayload
}

type linkPayload struct {
	Type         string `json:"type"`
	FromNumber   int64  `json:"from_number"`
	ToNumber     int64  `json:"to_number"`
	FromIssueUID string `json:"from_issue_uid,omitempty"`
	ToIssueUID   string `json:"to_issue_uid,omitempty"`
}

// labelsFetchedMsg carries the result of an api.ListLabels call. pid
// names which project the labels are for; gen is the dispatch-time
// generation so handleLabelsFetched can drop stale responses (a
// later dispatch under the same pid bumps the cache's gen, and any
// older response arriving after must NOT overwrite the newer state).
type labelsFetchedMsg struct {
	pid    int64
	gen    int64
	labels []LabelCount
	err    error
}

// projectsLoadedMsg is delivered after a /api/v1/projects fetch returns.
// The all-projects list view uses the projects map to prefix each row's
// title with the owning project's display name. The stats and idents
// maps are populated only by fetchProjectsWithStats; the boot
// fetchProjects cmd leaves them nil so callers can distinguish "names
// only" vs "with stats".
//
// gen carries the Model.projectsGen value captured at dispatch time. The
// projectsLoadedMsg handler clears m.projectsStale only when gen still
// matches m.projectsGen — guards against an SSE invalidation arriving
// while an older fetch is in flight. fetchProjects (the boot, no-stats
// variant) leaves gen=0 since it does not participate in the stale-flip
// race. Spec §6.3.
type projectsLoadedMsg struct {
	projects map[int64]string
	idents   map[int64]string
	stats    map[int64]ProjectStatsSummary
	err      error
	gen      uint64
}

// resetRequiredMsg signals sync.reset_required: the daemon's purge
// gap means the consumer's cursor is too old. The TEA loop drops the
// cache and refetches from scratch.
//
// We deliberately don't carry reset_after_id on this message: the
// daemon's contract (see internal/api/events.go EventReset) is that
// EventID == ResetAfterID, so the SSE frame's id: line — which the
// consumer already uses to update its Last-Event-ID resume cursor — is
// the authoritative checkpoint. A second copy of the same value on the
// envelope would invite drift if either path lagged.
type resetRequiredMsg struct{}

// sseStatusMsg carries connection-state transitions from the SSE
// goroutine to the TEA loop so the status bar can render the
// reconnect indicator.
type sseStatusMsg struct{ state sseConnState }

// sseConnState is the SSE consumer's connection state.
type sseConnState int

const (
	sseConnected sseConnState = iota
	sseReconnecting
	sseDisconnected
)

// refetchTickMsg fires after the 150ms debounce window so a single
// fetch covers a burst of events.
type refetchTickMsg struct{}

// detailFollowTickMsg fires after the 75ms M6 split-mode debounce
// window so a burst of cursor moves coalesces into one detail fetch.
// gen is the dispatch-time Model.nextDetailFollowGen value;
// handleDetailFollowTick drops a tick whose gen no longer matches
// m.nextDetailFollowGen — meaning the user has moved the cursor
// again and a fresher tick is in flight.
type detailFollowTickMsg struct {
	gen int64
}

// toastExpiredMsg fires after a toast's TTL so Update can clear it.
type toastExpiredMsg struct{}

// toast is a transient status notification rendered below the active
// view. Task 11 uses it for the 'resynced' notice; Task 12 will own
// stacked toasts for mutation feedback.
type toast struct {
	text      string
	level     toastLevel
	expiresAt time.Time
}

// toastLevel discriminates toast styling.
type toastLevel int

const (
	toastInfo toastLevel = iota
	//nolint:unused // reserved for mutation success toasts (deferred to Task 14)
	toastSuccess
	toastError
)
