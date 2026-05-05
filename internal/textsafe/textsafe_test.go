package textsafe

import "testing"

// TestBlock covers the multi-line sanitizer. Each case names the threat
// or property under test; exact-string equality implicitly proves the
// dangerous bytes were stripped.
func TestBlock(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// CSI escape (ESC [ 2 J) would blank the user's terminal.
		{"strips_ansi_csi", "before\x1b[2Jafter", "beforeafter"},
		// OSC family — title sets and OSC-8 hyperlinks can render as
		// legitimate-looking text linking to an attacker URL.
		{"strips_ansi_osc", "click \x1b]8;;https://evil.example/\x1b\\here\x1b]8;;\x1b\\!", "click here!"},
		// U+202E RIGHT-TO-LEFT OVERRIDE visually inverts subsequent
		// text; Cf runes are stripped alongside C0/C1 controls.
		{"strips_bidi_override", "safe\u202eevil", "safeevil"},
		// Block is for multi-line contexts, so legitimate body content
		// with newlines and tabs survives.
		{"preserves_newlines_and_tabs", "line one\nline two\tindented", "line one\nline two\tindented"},
		{"empty_string", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Block(tc.in); got != tc.want {
				t.Fatalf("Block(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestLine covers the single-line sanitizer, which stacks on Block and
// additionally collapses newlines/tabs that would break row layout.
func TestLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// Embedded newlines become the literal `\n` so the row stays
		// one visual line.
		{"replaces_newline_with_literal_escape", "title with\nembedded newline", `title with\nembedded newline`},
		// Tabs render at variable widths across terminals and break
		// column alignment; replace with a single space.
		{"replaces_tabs_with_space", "title\twith\ttabs", "title with tabs"},
		// Line inherits ANSI stripping from Block.
		{"strips_ansi", "title\x1b[31mred\x1b[0mback", "titleredback"},
		// \r in a single-row context can rewind the cursor and
		// overwrite the rest of the row.
		{"strips_carriage_return", "good\rbad", "goodbad"},
		{"empty_string", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Line(tc.in); got != tc.want {
				t.Fatalf("Line(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
