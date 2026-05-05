package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPurge_NoForceIsValidationError(t *testing.T) {
	f := newCLIFixture(t)
	createIssueViaHTTP(t, f.env, f.dir, "vaporize")

	err := f.execute("purge", "1")
	_ = requireCLIError(t, err, ExitValidation)
}

func TestPurge_ForceWithConfirmRemovesEverything(t *testing.T) {
	f := newCLIFixture(t)
	createIssueViaHTTP(t, f.env, f.dir, "vaporize")

	require.NoError(t, f.execute("purge", "1", "--force", "--confirm", "PURGE #1"))
	assert.Contains(t, f.buf.String(), "purged")
}

// TestPurge_NoTTYNoConfirmIsConfirmRequired mirrors the delete coverage:
// non-terminal stdin + missing --confirm must surface as exit 6
// confirm_required, not as a confirm_mismatch from an empty TTY read.
func TestPurge_NoTTYNoConfirmIsConfirmRequired(t *testing.T) {
	stubIsTTY(t, false)
	f := newCLIFixture(t)
	createIssueViaHTTP(t, f.env, f.dir, "vaporize")

	err := f.execute("purge", "1", "--force")
	ce := requireCLIError(t, err, ExitConfirm)
	assert.Equal(t, "confirm_required", ce.Code)
}

// TestPurge_ReasonFlagPersistsToPurgeLog verifies that `--reason "..."`
// flows through the CLI → HTTP body → daemon → DB so the purge_log.reason
// column captures the operator's free-text justification.
func TestPurge_ReasonFlagPersistsToPurgeLog(t *testing.T) {
	f := newCLIFixture(t)
	createIssueViaHTTP(t, f.env, f.dir, "vaporize")

	const wantReason = "spam test data"
	require.NoError(t, f.execute("purge", "1",
		"--force", "--confirm", "PURGE #1", "--reason", wantReason))

	var got *string
	err := f.env.DB.QueryRowContext(context.Background(),
		`SELECT reason FROM purge_log ORDER BY id DESC LIMIT 1`).Scan(&got)
	require.NoError(t, err)
	require.NotNil(t, got, "purge_log.reason should not be NULL when --reason was provided")
	assert.Equal(t, wantReason, *got)
}
