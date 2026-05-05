package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// View renders the stacked detail view as a document page. The page
// reads top-to-bottom:
//
//   - line 1: full-width project/title bar (kept in sync with the
//     list view so the global chrome reads as a single window)
//   - line 2: blank breather under the chrome
//   - sheet: issue lead + body + (optional children) + (optional
//     activity), each row prefixed with documentGutter cells of
//     space and capped at documentSheetWidth(width) cells of content
//   - line H-1: info line (chrome row)
//   - line H:   footer help table (chrome row)
//
// Content lives inside a 96-cell maximum measure. Spare terminal
// width to the right is left blank — the redesign avoids stretching
// section bands across wide terminals so the page reads as a focused
// document, not a half-empty workspace.
func (dm detailModel) View(width, height int, chrome viewChrome) string {
	if dm.loading {
		return statusStyle.Render("loading…")
	}
	if dm.issue == nil {
		return statusStyle.Render("no issue selected")
	}
	if width <= 0 || height < listMinHeight {
		return dm.renderTinyFallback(width)
	}
	sheetWidth := documentSheetWidth(width)
	helpRows := detailHelpRows(dm, chrome)
	footerLines := helpLines(helpRows, width)
	footer := renderFooterHelpTable(helpRows, width)
	titleBar := renderTitleBar(width, chrome.scope, chrome.version)
	header := append([]string{titleBar, ""}, dm.documentHeader(sheetWidth, chrome)...)
	hasChildren := len(dm.children) > 0
	hasActivity := dm.hasActivity()
	fixed := len(header) + 1 /* body label */ + 1 /* blank gap before activity */ +
		1 /* info */ + footerLines
	if hasChildren {
		fixed += 2 /* children label + blank gap */
	}
	if hasActivity {
		fixed += 2 /* activity header + blank gap */
	}
	bodyA, childA, tabA := detailDocumentBudgets(height-fixed, len(dm.children), hasActivity)
	bodyArea := withGutter(dm.renderBody(sheetWidth, bodyA))
	childrenArea := ""
	if hasChildren {
		childrenArea = withGutter(dm.renderChildrenSection(sheetWidth, childA))
	}
	tabArea := ""
	if hasActivity {
		tabArea = withGutter(dm.renderActiveTab(sheetWidth, tabA))
	}
	infoLine := dm.renderInfoLine(width, chrome, tabA)
	parts := append([]string{}, header...)
	parts = append(parts, "", renderDocumentSectionHeader("Body"), bodyArea)
	if hasChildren {
		parts = append(parts, "", renderDocumentSectionHeader("Children"), childrenArea)
	}
	if hasActivity {
		parts = append(parts, "", dm.renderActivityHeader(sheetWidth), tabArea)
	}
	content := padDocumentContent(parts, height-1-footerLines, width)
	return strings.Join([]string{content, infoLine, footer}, "\n")
}

// renderTinyFallback is the degraded render for terminals below the
// minimum height. Just dump body content so the user sees something.
func (dm detailModel) renderTinyFallback(width int) string {
	return dm.renderBody(width, detailMinBodyRows)
}

func padDocumentContent(parts []string, rows, terminalWidth int) string {
	if rows < 1 {
		rows = 1
	}
	content := strings.Join(parts, "\n")
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = padToWidth(line, terminalWidth)
	}
	for len(lines) < rows {
		lines = append(lines, blankLine(terminalWidth))
	}
	if len(lines) > rows {
		lines = lines[:rows]
	}
	return strings.Join(lines, "\n")
}

// documentHeader builds the issue-lead block: title row, byline,
// and the compact metadata strip. Each line is gutter-prefixed so
// the sheet reads as one indented column under the project bar.
func (dm detailModel) documentHeader(width int, chrome viewChrome) []string {
	iss := *dm.issue
	lines := []string{withGutter(renderDocumentTitleStatus(width, iss))}
	if byline := renderDocumentByline(width, iss); byline != "" {
		lines = append(lines, withGutter(byline))
	}
	for _, row := range renderDocumentMetadata(
		width, iss, dm.parent, dm.children, chrome.scope, dm.uidFormat,
	) {
		lines = append(lines, withGutter(row))
	}
	return lines
}

