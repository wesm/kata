package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/wesm/kata/internal/textsafe"
)

// viewChrome carries the cross-cutting render inputs that lm.View
// and dm.View need to draw the title bar, stats line, info line, and
// footer help row. Plumbed from Model so the sub-views don't have to
// reach back into parent state. Zero-value renders a "minimal chrome"
// view (used by snapshot tests that exercise just the body) — no
// version, no SSE indicator text, no toast, no input.
//
// input is the active inline command bar / panel-local prompt /
// centered form. When input.kind is a command bar, the chrome
// renders the bar on the info line just above the footer (msgvault
// pattern) and swaps the help row to the bar's keys.
//
// narrow signals the list pane is rendering in M6 split mode. The
// list table drops the owner column when narrow=true so the title
// column has more cells to flex into (owner is redundant with the
// detail header's assignment row). Detail rendering ignores narrow
// (the detail pane in split mode flexes the same as in stacked).
type viewChrome struct {
	scope        scope            // project / counts / version go in the title bar
	sseStatus    sseConnState     // surfaces only as a flash when not connected
	pending      bool             // pendingRefetch — surfaces as a flash when set
	toast        *toast           // optional flash message (e.g. "resynced")
	version      string           // build-time version string for the title bar; "" hides
	input        inputState       // active input shell (M3a bar; M3b prompt; M4 form)
	narrow       bool             // M6 split mode list pane: drop owner column
	projectsByID map[int64]string // for all-projects mode: pid → display name (empty otherwise)
}

// View renders the list under the M3.5 chrome layer:
//
//   - line 1: title bar (brand · project · version)
//   - line 2: stats line (counts + filter chips)
//   - lines 3..H-N: table (header + separator + windowed rows, padded
//     so the adaptive footer pins to the bottom of the terminal
//     regardless of row count)
//   - line H-1: info line (active input bar OR scroll/flash text)
//   - line H:   footer help table (one or more rows)
//
// The table body absorbs the slack so the footer always sits on the
// last line of the terminal — the msgvault `fillScreen` pattern.
func (lm listModel) View(width, height int, chrome viewChrome) string {
	if lm.loading {
		return statusStyle.Render("loading…")
	}
	if lm.err != nil {
		return errorStyle.Render(lm.err.Error())
	}
	if width <= 0 || height < listMinHeight {
		// Below the floor, render whatever fits without the chrome
		// layout math (avoids divide-by-negative or empty renders).
		return lm.renderTinyFallback(width)
	}
	title := renderTitleBar(width, chrome.scope, chrome.version)
	stats := renderStatsLine(width, chrome.scope, lm.issueCounts(), lm.filter)
	helpRows := listHelpRows(lm, chrome)
	footerLines := helpLines(helpRows, width)
	footer := renderFooterHelpTable(helpRows, width)

	// Body area: everything between header (lines 1-2) and the
	// info+adaptive footer. bodyRows is computed first so the
	// info-line scroll indicator uses the actual budget. The
	// table-overhead cost (header + separator) is baked into
	// renderBodyArea, so bodyRows here is the full body region.
	bodyRows := height - 2 /* header */ - 1 /* info */ - footerLines
	if bodyRows < listBodyFloor {
		bodyRows = listBodyFloor
	}
	dataBudget := bodyRows - 2 /* table header + separator */
	if dataBudget < 1 {
		dataBudget = 1
	}
	infoLine := renderListInfoLine(width, chrome, lm, dataBudget)
	body := lm.renderBodyArea(width, bodyRows, chrome)

	return strings.Join([]string{title, stats, body, infoLine, footer}, "\n")
}

// ViewBody returns the body region for an M6 split-mode list pane:
// table header + separator + windowed rows + fillScreen padding so
// the pane reaches `bodyRows` lines tall. Pulls the narrow flag off
// chrome so renderBody knows to drop the owner column. The chrome /
// title-bar / info-line / footer are composed by renderSplit, not
// here — this returns just the body region.
func (lm listModel) ViewBody(width, bodyRows int, chrome viewChrome) string {
	if lm.loading {
		return statusStyle.Render("loading…")
	}
	if lm.err != nil {
		return errorStyle.Render(lm.err.Error())
	}
	return lm.renderBodyArea(width, bodyRows, chrome)
}

// listMinHeight is the smallest terminal height the layout supports.
// Below this we fall through to the bare fallback render.
const listMinHeight = 10

