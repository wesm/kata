// Package tui implements the kata terminal UI built on Bubble Tea.
package tui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/wesm/kata/internal/daemonclient"
)

// Options controls TUI behavior. Stable across versions; new fields
// must be optional.
//
// IncludeDeleted is intentionally absent: the daemon's ListIssuesRequest
// (internal/api/types.go) does not accept include_deleted, and
// db.ListIssues hard-codes deleted_at IS NULL, so there is no way for
// the TUI to surface soft-deleted rows today. Re-introducing the flag
// is deferred to a follow-up that adds wire + handler support.
//
// AllProjects is intentionally absent from Options: the boot flow
// always starts in single-project mode (resolved from the cwd) or empty
// state, and users toggle to all-projects via the R binding at runtime.
// Adding a CLI flag is reasonable as a future ergonomic but isn't
// required for the navigation surface.
type Options struct {
	Stdout           io.Writer // typically os.Stdout
	Stderr           io.Writer // typically os.Stderr
	DisplayUIDFormat string    // none, short, or full
}

// Run starts the TUI. Blocks until the user quits or ctx is cancelled.
// Returns nil on clean exit. Returns errNotATTY when stdin or the
// active output stream is not a terminal so callers can print a
// friendly message.
func Run(ctx context.Context, opts Options) error {
	if !isTerminal(os.Stdin) || !outputIsTerminal(opts.Stdout) {
		return errNotATTY
	}
	c, sseHC, sc, endpoint, err := bootClient(ctx, opts)
	if err != nil {
		return err
	}
	m := buildRunModel(opts, c, sc)
	sseCtx, cancelSSE := context.WithCancel(ctx)
	defer cancelSSE()
	if !sc.empty && sseHC != nil {
		go startSSE(sseCtx, sseHC, endpoint, sseProjectScope(sc), m.sseCh)
	}
	if _, err := tea.NewProgram(m, programOpts(ctx, opts)...).Run(); err != nil {
		return err
	}
	return nil
}

// buildRunModel seeds the initial model with the resolved client and
// scope. Splitting this off Run keeps the orchestration body small.
func buildRunModel(opts Options, c *Client, sc scope) Model {
	m := initialModel(opts)
	m.api = c
	m.scope = sc
	if sc.empty {
		m.view = viewEmpty
	}
	return m
}

// programOpts returns the tea.ProgramOption slice for tea.NewProgram.
// Splitting this off Run keeps Run's cyclomatic complexity within the
// project's ≤8 limit.
func programOpts(ctx context.Context, opts Options) []tea.ProgramOption {
	// Mouse capture is intentionally NOT enabled (plan §152): the TUI is
	// keyboard-first, and tea.WithMouseAllMotion would emit mouse-tracking
	// control sequences that prevent the user from selecting text natively
	// in the alt-screen. Add it back in a future plan that actually wires
	// MouseMsg handlers.
	out := []tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithAltScreen(),
	}
	if opts.Stdout != nil {
		out = append(out, tea.WithOutput(opts.Stdout))
	}
	return out
}

// sseProjectScope picks the project_id pointer to thread into startSSE.
// Always returns nil so the SSE stream carries every project's events
// regardless of the current scope. The TUI filters per-message via
// Model.eventAffectsView, so a user who toggles into all-projects mode
// (R binding) sees events from projects that weren't in scope at boot
// without restarting the SSE goroutine.
func sseProjectScope(_ scope) *int64 {
	return nil
}

// bootClient discovers the daemon, constructs the typed HTTP client, the
// streaming-only client used for SSE, and resolves the initial scope.
// Splitting this off Run keeps Run's cyclomatic complexity inside the
// project's ≤8 hard limit and isolates the network preflight from the
// Bubble Tea wiring.
//
// The SSE client is built with no overall Client.Timeout (only a 10s
// response-header ceiling) so a long-lived stream isn't reaped after 5s.
// We re-use NewHTTPClient with ResponseHeaderTimeout instead of building
// a bespoke transport so unix-socket dialing stays in one place.
func bootClient(ctx context.Context, _ Options) (*Client, *http.Client, scope, string, error) {
	endpoint, err := daemonclient.EnsureRunning(ctx)
	if err != nil {
		return nil, nil, scope{}, "", err
	}
	hc, err := daemonclient.NewHTTPClient(ctx, endpoint,
		daemonclient.Opts{Timeout: 5 * time.Second})
	if err != nil {
		return nil, nil, scope{}, "", err
	}
	sseHC, err := daemonclient.NewHTTPClient(ctx, endpoint,
		daemonclient.Opts{ResponseHeaderTimeout: daemonclient.SSEHandshakeTimeout})
	if err != nil {
		return nil, nil, scope{}, "", err
	}
	c := NewClient(endpoint, hc)
	cwd, _ := os.Getwd()
	sc, err := bootResolveScope(ctx, c, cwd)
	if err != nil {
		return nil, nil, scope{}, "", err
	}
	return c, sseHC, sc, endpoint, nil
}

// scope describes the issue-set the TUI is browsing. Exactly one of
// projectID, allProjects, empty is set.
//
// allProjects is currently always false: cross-project mode is gated
// off until the daemon ships a list endpoint (handlers_issues.go has
// no cross-project route). The field stays on the struct so the R
// toggle can be re-enabled in one place when daemon support lands.
//
// homeProjectID/homeProjectName capture the project bootResolveScope
// picked from the cwd. They're zero when boot landed in the empty
// state.
type scope struct {
	projectID       int64
	allProjects     bool
	empty           bool
	projectName     string
	workspace       string
	homeProjectID   int64
	homeProjectName string
}

// bootResolveScope picks the initial scope from the cwd. With cross-
// project mode gated off, the path is:
//
//  1. POST /projects/resolve(cwd) success → single-project mode.
//  2. project_not_initialized → empty state regardless of how many
//     projects are registered. The pre-gate code dropped into
//     all-projects when ≥1 project existed; that path now hits a 404
//     because the daemon has no cross-project list route. Empty state
//     is honest: the user gets the "run kata init" hint instead of an
//     error screen.
//  3. Any other resolve error → propagate so Run fails loudly.
func bootResolveScope(
	ctx context.Context, c *Client, cwd string,
) (scope, error) {
	rr, err := c.ResolveProject(ctx, cwd)
	if err == nil {
		return scope{
			projectID:       rr.Project.ID,
			projectName:     rr.Project.Name,
			workspace:       rr.WorkspaceRoot,
			homeProjectID:   rr.Project.ID,
			homeProjectName: rr.Project.Name,
		}, nil
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "project_not_initialized" {
		return scope{}, err
	}
	return scope{empty: true}, nil
}

// errNotATTY indicates the TUI was launched outside a terminal.
var errNotATTY = errors.New("kata tui requires a terminal (stdin/stdout must be a tty)")

// isTerminal reports whether f is connected to a real terminal. We use
// golang.org/x/term so /dev/null and other character devices do not
// pass (an os.ModeCharDevice check would let those through).
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd())) //nolint:gosec // G115: fd fits int on every supported OS.
}

// outputIsTerminal validates the writer the TUI will actually render to.
// A nil opts.Stdout means "use os.Stdout". Only *os.File values can be
// terminals — bytes.Buffer and other in-memory writers always fail this
// check so Run refuses to emit alt-screen control sequences into a sink
// that cannot honor them.
func outputIsTerminal(w io.Writer) bool {
	if w == nil {
		return isTerminal(os.Stdout)
	}
	if f, ok := w.(*os.File); ok {
		return isTerminal(f)
	}
	return false
}
