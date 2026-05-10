package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDelete_NoForceIsValidationError(t *testing.T) {
	f := newCLIFixture(t)
	short := createIssueViaHTTP(t, f.env, f.dir, "to be deleted")

	err := f.execute("delete", short)
	ce := requireCLIError(t, err, ExitValidation)
	assert.Contains(t, ce.Message, "--force")
}

func TestDelete_ForceWithConfirmSoftDeletes(t *testing.T) {
	f := newCLIFixture(t)
	short := createIssueViaHTTP(t, f.env, f.dir, "to be deleted")

	require.NoError(t, f.execute("delete", short, "--force", "--confirm", "DELETE kata#"+short))
	assert.Contains(t, f.buf.String(), "deleted")
}

func TestDelete_ConfirmMismatchIsExit6(t *testing.T) {
	f := newCLIFixture(t)
	short := createIssueViaHTTP(t, f.env, f.dir, "to be deleted")

	err := f.execute("delete", short, "--force", "--confirm", "DELETE wrong")
	ce := requireCLIError(t, err, ExitConfirm)
	assert.True(t, strings.Contains(ce.Code, "confirm_mismatch"))
}

// TestDelete_NoTTYNoConfirmIsConfirmRequired pins the resolveConfirm branch
// where stdin isn't a TTY and --confirm wasn't passed.
func TestDelete_NoTTYNoConfirmIsConfirmRequired(t *testing.T) {
	stubIsTTY(t, false)
	f := newCLIFixture(t)
	short := createIssueViaHTTP(t, f.env, f.dir, "to be deleted")

	err := f.execute("delete", short, "--force")
	ce := requireCLIError(t, err, ExitConfirm)
	assert.Equal(t, "confirm_required", ce.Code)
}