// listBodyFloor is the smallest row count the table body will ever
// render. Keeps a usable list visible even when the chrome math is
// tight on a small terminal.
const listBodyFloor = 5

// renderTinyFallback is the degraded render for terminals below the
// minimum height. M5 will replace this with a proper "too narrow"
// hint; for now it's just the table without chrome.
func (lm listModel) renderTinyFallback(width int) string {
	return lm.renderBody(width, listBodyFloor, viewChrome{})
}

// renderBodyArea wraps renderBody with the fillScreen padding that
// pins the footer to the bottom. The table's data rows window around
// the cursor to fit bodyRows; the remaining vertical slack is padded
// with blank rows styled with normalRowStyle so terminals that retain
// prior content overwrite cleanly.
//
// chrome.narrow is consulted by renderBody to drop the owner column
// in M6 split-mode. The narrow flag stays scoped to renderBody +
// helpers — chrome boilerplate (title/footer) is unaffected.
func (lm listModel) renderBodyArea(width, bodyRows int, chrome viewChrome) string {
	body := lm.renderBody(width, bodyRows-2 /* header + sep */, chrome)
	rendered := strings.Split(body, "\n")
	for len(rendered) < bodyRows {
		rendered = append(rendered, normalRowStyle.Render(strings.Repeat(" ", width)))
	}
	if len(rendered) > bodyRows {
		rendered = rendered[:bodyRows]
	}
	return strings.Join(rendered, "\n")
}

// renderTitleBar formats the top brand strip:
//
//	Project: {name}                         kata カタ · vX.Y.Z
//
// Project context lives on the left because it's what the user is
// actually working in; brand + version are window-chrome and pin to
// the right. `カタ` is katakana for "form/pattern" — the romaji
// disambiguator for the brand vs. a project that happens to also be
// named "kata". All-projects scope and the empty-scope hint render
// in the project slot so the left side is never blank.
func renderTitleBar(width int, sc scope, version string) string {
	left := titleBarLeft(sc)
	right := titleBarRight(version)
	body := padLeftRightInside(left, right, titleBarInnerWidth(width))
	return titleBarStyle.Render(body)
}

// titleBarLeft builds the left-aligned project label. Single-project
// scope reads `Project: $name`; all-projects scope reads
// `Project: all`; the no-scope startup state reads `Project: —` so
// the bar layout doesn't shift once a project is resolved.
//
// sanitizeForDisplay is applied BEFORE the empty check so a daemon
// reply like "​" or pure control runes (which sanitize down to
// "") falls through to the `—` placeholder instead of rendering an
// empty `Project: ` string. Roborev job 128.
func titleBarLeft(sc scope) string {
	if sc.allProjects {
		return "Project: all"
	}
	clean := sanitizeForDisplay(sc.projectName)
	if clean == "" {
		return "Project: —"
	}
	return "Project: " + clean
}

// titleBarRight is the brand + version cluster pinned to the right
// of the title bar. Version is omitted (gracefully) on builds that
// didn't stamp it so the right side is just the brand.
func titleBarRight(version string) string {
	if version == "" {
		return "kata カタ"
	}
	return "kata カタ · " + version
}

// titleBarInnerWidth subtracts the titleBarStyle horizontal padding
// (1 cell each side) so padLeftRightInside fills exactly the
// renderable area.
func titleBarInnerWidth(width int) int {
	w := width - 2 // padding
	if w < 1 {
		w = 1
	}
	return w
}

// renderStatsLine is the second header line — counts + filter chips.
// SSE state and the actor used to live here in M1; both are gone
// (SSE surfaces as a flash on the info line when degraded; actor
// stays in lm.actor for mutation dispatch but isn't surfaced).
func renderStatsLine(width int, _ scope, c issueCounts, f ListFilter) string {
	left := statsCountsText(c)
	right := renderChips(f)
	body := padLeftRightInside(left, right, titleBarInnerWidth(width))
	return statsLineStyle.Render(body)
}

// statsCountsText reads the open/closed/all counts from issueCounts
// and renders them as `open: N · closed: N · all: N`. Empty issues
// renders as "no issues" so the line never goes blank when the user
// has the empty-state hint visible.
func statsCountsText(c issueCounts) string {
	if c.all == 0 {
		return "no issues"
	}
	return fmt.Sprintf("open: %d · closed: %d · all: %d", c.open, c.closed, c.all)
}

