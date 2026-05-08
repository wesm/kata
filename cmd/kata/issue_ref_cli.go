package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func resolveIssueRefForCommand(cmd *cobra.Command, ref string) (context.Context, string, int64, resolvedIssueRef, error) {
	return resolveIssueRefForCommandWithOptions(cmd, ref, false)
}

func resolveIssueRefForCommandWithOptions(cmd *cobra.Command, ref string, includeDeleted bool) (context.Context, string, int64, resolvedIssueRef, error) {
	ctx := cmd.Context()
	start, err := resolveStartPath(flags.Workspace)
	if err != nil {
		return nil, "", 0, resolvedIssueRef{}, err
	}
	baseURL, err := ensureDaemon(ctx)
	if err != nil {
		return nil, "", 0, resolvedIssueRef{}, err
	}
	pid, err := resolveProjectID(ctx, baseURL, start)
	if err != nil {
		return nil, "", 0, resolvedIssueRef{}, err
	}
	issue, err := resolveIssueRefWithOptions(ctx, baseURL, pid, ref, includeDeleted)
	if err != nil {
		return nil, "", 0, resolvedIssueRef{}, err
	}
	return ctx, baseURL, pid, issue, nil
}

// resolveRefToNumberOpts resolves one issue-ref string to its project-scoped
// issue number. Used by relationship flags on `kata create` and `kata edit`
// so they accept the same shapes as positional issue-ref args (#N, N, UID,
// or UID prefix). Numeric refs short-circuit without a daemon round-trip;
// UID/prefix refs go through the standard /api/v1/issues/{ref} resolution.
//
// flagName is folded into the validation error so the user knows which flag
// failed when one of several link flags is malformed.
//
// includeDeleted=true matches the soft-delete-tolerant lookup the daemon's
// remove paths use: the link row is real, and the user can still ask to
// clean it up even when the peer issue has been soft-deleted. The remove
// flags pass true; the add flags (which require a live target) pass false.
func resolveRefToNumberOpts(ctx context.Context, baseURL string, projectID int64, ref, flagName string, includeDeleted bool) (int64, error) {
	if strings.TrimSpace(ref) == "" {
		return 0, &cliError{
			Message:  fmt.Sprintf("%s must not be empty", flagName),
			Kind:     kindValidation,
			ExitCode: ExitValidation,
		}
	}
	r, err := resolveIssueRefWithOptions(ctx, baseURL, projectID, ref, includeDeleted)
	if err != nil {
		return 0, err
	}
	return r.Number, nil
}

// resolveRefSliceToNumbers maps every entry of refs through resolveRefToNumberOpts.
// Returns the slice in the original order. Empty refs is OK (nil out, nil err).
func resolveRefSliceToNumbers(ctx context.Context, baseURL string, projectID int64, refs []string, flagName string) ([]int64, error) {
	return resolveRefSliceToNumbersOpts(ctx, baseURL, projectID, refs, flagName, false, false)
}

// resolveRefSliceToNumbersIdempotentRemove is the variant the
// idempotent --remove-blocks / --remove-blocked-by / --remove-related
// flags use. In addition to soft-delete tolerance, it drops refs that
// resolve to "issue not found" entirely: the contract is "no link to
// N"; if there is no N at all, the end state already holds, so the
// request should succeed as a no-op rather than 404. Other resolution
// errors (validation / ambiguous prefix) still fail loudly so genuine
// typos surface.
//
// Strict --remove-parent keeps the not-found error; it asserts a fact
// about the current parent and a missing target is still a 4xx.
func resolveRefSliceToNumbersIdempotentRemove(ctx context.Context, baseURL string, projectID int64, refs []string, flagName string) ([]int64, error) {
	return resolveRefSliceToNumbersOpts(ctx, baseURL, projectID, refs, flagName, true, true)
}

func resolveRefSliceToNumbersOpts(ctx context.Context, baseURL string, projectID int64, refs []string, flagName string, includeDeleted, tolerateNotFound bool) ([]int64, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]int64, 0, len(refs))
	for _, r := range refs {
		n, err := resolveRefToNumberOpts(ctx, baseURL, projectID, r, flagName, includeDeleted)
		if err != nil {
			if tolerateNotFound && isCLINotFoundError(err) {
				continue
			}
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// isCLINotFoundError reports whether err names a "target issue not
// found" condition (the daemon's 404 / not_found / issue_not_found
// envelope). Used by idempotent-remove resolution so a typo to a
// completely missing issue produces the same no-op as a typo to an
// issue that simply has no current link.
func isCLINotFoundError(err error) bool {
	var ce *cliError
	if !errors.As(err, &ce) {
		return false
	}
	return ce.Kind == kindNotFound
}

// resolveSingletonRefToNumber resolves the StringSliceVar storage for an
// at-most-one flag (--parent, --remove-parent) and returns the issue
// number every entry resolves to. Rejects only when entries resolve to
// *different* issues, so equivalent forms — `2` and `#2`, a full UID
// and its prefix, the same string twice — succeed. Returns 0 when the
// slice is empty (caller checks the changed-flag separately).
//
// includeDeleted should match the lookup variant the caller would use
// downstream (true for --remove-parent so a soft-deleted asserted
// parent still resolves).
func resolveSingletonRefToNumber(ctx context.Context, baseURL string, projectID int64, values []string, flagName string, includeDeleted bool) (int64, error) {
	if len(values) == 0 {
		return 0, nil
	}
	first := strings.TrimSpace(values[0])
	firstNum, err := resolveRefToNumberOpts(ctx, baseURL, projectID, first, flagName, includeDeleted)
	if err != nil {
		return 0, err
	}
	for _, v := range values[1:] {
		trimmed := strings.TrimSpace(v)
		if trimmed == first {
			continue // exact-string repeat; no daemon round-trip needed
		}
		n, err := resolveRefToNumberOpts(ctx, baseURL, projectID, trimmed, flagName, includeDeleted)
		if err != nil {
			return 0, err
		}
		if n != firstNum {
			return 0, &cliError{
				Message: fmt.Sprintf("%s only accepts one ref; got %q (#%d) and %q (#%d) which resolve to different issues",
					flagName, first, firstNum, trimmed, n),
				Kind:     kindValidation,
				ExitCode: ExitValidation,
			}
		}
	}
	return firstNum, nil
}
