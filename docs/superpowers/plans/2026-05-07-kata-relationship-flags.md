# Plan — Relationship Flags on Create + Edit

**Tracks:** kata#1
**Spec:** `docs/superpowers/specs/2026-05-07-kata-relationship-flags.md`

## Step 1 — Daemon: extend PATCH with `links_delta` and priority

Files: `internal/api/issues.go`, `internal/api/types.go`, `internal/store/links.go` (or wherever the link DB ops live).

- Add `LinksDelta` request type matching the spec §3 wire shape.
- Add `priority` to the PATCH request body. Existing `*int64 omitempty` semantics apply: omitted means "no change," explicit `null` means "clear," integer 0..4 means "set." (The standalone priority action endpoint stays in service for the TUI; this change adds priority to PATCH, doesn't remove the action endpoint.)
- Validate the delta server-side: parent assertion, no internal conflicts, no self-links, cycle check on resulting graph, priority range.
- Apply in one DB transaction with the existing PATCH field updates (title/body/owner) and the new priority field. Return the updated issue plus the applied delta in the response so the CLI can render `changes` without a follow-up read.
- Emit a single `issue.links_changed` event post-commit if any link mutation applied, with a payload listing all adds and removes. Emit `issue.updated` if non-link fields changed (priority changes count as field changes for this event). Both events fire if both classes of change happened.
- Tests:
  - PATCH with `links_delta` only succeeds.
  - PATCH with combined field + link changes is atomic (inject a forced cycle and confirm the title change is rolled back).
  - PATCH with title + priority + links_delta produces exactly one `issue.updated` and one `issue.links_changed` event.
  - `remove_parent` mismatch returns 409 with a structured error.
  - Idempotent multi-removes succeed and emit no `issue.links_changed` event.
  - Self-link in `add` returns 400.

## Step 2 — CLI: implement add flags on `kata create`

File: `cmd/kata/create.go`.

- Replace existing `--parent` and `--blocks` registration with the full set: `--parent`, `--blocks`, `--blocked-by`, `--related`. All except `--parent` are `Int64SliceVar`.
- Build the `links` array unchanged in shape, but populated from all four flags.
- Client-side conflict checks (self-link, duplicate non-`--parent` flags collapsed, conflicting `--parent` values rejected).
- Update the existing test file `cmd/kata/create_test.go` to cover all four flags including repeats and conflicts.

## Step 3 — CLI: implement add and remove flags on `kata edit`

File: `cmd/kata/edit.go`.

- Register all eight new flags plus the existing `--title`, `--body`, `--owner`, `--priority`.
- Build a `links_delta` from the flags. Reuse the conflict-validation helper from Step 2 (factor out into `cmd/kata/links.go` or `internal/cli/links/`).
- Edit becomes a single PATCH call. `--priority` (and the dash-clear sentinel) is sent in the PATCH body alongside title/body/owner and `links_delta`. Delete the priority-action POST path from the CLI.
- Replace the "pass at least one of --title, --body, --owner, --priority" error to include any link flag.
- Update the JSON output to merge a `changes` block from the daemon response.
- Tests in `cmd/kata/edit_test.go`:
  - Each link flag adds and removes correctly in isolation.
  - Combined field + link mutations succeed.
  - `--remove-parent N` mismatch surfaces a clear error from the daemon.
  - All client-side conflict checks fire before any HTTP call.
  - `--json` output contains the expected `changes` shape.

## Step 4 — Delete the eight old commands

Files: `cmd/kata/link.go`, `cmd/kata/link_test.go`, `cmd/kata/main.go` (registration), README, CLAUDE.md, AGENTS.md, `cmd/kata/quickstart.go`.

- Delete `cmd/kata/link.go` and `cmd/kata/link_test.go`.
- Remove registrations from `cmd/kata/main.go`.
- Update README's "Labels, ownership, and relationships" section to remove all eight commands and replace with `kata edit` examples.
- Update CLAUDE.md "Project management" section so the link-management bullet points to `kata edit --blocks N` etc.
- Update AGENTS.md identically.
- Rewrite the relationship example in `cmd/kata/quickstart.go` to use only `create` and `edit`.
- Run `go build ./...` and `go test ./...` to flush out internal callers; fix anything that breaks.

## Step 5 — Audit external callers

- Grep `roborev` and any other repos in `~/Documents/GitHub/` and `~/git/` that shell out to kata's CLI for the eight removed commands. Report findings to Jesse before merging.
  - `rg -l "kata (block|unblock|parent|unparent|relate|unrelate|link|unlink) " ~/Documents/GitHub ~/git`
- Hooks: check `kata daemon logs --hooks` examples and any shipped hook templates for old-command usage.

## Step 6 — Update kata#1's acceptance checklist

Comment on kata#1 with the implementation notes and tick the acceptance boxes via a comment summary (the body checklist is informational; the comment is the durable record).

## Verification before completion

- `make test` passes.
- `make lint` passes.
- `kata --workspace <fresh-tmp> init`, then run through the spec's example session end-to-end with the new CLI: create with all four add flags, edit to add and remove, edit with `--remove-parent` mismatch and confirm the error, edit with no-op idempotent removes and confirm `changes: {}`.
- `kata quickstart` text reads cleanly and contains no references to the deleted commands.
- README rendered (locally or on GitHub) shows the new flag table where the old command list used to be.