// renderListInfoLine renders the info line just above the footer.
// Sources, in priority order:
//
//  1. Active inline command bar (search/owner) — `/buffer` form via
//     renderInfoBar.
//  2. A status flash (mutation result like "closed #42").
//  3. SSE-degraded indicator when sseStatus != connected.
//  4. A toast (e.g. "resynced").
//  5. The scroll indicator `[start-end of N]` when the visible window
//     doesn't fit the full filtered list.
//  6. The cursor indicator `[N/M]` when the visible list fits.
//  7. Blank if none of the above apply.
//
// dataBudget is the actual data-row budget the table renders into;
// the scroll indicator only fires when the visible list exceeds it.
// Threading it from View instead of approximating fixes roborev
// #107 finding 2 (hardcoded `lastBodyRows()` was wrong on terminals
// other than 30 rows tall).
//
// Always rendered inside statsLineStyle so the line reads as part
// of the chrome strip even when blank (background fills the row).
func renderListInfoLine(width int, chrome viewChrome, lm listModel, dataBudget int) string {
	body := ""
	switch {
	case chrome.input.kind.isCommandBar():
		body = renderInfoBar(chrome.input, titleBarInnerWidth(width))
	case lm.status != "":
		body = lm.status
	case chrome.sseStatus != sseConnected:
		body = sseDegradedFlash(chrome.sseStatus)
	case chrome.toast != nil:
		body = chrome.toast.text
	case lm.truncated:
		body = fmt.Sprintf("showing first %d issues; refine filters", queueWorkingSetLimit)
	default:
		visible := lm.visibleRows()
		if n := len(visible); n > 0 && dataBudget > 0 && n > dataBudget {
			start, end := windowBounds(n, lm.cursor, dataBudget)
			body = rightAlignInside(
				fmt.Sprintf("[%d-%d of %d]", start+1, end, n),
				titleBarInnerWidth(width),
			)
		} else if n > 0 {
			body = rightAlignInside(footerPositionIndicator(lm.cursor, n), titleBarInnerWidth(width))
		}
	}
	return statsLineStyle.Render(padToWidth(body, titleBarInnerWidth(width)))
}

// renderInfoBar formats the inline command bar for the info line.
// Single line, no border (the surrounding statsLineStyle already
// gives the row a chrome look), prefixed by a slash for search.
//
// The textinput's View() includes bubbles' own cursor-paint ANSI;
// we keep it intact (don't sanitize — that would erase the cursor)
// and width-clip with ansi.Truncate so escape sequences survive.
//
// Plan 8 commit 5a retired the owner bar; the search bar is the only
// remaining inline command bar so the prefix is unconditionally "/".
func renderInfoBar(s inputState, innerWidth int) string {
	full := "/"
	if field := s.activeField(); field != nil {
		full += field.input.View()
	}
	return ansi.Truncate(full, innerWidth, "…")
}

// sseDegradedFlash returns a brief inline notice when SSE is in a
// non-connected state. Used by renderListInfoLine when no other
// flash takes priority.
func sseDegradedFlash(state sseConnState) string {
	switch state {
	case sseReconnecting:
		return "kata: reconnecting…"
	case sseDisconnected:
		return "kata: disconnected (retrying)"
	}
	return ""
}

// footerPositionIndicator returns the cursor position in the visible
// list — `[N/M]`. Renders empty when the list is empty.
func footerPositionIndicator(cursor, totalRows int) string {
	if totalRows == 0 {
		return ""
	}
	pos := cursor + 1
	if pos > totalRows {
		pos = totalRows
	}
	return fmt.Sprintf("[%d/%d]", pos, totalRows)
}

