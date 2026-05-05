package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestEdit_EmptyTitle_ValidatedClientSide covers hammer-test
// finding #4: edit --title "" (or whitespace-only) used to forward
// the value to the daemon, which returned a raw SQLite CHECK
// constraint error. Now blocked client-side with a kindValidation
// cliError that points the user at the right action (omit the
// flag to keep the existing title).
func TestEdit_EmptyTitle_ValidatedClientSide(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t\n"} {
		_, err := runCmdOutput(t, nil, "edit", "1", "--title", blank)
		ce := requireCLIError(t, err, ExitValidation)
		assert.Equal(t, kindValidation, ce.Kind)
		assert.Contains(t, ce.Message, "must not be empty",
			"validation message should explain the failure for %q", blank)
	}
}