func renderDocumentTitleStatus(width int, iss Issue) string {
	status := renderStatusPill(iss)
	statusPlain := stripANSI(status)
	statusW := runewidth.StringWidth(statusPlain)
	prefix := subtleStyle.Render(fmt.Sprintf("#%d", iss.Number))
	prefixW := runewidth.StringWidth(stripANSI(prefix)) + 2
	titleBudget := width - prefixW - statusW - 2
	if titleBudget < 1 {
		titleBudget = 1
	}
	title := prefix + "  " + titleStyle.Render(truncate(sanitizeForDisplay(iss.Title), titleBudget))
	return padLeftRightInside(title, status, width)
}

func renderStatusPill(iss Issue) string {
	text := "[open]"
	style := openStyle
	if iss.DeletedAt != nil {
		text = "[deleted]"
		style = deletedStyle
	} else if iss.Status == "closed" {
		text = "[closed]"
		style = closedStyle
	}
	return style.Render(text)
}

func renderDocumentByline(width int, iss Issue) string {
	parts := documentBylineParts(iss, true, true)
	line := strings.Join(parts, " · ")
	if line == "" {
		return ""
	}
	if runewidth.StringWidth(line) > width {
		parts = documentBylineParts(iss, false, true)
		line = strings.Join(parts, " · ")
	}
	if runewidth.StringWidth(line) > width {
		parts = documentBylineParts(iss, false, false)
		line = strings.Join(parts, " · ")
	}
	return subtleStyle.Render(truncate(line, width))
}

func documentBylineParts(iss Issue, includeCreated, includeAuthor bool) []string {
	parts := []string{}
	if includeAuthor && iss.Author != "" {
		parts = append(parts, "authored by "+sanitizeForDisplay(iss.Author))
	}
	if includeCreated && !iss.CreatedAt.IsZero() {
		parts = append(parts, "created "+formatDocumentTime(iss.CreatedAt))
	}
	if !iss.UpdatedAt.IsZero() {
		parts = append(parts, "updated "+humanizeRelative(iss.UpdatedAt))
	}
	return parts
}

func formatDocumentTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("Jan 2 15:04")
}

// renderDocumentMetadata returns the compact metadata strip that
// sits below the byline. Owner and parent are always shown — the
// "none" placeholder is informative because both fields are used as
// triage signals. Labels and children are omitted entirely when
// empty so the page does not carry rows like `labels: none` whose
// only payload is a placeholder.
//
// In all-projects scope the project name leads on its own row so
// the user can tell which project a cross-project result lives in
// without consulting the global title bar (which reads "all" in
// that scope).
func renderDocumentMetadata(
	width int, iss Issue, parent *IssueRef, children []Issue, sc scope, uidFormat uidDisplayFormat,
) []string {
	rows := []string{}
	if sc.allProjects {
		rows = append(rows, "project: "+sanitizeForDisplay(sc.projectName))
	}
	owner := metadataLabel("owner:") + " " + ownerDocumentText(iss.Owner)
	parentText := metadataLabel("parent:") + " " + parentDocumentText(parent)
	rows = append(rows, joinMetadataRow(owner, parentText, width)...)
	if len(iss.Labels) > 0 {
		labelsBudget := width - runewidth.StringWidth("labels: ")
		labels := metadataLabel("labels:") + " " + labelsDocumentText(iss.Labels, labelsBudget)
		rows = append(rows, truncate(labels, width))
	}
	if len(children) > 0 {
		count := metadataLabel("children:") + " " + childrenCountSummary(children)
		rows = append(rows, truncate(count, width))
	}
	if uid := formatIssueUID(iss.UID, uidFormat); uid != "" {
		rows = append(rows, truncate(metadataLabel("uid:")+" "+uid, width))
	}
	return rows
}

func parseUIDDisplayFormat(v string) uidDisplayFormat {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "short":
		return uidDisplayShort
	case "full":
		return uidDisplayFull
	default:
		return uidDisplayNone
	}
}

func formatIssueUID(uid string, format uidDisplayFormat) string {
	if uid == "" {
		return ""
	}
	switch format {
	case uidDisplayShort:
		if len(uid) <= 8 {
			return "~" + uid
		}
		return "~" + uid[:8]
	case uidDisplayFull:
		return uid
	default:
		return ""
	}
}