// padLeftRightInside places left text on the left and right text on
// the right of an `innerWidth`-cell-wide line, padding with spaces
// in between. Wide-character aware via runewidth.StringWidth so the
// `カタ` glyphs in the title bar align correctly. When the right
// text would overflow, it's truncated to fit (the left side wins
// because it carries the brand/identity).
func padLeftRightInside(left, right string, innerWidth int) string {
	lw := runewidth.StringWidth(stripANSI(left))
	rw := runewidth.StringWidth(stripANSI(right))
	if lw >= innerWidth {
		// ANSI-aware cut: renderStatsLine and several callers pass
		// styled chips here; runewidth.Truncate would slice inside an
		// SGR sequence and corrupt the row (roborev #17162 finding 1).
		return ansi.Truncate(left, innerWidth, "…")
	}
	if lw+rw+1 > innerWidth {
		// Right doesn't fit; truncate it (ANSI-aware — see above).
		availableForRight := innerWidth - lw - 1
		if availableForRight < 1 {
			return left + " "
		}
		right = ansi.Truncate(right, availableForRight, "…")
		rw = runewidth.StringWidth(stripANSI(right))
	}
	gap := innerWidth - lw - rw
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// rightAlignInside returns s right-aligned within innerWidth cells.
// Used by the scroll indicator. ANSI-aware: stripANSI for measurement,
// ansi.Truncate for the cut so escape sequences are never sliced
// mid-byte when the indicator carries styling.
func rightAlignInside(s string, innerWidth int) string {
	w := runewidth.StringWidth(stripANSI(s))
	if w >= innerWidth {
		return ansi.Truncate(s, innerWidth, "…")
	}
	return strings.Repeat(" ", innerWidth-w) + s
}

// padToWidth right-pads s with spaces so the rendered cell fills
// `width` cells. Used for chrome lines that need a uniform width.
// ANSI-aware on the truncate path: stripANSI for measurement, then
// ansi.Truncate for the cut so escape sequences are never sliced
// mid-byte. The previous implementation used runewidth.Truncate
// which sliced inside SGR sequences when the input carried ANSI —
// the terminal then rendered the dangling escape fragment as a
// stray suffix and chopped most of the title bar off the screen.
func padToWidth(s string, width int) string {
	w := runewidth.StringWidth(stripANSI(s))
	if w == width {
		return s
	}
	if w > width {
		return ansi.Truncate(s, width, "…")
	}
	return s + strings.Repeat(" ", width-w)
}

// stripANSI removes ANSI escape sequences from s for width math (so
// padding accounts for visible runes only). Thin alias over
// textsafe.StripANSI so width helpers and the sanitizer share the
// same regex.
func stripANSI(s string) string { return textsafe.StripANSI(s) }

// issueCounts derives the per-status counts from the unfiltered
// lm.issues slice. Used by the title bar.
type issueCounts struct {
	open, closed, deleted, all int
}

func (lm listModel) issueCounts() issueCounts {
	c := issueCounts{all: len(lm.issues)}
	for _, iss := range lm.issues {
		if iss.DeletedAt != nil {
			c.deleted++
			continue
		}
		switch iss.Status {
		case "open":
			c.open++
		case "closed":
			c.closed++
		}
	}
	return c
}

// renderBody is the table body — header, separator, then up to height
// data rows around the cursor. No top/bottom borders (msgvault
// pattern); just one separator under the column header.
//
// chrome.narrow=true (M6 split-mode list pane) drops the owner column so
// the title column flexes into the recovered cells. chrome.scope and
// chrome.projectsByID drive the all-projects per-row prefix so the user
// can tell which project each row belongs to under the R toggle.
func (lm listModel) renderBody(width, height int, chrome viewChrome) string {
	narrow := chrome.narrow
	queueRows := lm.visibleRows()
	if len(queueRows) == 0 {
		hint := "no issues match. press c to clear filters or n to create one."
		return tableHeaderRow(width, narrow) + "\n" +
			separatorRuleStyle.Render(strings.Repeat("─", width)) + "\n" +
			normalRowStyle.Render(padToWidth("  "+hint, width))
	}
	displayCursor := lm.cursor
	if displayCursor >= len(queueRows) {
		displayCursor = len(queueRows) - 1
	}
	if displayCursor < 0 {
		displayCursor = 0
	}
	visible, vCursor := windowQueueRows(queueRows, displayCursor, height)
	cols := listColumnWidths(width, narrow)
	rows := buildRows(visible, vCursor, cols.title, narrow, chrome)
	headers := listTableHeaders(narrow)
	t := table.New().
		Border(lipgloss.HiddenBorder()).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderColumn(false).
		BorderRow(false).
		BorderHeader(false).
		Width(width).
		Wrap(false).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().Width(cols.byIndex(col, narrow)).PaddingRight(1)
			if row == table.HeaderRow {
				return s.Inherit(tableHeaderStyle)
			}
			if row >= 0 && row < len(rows) && row == vCursor {
				return s.Inherit(cursorRowStyle)
			}
			if row >= 0 && row < len(visible) && visible[row].context {
				return s.Inherit(subtleStyle)
			}
			if row >= 0 && row%2 == 1 {
				return s.Inherit(altRowStyle)
			}
			return s.Inherit(normalRowStyle)
		})
	rendered := t.Render()
	// Insert the separator rule between the header row and the data
	// rows. lipgloss.Table renders as "header\nrow1\nrow2..."; we
	// split, inject the rule, and re-join.
	lines := strings.SplitN(rendered, "\n", 2)
	if len(lines) < 2 {
		return rendered
	}
	rule := separatorRuleStyle.Render(strings.Repeat("─", width))
	return lines[0] + "\n" + rule + "\n" + lines[1]
}

