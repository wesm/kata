package main

import (
	"strings"
	"testing"
)

// kata tui needs a TTY, so we exercise the registration via --help;
// cobra prints help text and returns before RunE is invoked.
//
// --all-projects and --include-deleted are intentionally NOT
// registered: the daemon has no cross-project list endpoint and no
// include_deleted query param, so either flag would advertise a
// capability the wire cannot deliver. Both gates land at the daemon
// boundary; re-add when handlers_issues.go grows the routes.
func TestTUI_CommandRegistered(t *testing.T) {
	out, err := runCmdOutput(t, nil, "tui", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--uid-format") {
		t.Fatalf("--uid-format missing from help: %s", out)
	}
	for _, banned := range []string{"--all-projects", "--include-deleted"} {
		if strings.Contains(out, banned) {
			t.Fatalf("%s leaked back into help (daemon support not yet wired): %s",
				banned, out)
		}
	}
}

func TestTUI_RejectsInvalidUIDFormatBeforeTTYCheck(t *testing.T) {
	_, err := runCmdOutput(t, nil, "tui", "--uid-format", "wide")
	if err == nil {
		t.Fatal("expected invalid uid format error")
	}
	if !strings.Contains(err.Error(), "uid format must be one of none, short, full") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestTUI_RejectsExtraArgs guards the cobra.NoArgs constraint: a typo'd
// positional must error out before RunE so the user sees a usage
// failure (and the TTY check in tui.Run is never reached, which would
// be inappropriate for an arg-parse failure).
func TestTUI_RejectsExtraArgs(t *testing.T) {
	_, err := runCmdOutput(t, nil, "tui", "extra-positional")
	if err == nil {
		t.Fatal("expected error for extra positional arg")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown command") &&
		!strings.Contains(msg, "accepts no args") {
		t.Fatalf("unexpected error: %v", err)
	}
}