// joinMetadataRow returns the (owner, parent) pair as one inline row
// when both fit the sheet width, or as two stacked rows when the
// combined width would overflow. Stacking — rather than truncating —
// preserves the parent reference text on narrow terminals where the
// title would otherwise be lost to ellipsis.
func joinMetadataRow(left, right string, width int) []string {
	lw := runewidth.StringWidth(stripANSI(left))
	rw := runewidth.StringWidth(stripANSI(right))
	if lw+3+rw <= width {
		return []string{left + "   " + right}
	}
	return []string{truncate(left, width), truncate(right, width)}
}

func metadataLabel(label string) string {
	return subtleStyle.Render(label)
}

func ownerDocumentText(owner *string) string {
	if owner == nil || *owner == "" {
		return "none"
	}
	return sanitizeForDisplay(*owner)
}

func labelsDocumentText(labels []string, available int) string {
	if len(labels) == 0 {
		return "none"
	}
	if available < 1 {
		available = 1
	}
	return renderLabelChips(labels, available)
}

func parentDocumentText(parent *IssueRef) string {
	if parent == nil {
		return "none"
	}
	return fmt.Sprintf("#%d %s", parent.Number, sanitizeForDisplay(parent.Title))
}

// renderDocumentSectionHeader renders a section label (Body /
// Children / Activity). The label sits inside the gutter without a
// background slab — the redesign relies on bold weight + position
// rather than a chrome band so the page reads as a quiet document.
func renderDocumentSectionHeader(label string) string {
	return withGutter(detailSectionHeaderStyle.Render(label))
}

// renderActivityHeader is a section header with the three tab chips
// trailing the label. Active tab is wrapped in `[ ]` brackets so the
// active state survives `KATA_COLOR_MODE=none`. The full strip is
// truncated to `width` cells so a narrow terminal never produces an
// overflow row that wraps under the gutter.
func (dm detailModel) renderActivityHeader(width int) string {
	tabs := [detailTabCount]string{
		fmt.Sprintf("Comments (%d)", len(dm.comments)),
		fmt.Sprintf("Events (%d)", len(dm.events)),
		fmt.Sprintf("Links (%d)", len(dm.links)),
	}
	parts := []string{detailSectionHeaderStyle.Render("Activity")}
	for i, tab := range tabs {
		if detailTab(i) == dm.activeTab {
			parts = append(parts, tabActive.Render("[ "+tab+" ]"))
		} else {
			parts = append(parts, tabInactive.Render(tab))
		}
	}
	return withGutter(truncate(strings.Join(parts, "   "), width))
}

// documentGutter is the left-side cell padding for content rows in
// the stacked detail view. The gutter visually separates the document
// content from the global title/info chrome at the page edges.
const documentGutter = 2

// documentSheetMaxWidth caps the readable measure of issue content
// rows. Wider terminals leave the spare cells blank rather than
// stretching backgrounds across the screen.
const documentSheetMaxWidth = 96

// documentSheetWidth returns the per-row content budget — the cells
// available for issue content after the gutter is reserved, capped at
// documentSheetMaxWidth so wide terminals do not blow out paragraph
// measure.
func documentSheetWidth(termWidth int) int {
	w := termWidth - documentGutter
	if w > documentSheetMaxWidth {
		w = documentSheetMaxWidth
	}
	if w < 1 {
		w = 1
	}
	return w
}

// withGutter prefixes each newline-separated line in s with the
// document gutter. Empty input returns the gutter alone so blank
// rows still carry the indent and the page reads as a uniform
// column.
func withGutter(s string) string {
	if s == "" {
		return strings.Repeat(" ", documentGutter)
	}
	gutter := strings.Repeat(" ", documentGutter)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = gutter + line
	}
	return strings.Join(lines, "\n")
}

func blankLine(width int) string {
	return normalRowStyle.Render(strings.Repeat(" ", max(0, width)))
}

func detailDocumentBudgets(avail, childCount int, hasActivity bool) (
	bodyRows, childRows, tabRows int,
) {
	if avail < 1 {
		return 1, 0, 0
	}
	if childCount > 0 && avail > 1 {
		childRows = min(childCount, max(1, avail/5))
		avail -= childRows
	}
	if hasActivity && avail >= 6 {
		tabRows = max(detailMinTabRows, avail/3)
		if tabRows > avail-1 {
			tabRows = avail - 1
		}
		avail -= tabRows
	}
	bodyRows = avail
	if bodyRows < 1 {
		bodyRows = 1
	}
	return bodyRows, childRows, tabRows
}