// listTableHeaders returns the column-header label slice for the
// list table. Wide (default) mode renders six columns including
// owner; narrow (M6 split-mode list pane) drops owner.
func listTableHeaders(narrow bool) []string {
	if narrow {
		return []string{"", "", "", "#", "status", "title", "kids", "updated"}
	}
	return []string{"", "", "", "#", "status", "title", "kids", "owner", "updated"}
}

// tableHeaderRow renders just the column-header line at the given
// width, used by the empty-state branch where the lipgloss Table
// isn't constructed. Mirrors the column widths and styling. The
// narrow flag drops the owner column to match renderBody.
func tableHeaderRow(width int, narrow bool) string {
	cols := listColumnWidths(width, narrow)
	headers := listTableHeaders(narrow)
	parts := make([]string, len(headers))
	for i, h := range headers {
		w := cols.byIndex(i, narrow)
		parts[i] = tableHeaderStyle.Render(padToWidth(h, w-1)) + " "
	}
	return strings.Join(parts, "")
}

// listColWidths holds the per-column cell widths the list table renders
// at. Fixed columns (cursor / # / status / owner / updated) take what
// they need; the title column flexes to fill the rest of the terminal
// with a 20-cell floor so titles stay readable on narrow terminals.
type listColWidths struct {
	cursor, context, tree, num, status, title, children, owner, updated int
}

// byIndex maps a table column index to its width. The narrow flag
// shifts the updated column from index 5 (wide) to index 4 (narrow,
// owner dropped) so the table's per-column StyleFunc still picks
// the right width.
func (c listColWidths) byIndex(col int, narrow bool) int {
	if narrow {
		switch col {
		case 0:
			return c.cursor
		case 1:
			return c.context
		case 2:
			return c.tree
		case 3:
			return c.num
		case 4:
			return c.status
		case 5:
			return c.title
		case 6:
			return c.children
		case 7:
			return c.updated
		}
		return 0
	}
	switch col {
	case 0:
		return c.cursor
	case 1:
		return c.context
	case 2:
		return c.tree
	case 3:
		return c.num
	case 4:
		return c.status
	case 5:
		return c.title
	case 6:
		return c.children
	case 7:
		return c.owner
	case 8:
		return c.updated
	}
	return 0
}

// listColumnWidths computes per-column cell widths for the list table.
// Fixed-width columns sum to ~42 cells (with PaddingRight(1) per cell);
// the title column flexes to fill the rest with a 20-cell floor.
//
// narrow=true (M6 split-mode list pane) drops the owner column from
// the fixed-width sum so the title flexes 14 cells wider — keeps
// titles readable inside the 68-cell list pane.
func listColumnWidths(termWidth int, narrow bool) listColWidths {
	c := listColWidths{
		cursor:   2,  // "▶" + padding
		context:  2,  // "~" + padding
		tree:     4,  // disclosure + shallow indent
		num:      6,  // "#9999"
		status:   10, // "[deleted]"
		children: 8,  // "12/100"
		owner:    14,
		updated:  10, // "12w ago"
	}
	fixed := c.cursor + c.context + c.tree + c.num + c.status + c.children + c.updated
	if !narrow {
		fixed += c.owner
	}
	c.title = termWidth - fixed
	if c.title < 20 {
		c.title = 20
	}
	return c
}

// windowQueueRows returns the contiguous slice of queue rows that includes
// the cursor row and fits within budget. The cursor index in the
// returned slice (vCursor) is the local position so the table renderer
// can highlight the right row.
func windowQueueRows(rows []queueRow, cursor, budget int) ([]queueRow, int) {
	n := len(rows)
	if n == 0 {
		return rows, 0
	}
	start, end := windowBounds(n, cursor, budget)
	return rows[start:end], cursor - start
}

