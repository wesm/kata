package tui

import (
	"io"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestResolveColorMode_NoColorOverridesAll(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("KATA_COLOR_MODE", "dark")
	if got := resolveColorMode(); got != colorNone {
		t.Fatalf("NO_COLOR=1 must force colorNone, got %v", got)
	}
}

func TestResolveColorMode_KataColorModeRespected(t *testing.T) {
	cases := map[string]colorMode{
		"":      colorAuto,
		"auto":  colorAuto,
		"dark":  colorDark,
		"light": colorLight,
		"none":  colorNone,
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("KATA_COLOR_MODE", in)
			if got := resolveColorMode(); got != want {
				t.Fatalf("KATA_COLOR_MODE=%q -> %v, want %v", in, got, want)
			}
		})
	}
}

func TestResolveColorMode_InvalidFallsBackToAuto(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("KATA_COLOR_MODE", "rainbow")
	if got := resolveColorMode(); got != colorAuto {
		t.Fatalf("invalid value should fall back to colorAuto, got %v", got)
	}
}

func TestApplyColorMode_NoneStripsForeground(t *testing.T) {
	applyColorMode(colorNone, io.Discard)
	rendered := titleStyle.Render("hello")
	if rendered != "hello" {
		t.Fatalf("colorNone should render plain text, got %q", rendered)
	}
}

// TestApplyColorMode_RebuildsAllStyles guards against silently
// forgetting a style var in applyColorMode (which would leak the prior
// mode's value across boots). We pre-poison every var with a sentinel
// foreground (a real lipgloss.Color) so that GetForeground returns
// that exact value. After applyColorMode(colorNone) every var must
// have shed the sentinel foreground (colorNone leaves Foreground unset
// or a different value entirely).
func TestApplyColorMode_RebuildsAllStyles(t *testing.T) {
	sentinelColor := lipgloss.Color("999")
	sentinel := lipgloss.NewStyle().Foreground(sentinelColor)
	titleStyle = sentinel
	subtleStyle = sentinel
	statusStyle = sentinel
	selectedStyle = sentinel
	openStyle = sentinel
	closedStyle = sentinel
	deletedStyle = sentinel
	helpKeyStyle = sentinel
	helpDescStyle = sentinel
	errorStyle = sentinel
	toastStyle = sentinel
	chipStyle = sentinel
	chipActive = sentinel
	tabActive = sentinel
	tabInactive = sentinel
	detailMetaStyle = sentinel
	detailSectionHeaderStyle = sentinel
	markdownCodeBlockStyle = sentinel
	titleBarStyle = sentinel
	statsLineStyle = sentinel
	tableHeaderStyle = sentinel
	separatorRuleStyle = sentinel
	cursorRowStyle = sentinel
	altRowStyle = sentinel
	normalRowStyle = sentinel
	footerBarStyle = sentinel
	modalBoxStyle = sentinel
	// Panel-border vars are TerminalColor (not Style); poison them
	// with the same sentinel value so we can detect a forgotten
	// rebuild via the same colorNone test.
	panelActiveBorder = sentinelColor
	panelInactiveBorder = sentinelColor

	applyColorMode(colorNone, io.Discard)

	all := []lipgloss.Style{
		titleStyle, subtleStyle, statusStyle, selectedStyle,
		openStyle, closedStyle, deletedStyle, helpKeyStyle,
		helpDescStyle, errorStyle, toastStyle, chipStyle,
		chipActive, tabActive, tabInactive,
		detailMetaStyle, detailSectionHeaderStyle, markdownCodeBlockStyle,
		titleBarStyle, statsLineStyle, tableHeaderStyle,
		separatorRuleStyle, cursorRowStyle, altRowStyle,
		normalRowStyle, footerBarStyle, modalBoxStyle,
	}
	for i, s := range all {
		if fg, ok := s.GetForeground().(lipgloss.Color); ok && fg == sentinelColor {
			t.Fatalf("style %d not rebuilt by applyColorMode(colorNone): retained sentinel %q", i, fg)
		}
	}
	if c, ok := panelActiveBorder.(lipgloss.Color); ok && c == sentinelColor {
		t.Fatal("panelActiveBorder not rebuilt by applyColorMode(colorNone)")
	}
	if c, ok := panelInactiveBorder.(lipgloss.Color); ok && c == sentinelColor {
		t.Fatal("panelInactiveBorder not rebuilt by applyColorMode(colorNone)")
	}
}

// TestApplyColorMode_DeletedStyleIsRedFaint locks the M0 semantic
// remap: deletedStyle uses roborev's failStyle codes (124/196) with
// Faint so soft-deleted rows read as out-of-band but not alarming.
// Earlier the codes were gray (243/245) — that didn't differentiate
// from statusStyle.
func TestApplyColorMode_DeletedStyleIsRedFaint(t *testing.T) {
	applyColorMode(colorDark, io.Discard)
	assertStyleForeground(t, deletedStyle, "deletedStyle dark", "196")
	if !deletedStyle.GetFaint() {
		t.Fatal("deletedStyle must be faint so the red doesn't read as an error chip")
	}
}

func TestApplyColorMode_StatusColorsStayDistinctInWarmDisplays(t *testing.T) {
	applyColorMode(colorDark, io.Discard)
	assertStyleForeground(t, openStyle, "openStyle dark", "46")
	assertStyleForeground(t, closedStyle, "closedStyle dark", "245")

	applyColorMode(colorLight, io.Discard)
	assertStyleForeground(t, closedStyle, "closedStyle light", "240")
}

// TestApplyColorMode_PanelBorderColorsBound asserts the M3+ panel
// border vars are bound after a normal-mode apply. M0 introduces these
// vars even though the first usage lands in M3a — locking the values
// here keeps them honest.
func TestApplyColorMode_PanelBorderColorsBound(t *testing.T) {
	applyColorMode(colorDark, io.Discard)
	if panelActiveBorder == nil {
		t.Fatal("panelActiveBorder must be bound by applyColorMode(colorDark)")
	}
	if panelInactiveBorder == nil {
		t.Fatal("panelInactiveBorder must be bound by applyColorMode(colorDark)")
	}
	assertTerminalColor(t, panelActiveBorder, "panelActiveBorder dark", "205")
	assertTerminalColor(t, panelInactiveBorder, "panelInactiveBorder dark", "246")
}