// renderInfoLine renders the info line just above the footer for the
// detail view. Same priority order as the list view: active panel
// prompt > flash > SSE-degraded > toast > scroll indicator. Always
// rendered inside statsLineStyle so the row reads as chrome even
// when blank.
//
// tabBudget is the actual tab-content row budget (computed in View
// from height). When 0 the scroll indicator is suppressed — used by
// the early View call before bodyA/tabA are resolved; View calls
// this again with the real budget once it knows tabA.
func (dm detailModel) renderInfoLine(width int, chrome viewChrome, tabBudget int) string {
	body := ""
	switch {
	case chrome.input.kind.isPanelPrompt():
		body = renderInfoPrompt(chrome.input, titleBarInnerWidth(width))
	case dm.status != "":
		body = dm.status
	case chrome.sseStatus != sseConnected:
		body = sseDegradedFlash(chrome.sseStatus)
	case chrome.toast != nil:
		body = chrome.toast.text
	default:
		// Compute the visible-entry window from the same chunk-
		// windowing logic the per-tab renderer uses, so multi-line
		// chunks (comments) report the right [start-end] range and
		// don't suppress the indicator when entry count <= line
		// budget but total wrapped lines exceed it (#119 finding 2).
		n := dm.activeRowCount()
		if n > 0 && tabBudget > 0 {
			// Chunk math uses the same content-width as the per-tab
			// renderer (sheet, not terminal) so wrapped comment line
			// counts match what's actually drawn — see roborev #17140
			// finding 2.
			chunks := dm.activeChunks(documentSheetWidth(width))
			start, end := windowChunkBounds(chunks, dm.tabCursor, tabBudget)
			if end-start < n {
				body = rightAlignInside(
					fmt.Sprintf("[%d-%d of %d %s]",
						start+1, end, n, dm.activeTabLabel()),
					titleBarInnerWidth(width))
			}
		}
	}
	return statsLineStyle.Render(padToWidth(body, titleBarInnerWidth(width)))
}

// renderInfoPrompt renders an active panel-local prompt as a single
// info-line row. Bordered/labeled at panel-prompt scope makes the
// info line too tall; instead the prompt's title prefixes the buffer.
//
// The textinput's View() carries bubbles' own cursor-paint ANSI;
// keep it intact (don't sanitize — strips the cursor) and width-clip
// with ansi.Truncate so escape sequences survive.
func renderInfoPrompt(s inputState, innerWidth int) string {
	field := s.activeField()
	if field == nil {
		return ansi.Truncate(s.title+": ", innerWidth, "…")
	}
	body := s.title + ": " + field.input.View()
	return ansi.Truncate(body, innerWidth, "…")
}

// detailMinBodyRows / detailMinTabRows are the floors so neither
// pane collapses to zero on small terminals.
const (
	detailMinBodyRows = 4
	detailMinTabRows  = 3
)

func renderHierarchySummary(width int, parent *IssueRef, children []Issue) string {
	left := "Parent: -"
	if parent != nil {
		left = fmt.Sprintf("Parent: #%d %s", parent.Number, sanitizeForDisplay(parent.Title))
	}
	right := "Children: " + childrenCountSummary(children)
	rightW := runewidth.StringWidth(right)
	leftBudget := width - rightW - 1
	if leftBudget < 1 {
		leftBudget = 1
	}
	left = truncate(left, leftBudget)
	return padLeftRightInside(left, right, width)
}

func childrenCountSummary(children []Issue) string {
	open := 0
	for _, child := range children {
		if child.Status == "open" {
			open++
		}
	}
	return fmt.Sprintf("%d open / %d total", open, len(children))
}

// activeTabLabel returns the singular noun for the active tab so the
// scroll indicator reads naturally ("[1-9 of 12 events]" not
// "[1-9 of 12]").
func (dm detailModel) activeTabLabel() string {
	switch dm.activeTab {
	case tabComments:
		return "comments"
	case tabEvents:
		return "events"
	case tabLinks:
		return "links"
	}
	return ""
}

// renderBody splits the issue body on newlines, hard-wraps each line,
// and returns the dm.scroll-windowed slice. Hard-wrap (truncate) keeps
// v1 simple; soft word-wrap is deferred. Body is sanitized before
// wrapping so agent-authored ANSI / control sequences cannot reach
// the terminal.
func (dm detailModel) renderBody(width, lines int) string {
	wrapped := renderMarkdownLines(dm.issue.Body, width)
	if len(wrapped) == 0 {
		return statusStyle.Render("(no description)")
	}
	start := dm.scroll
	if maxStart := len(wrapped) - lines; start > maxStart {
		if maxStart < 0 {
			maxStart = 0
		}
		start = maxStart
	}
	end := start + lines
	if end > len(wrapped) {
		end = len(wrapped)
	}
	return strings.Join(wrapped[start:end], "\n")
}