// windowBounds returns the [start, end) slice indices of the visible
// window for a list of n items with the cursor at index cursor and a
// row budget of budget. Used by both the row renderer and the scroll
// indicator so the displayed range and the rendered slice stay in
// sync. Empty input returns (0, 0); budget < 1 collapses to 1 so the
// cursor is always visible.
//
// The window slides so the cursor sits anywhere from the top to the
// bottom of the viewport, preferring to anchor at the top until the
// cursor moves past the budget, then scrolling to keep the cursor
// near the bottom. The "two-thirds from the top" anchor matches the
// conventional vim/less feel — more upcoming rows than scrolled-past.
func windowBounds(n, cursor, budget int) (int, int) {
	if n == 0 {
		return 0, 0
	}
	if budget < 1 {
		budget = 1
	}
	if n <= budget {
		return 0, n
	}
	headroom := budget / 3
	start := cursor - headroom
	if start < 0 {
		start = 0
	}
	end := start + budget
	if end > n {
		end = n
		start = n - budget
	}
	return start, end
}

// renderSSEStatus returns the connection-status line rendered below
// non-list views (help / empty) when the SSE consumer is degraded.
// The list view surfaces the same info on its info line via
// renderListInfoLine, so this helper only fires for views that
// don't carry the M3.5 chrome.
func renderSSEStatus(state sseConnState) string {
	switch state {
	case sseReconnecting:
		return statusStyle.Render("kata: reconnecting…")
	case sseDisconnected:
		return statusStyle.Render("kata: disconnected (retrying)")
	}
	return ""
}

// renderToast wraps an active toast for display below non-list views
// that don't have an info line of their own. List view renders the
// toast text inline via renderListInfoLine.
func renderToast(t *toast) string {
	if t == nil {
		return ""
	}
	return toastStyle.Render(t.text)
}

// renderChips returns one chip per active filter slot. Inactive
// defaults (status="", owner="", search="", labels=nil) are skipped.
// Plan 8 commit 5b: the label chip is rendered now that the wire
// carries per-issue labels (api.IssueOut) and matchesFilter honors
// any-of semantics.
//
// "search" chip prefix changed from `q:` to `search:` in M3.5: the
// `q` letter collided with the global Quit binding and read as if
// the chip itself was bound to q.
func renderChips(f ListFilter) string {
	chips := []string{}
	if f.Status != "" {
		chips = append(chips, chipActive.Render("status:"+f.Status))
	}
	if f.Owner != "" {
		chips = append(chips, chipStyle.Render(
			"owner:"+sanitizeForDisplay(f.Owner)))
	}
	if f.Author != "" {
		chips = append(chips, chipStyle.Render(
			"author:"+sanitizeForDisplay(f.Author)))
	}
	if f.Search != "" {
		chips = append(chips, chipStyle.Render(
			fmt.Sprintf("search:%q", sanitizeForDisplay(f.Search))))
	}
	for _, l := range f.Labels {
		chips = append(chips, chipStyle.Render(
			"label:"+sanitizeForDisplay(l)))
	}
	if len(chips) == 0 {
		return ""
	}
	return strings.Join(chips, "  ")
}

// renderLabelChips packs label chips into `available` cells for the
// detail header's right-side label strip. Chips render alphabetically;
// trailing chips that don't fit collapse into a `+N` suffix. When even
// one chip would overflow `available`, the entire row degrades to the
// fixed-width `[N labels]` token so the header stays informative on
// tiny terminals. Empty input yields a `(no labels)` placeholder so the
// row keeps its visible weight when an issue carries no labels.
//
// Sanitization runs BEFORE both width measurement and rendering: a
// caller that measured the stripped form but rendered the raw label
// would still leak ANSI / Cf control runes into the header. The
// sanitized text is the single source of truth used for both.
//
// Width math: each chip is `[<sanitized-label>]` plus one space
// separator before the next chip. The +N overflow token reserves
// `1 + width("+<len(clean)>")` cells inside `available` so packing
// never blows the budget by failing to leave room for the suffix —
// computed from the actual label count so projects with 100+ labels
// (`+100` = 5 cells) reserve correctly instead of underspending the
// fixed `+99` budget. Roborev job 235.
func renderLabelChips(labels []string, available int) string {
	if len(labels) == 0 {
		return subtleStyle.Render("(no labels)")
	}
	clean := sanitizeAndSortLabels(labels)
	// Reserve the worst-case `+N` suffix width (leading space + token).
	// Computed from len(clean) so a 100-label issue reserves 5 cells
	// for "+100" instead of the legacy 4-cell "+99" assumption.
	overflowReserve := 1 + runewidth.StringWidth(fmt.Sprintf("+%d", len(clean)))
	packed, dropped := packChips(clean, available, overflowReserve)
	if len(packed) == 0 {
		return ultraNarrowChipFallback(len(clean))
	}
	out := strings.Join(packed, " ")
	if dropped > 0 {
		out += " " + chipStyle.Render(fmt.Sprintf("+%d", dropped))
	}
	return out
}

