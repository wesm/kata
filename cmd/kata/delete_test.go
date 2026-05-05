package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDelete_NoForceIsValidationError(t *testing.T) {
	f := newCLIFixture(t)
	createIssueViaHTTP(t, f.env, f.dir, "to be deleted")

	err := f.execute("delete", "1")
	ce := requireCLIError(t, err, ExitValidation)
	assert.Contains(t, ce.Message, "--force")
}

func TestDelete_ForceWithConfirmSoftDeletes(t *testing.T) {
	f := newCLIFixture(t)
	createIssueViaHTTP(t, f.env, f.dir, "to be deleted")

	require.NoError(t, f.execute("delete", "1", "--force", "--confirm", "DELETE #1"))
	assert.Contains(t, f.buf.String(), "deleted")
}

func TestDelete_ConfirmMismatchIsExit6(t *testing.T) {
	f := newCLIFixture(t)
	createIssueViaHTTP(t, f.env, f.dir, "to be deleted")

	err := f.execute("delete", "1", "--force", "--confirm", "DELETE #2")
	ce := requireCLIError(t, err, ExitConfirm)
	assert.True(t, strings.Contains(ce.Code, "confirm_mismatch"))
}

// TestDelete_NoTTYNoConfirmIsConfirmRequired pins the resolveConfirm branch
// where stdin isn't a TTY and --confirm wasn't passed. Replaces the real
// isTTY (which sees the developer's terminal under `go test`) with a stub
// that always reports false, so the assertion is deterministic.
func TestDelete_NoTTYNoConfirmIsConfirmRequired(t *testing.T) {
	stubIsTTY(t, false)
	f := newCLIFixture(t)
	createIssueViaHTTP(t, f.env, f.dir, "to be deleted")

	err := f.execute("delete", "1", "--force")
	ce := requireCLIError(t, err, ExitConfirm)
	assert.Equal(t, "confirm_required", ce.Code)
}