func (dm detailModel) renderChildrenSection(width, rows int) string {
	if rows <= 0 || len(dm.children) == 0 {
		return ""
	}
	cursor := clampInt(dm.childCursor, 0, len(dm.children)-1)
	visible, vCursor := windowChildIssues(dm.children, cursor, rows)
	lines := []string{}
	for i, child := range visible {
		line := renderChildIssueRow(child, i == vCursor && dm.detailFocus == focusChildren, width)
		if i == vCursor && dm.detailFocus == focusChildren {
			line = selectedStyle.Render(padToWidth(line, width))
		} else if i%2 == 1 {
			line = altRowStyle.Render(padToWidth(line, width))
		} else {
			line = normalRowStyle.Render(padToWidth(line, width))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderChildIssueRow(child Issue, selected bool, width int) string {
	const (
		markerW = 2
		numW    = 7
		statusW = 10
		ownerW  = 12
		updateW = 10
	)
	titleW := width - markerW - numW - statusW - ownerW - updateW
	if titleW < 12 {
		titleW = 12
	}
	marker := " "
	if selected {
		marker = ">"
	}
	parts := []string{
		padToWidth(marker, markerW),
		padToWidth(fmt.Sprintf("#%d", child.Number), numW),
		padToWidth(statusChip(child), statusW),
		padToWidth(truncate(sanitizeForDisplay(child.Title), titleW), titleW),
		padToWidth(truncate(sanitizeForDisplay(ownerText(child.Owner)), ownerW-1), ownerW),
		padToWidth(humanizeRelative(child.UpdatedAt), updateW),
	}
	return strings.Join(parts, "")
}

func windowChildIssues(issues []Issue, cursor, budget int) ([]Issue, int) {
	n := len(issues)
	if n == 0 {
		return issues, 0
	}
	start, end := windowBounds(n, cursor, budget)
	return issues[start:end], cursor - start
}

// wrapBody splits s on newlines, then hard-wraps each segment to width.
func wrapBody(s string, width int) []string {
	if s == "" {
		return nil
	}
	if width < 1 {
		width = 1
	}
	out := []string{}
	for _, raw := range strings.Split(s, "\n") {
		if raw == "" {
			out = append(out, "")
			continue
		}
		out = append(out, hardWrap(raw, width)...)
	}
	return out
}

// hardWrap breaks s into chunks no wider than width cells.
func hardWrap(s string, width int) []string {
	out := []string{}
	for runewidth.StringWidth(s) > width {
		head := runewidth.Truncate(s, width, "")
		if head == "" {
			_, sz := utf8.DecodeRuneInString(s)
			out = append(out, s[:sz])
			s = s[sz:]
			continue
		}
		out = append(out, head)
		s = s[len(head):]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// renderActiveTab dispatches to the per-tab renderer. The header line
// "Comments (N)" / "Events (N)" / "Links (N)" sits above the entries
// and is always rendered (even on empty data) so the tab strip + count
// stays consistent across tab switches. The per-tab loading/err state
// is forwarded so the renderer can substitute "(loading...)" or an
// error chip for the entry list.
func (dm detailModel) renderActiveTab(width, height int) string {
	switch dm.activeTab {
	case tabComments:
		return renderCommentsTab(dm.comments, width, height, dm.tabCursor,
			tabState{loading: dm.commentsLoading, err: dm.commentsErr})
	case tabEvents:
		return renderEventsTab(dm.events, width, height, dm.tabCursor,
			tabState{loading: dm.eventsLoading, err: dm.eventsErr})
	case tabLinks:
		return renderLinksTab(dm.links, width, height, dm.tabCursor,
			tabState{loading: dm.linksLoading, err: dm.linksErr})
	}
	return ""
}

func (dm detailModel) hasActivity() bool {
	return len(dm.comments) > 0 || len(dm.events) > 0 || len(dm.links) > 0 ||
		dm.commentsLoading || dm.eventsLoading || dm.linksLoading ||
		dm.commentsErr != nil || dm.eventsErr != nil || dm.linksErr != nil
}