// sanitizeAndSortLabels returns labels with each entry sanitized
// through textsafe.Line (ANSI / control / Cf stripped, plus newlines
// replaced with literal "\n" and tabs with spaces) and the slice
// sorted in ascending byte order. Line — not Block — because the chip
// strip is a single-row context: a label containing a literal \n
// would split mid-chip across terminal rows and break the fixed-row
// budget. The schema bars newlines in labels, but the renderer is
// the wrong layer to depend on that. Sort lives here so
// renderLabelChips stays focused on the packing math.
func sanitizeAndSortLabels(labels []string) []string {
	clean := make([]string, len(labels))
	for i, l := range labels {
		clean[i] = textsafe.Line(l)
	}
	sort.Strings(clean)
	return clean
}

// chipMinSlot returns the cell width of one rendered chip including
// the surrounding brackets — the unit packChips advances on per chip.
func chipMinSlot(label string) int {
	return runewidth.StringWidth(label) + 2 // brackets
}

// packChips greedily fits chips into `available` cells, leaving
// `overflowReserve` cells free at the tail so a `+N` token can be
// appended without overflow. Returns the rendered chip slice and the
// number of dropped tail labels.
func packChips(clean []string, available, overflowReserve int) ([]string, int) {
	out := make([]string, 0, len(clean))
	used := 0
	for i, l := range clean {
		chip := chipStyle.Render("[" + l + "]")
		w := chipMinSlot(l)
		// Separator between chips (not before the first chip).
		if len(out) > 0 {
			w++
		}
		// Reserve overflow room for any chips after this one.
		remaining := len(clean) - i - 1
		needTail := 0
		if remaining > 0 {
			needTail = overflowReserve
		}
		if used+w+needTail > available {
			return out, len(clean) - len(out)
		}
		out = append(out, chip)
		used += w
	}
	return out, 0
}

// ultraNarrowChipFallback is the degraded `[N labels]` token used when
// `available` is too small to fit even one chip plus the overflow
// reserve. Keeps the header informative without overflowing.
func ultraNarrowChipFallback(n int) string {
	return chipStyle.Render(fmt.Sprintf("[%d labels]", n))
}

// joinNonEmpty assembles a view from its non-empty sections.
// Retained for non-list callers (detail view still uses it); the list
// view now uses fixed-position composition.
func joinNonEmpty(parts []string) string {
	out := []string{}
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}

// buildRows projects issues to the six-column shape the table renders
// (five-column when narrow drops the owner). titleW is the budget for
// the (flexed) title column — the renderer truncates titles longer
// than that with an ellipsis. Owner is truncated at 12 cells so the
// column never overflows its 14-cell width. Title and owner are
// agent-authored so both run through sanitizeForDisplay before
// truncation.
//
// Cursor glyph is `▶` (msgvault pattern) — more visible than `›` in
// terminals that render fonts at low pixel density.
//
// narrow=true (M6 split-mode list pane) drops the owner cell so the
// row aligns with the five-header table.
func buildRows(queueRows []queueRow, cursor, titleW int, narrow bool, chrome viewChrome) [][]string {
	if titleW < 20 {
		titleW = 20
	}
	rows := make([][]string, 0, len(queueRows))
	for i, qr := range queueRows {
		iss := qr.issue
		title := titleForRow(iss, chrome, titleW)
		row := []string{
			selMarker(i == cursor),
			contextMarker(qr.context),
			treeCell(qr),
			fmt.Sprintf("#%d", iss.Number),
			statusChip(iss),
			title,
			childCountCell(iss.ChildCounts),
		}
		if !narrow {
			row = append(row, truncate(sanitizeForDisplay(ownerText(iss.Owner)), 12))
		}
		row = append(row, humanizeRelative(iss.UpdatedAt))
		rows = append(rows, row)
	}
	return rows
}

