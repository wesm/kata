package tui

import "github.com/wesm/kata/internal/textsafe"

// sanitizeForDisplay is the TUI-side alias for textsafe.Block — strips
// ANSI escape sequences, Unicode control characters, and Cf bidi
// overrides from agent-authored text before it lands on the terminal.
// Kept as a thin alias so existing call sites stay readable; the real
// logic lives in internal/textsafe so the CLI can share it.
func sanitizeForDisplay(s string) string { return textsafe.Block(s) }

// sanitizeForLine is the single-line variant. Same control / ANSI
// stripping as sanitizeForDisplay, plus newlines are replaced with the
// literal escape sequence "\n" so a multi-line value can't spill into
// extra physical rows that bypass chunk line-counting, clipping, and
// cursor anchoring. Use for tab rows where one value owns one row
// (events-tab close detail, link rows, etc.).
func sanitizeForLine(s string) string { return textsafe.Line(s) }