// titleForRow renders the title cell, prefixing the owning project's
// display name in all-projects scope so the user can tell which project
// a row belongs to under the R toggle. The prefix is rendered as
// "[name] " (or "[#PID] " when projectsByID is missing the entry, e.g.
// after a freshly-created project before the cache refresh) and is
// truncated together with the title to fit titleW.
//
// In single-project scope the prefix is omitted — every row belongs to
// the same project so repeating the name on every line is noise.
func titleForRow(iss Issue, chrome viewChrome, titleW int) string {
	clean := sanitizeForDisplay(iss.Title)
	if !chrome.scope.allProjects {
		return truncate(clean, titleW)
	}
	prefix := projectPrefix(iss.ProjectID, chrome.projectsByID)
	return truncate(prefix+clean, titleW)
}

// projectPrefix renders the bracketed project name for a list row. Falls
// back to "[#PID]" when the project lookup misses so the row still tells
// the user which project rather than appearing nameless. The name is
// sanitized so control characters or ANSI sequences in stored project
// names cannot corrupt the terminal UI.
func projectPrefix(projectID int64, byID map[int64]string) string {
	if name, ok := byID[projectID]; ok {
		if clean := sanitizeForDisplay(name); clean != "" {
			return "[" + clean + "] "
		}
	}
	return fmt.Sprintf("[#%d] ", projectID)
}

func contextMarker(context bool) string {
	if context {
		return "~"
	}
	return ""
}

func treeCell(row queueRow) string {
	indent := strings.Repeat(" ", min(row.depth, 3))
	return truncate(indent+disclosureGlyph(row.hasChildren, row.expanded), 3)
}

func disclosureGlyph(hasChildren, expanded bool) string {
	if !hasChildren {
		return " "
	}
	if activeColorMode == colorNone {
		if expanded {
			return "-"
		}
		return "+"
	}
	if expanded {
		return "▾"
	}
	return "▸"
}

func childCountCell(counts *ChildCounts) string {
	if counts == nil || counts.Total == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", counts.Open, counts.Total)
}

// selMarker is the per-row arrow glyph; ' ' for unselected so the
// column width stays stable.
func selMarker(selected bool) string {
	if selected {
		return "▶"
	}
	return " "
}

// statusChip picks the right colored chip text for the issue.
// Soft-deleted rows win over closed.
func statusChip(iss Issue) string {
	switch {
	case iss.DeletedAt != nil:
		return deletedStyle.Render("[deleted]")
	case iss.Status == "closed":
		return closedStyle.Render("closed")
	default:
		return openStyle.Render("open")
	}
}

// ownerText flattens a *string owner to display form ("" when unset
// so truncate's no-op branch handles the empty case cleanly).
func ownerText(owner *string) string {
	if owner == nil {
		return ""
	}
	return *owner
}

// truncate cuts s to visible-cell width w, appending an ellipsis. Width
// is measured in visible cells (ANSI escape sequences and wide
// East-Asian glyphs are handled correctly), and the cut itself is
// ANSI-aware via ansi.Truncate so escape sequences are never sliced
// in half. The previous implementation used runewidth.Truncate which
// counted every byte of an escape sequence as a cell — passing a
// styled string would over-count the width, then the cut would slice
// mid-sequence, leaving the terminal to render `�` glyphs and
// fragments of the SGR command (the source of the screenshot's `??`
// artifacts on the active activity tab).
func truncate(s string, w int) string {
	if w <= 0 {
		return s
	}
	if runewidth.StringWidth(stripANSI(s)) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// renderNow is the clock injection point for humanizeRelative.
// Production uses time.Now; snapshot tests override this to freeze
// time so the "Nh ago" column in golden files doesn't churn as
// wall-clock advances.
var renderNow = time.Now

// humanizeRelative renders a timestamp as a small human delta
// (e.g. "30s ago", "2h ago", "3d ago"). The zero value renders as a
// dash so empty rows in tests stay readable; malformed inputs fail
// earlier at JSON decode time and never reach this function.
func humanizeRelative(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := renderNow().Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	}
}
