# Cutover wedge on pre-existing FK orphans (issue #43) — design

## Problem

`jsonl.AutoCutover` runs export → fresh DB → import to migrate older
SQLite databases to the current schema. The importer's
`validateBeforeCommit` runs `PRAGMA foreign_key_check` and aborts the
transaction on any violation, with a single-line message:

```
kata: foreign_key_check: violations found
{"error":{"kind":"internal","message":"daemon failed to start within 5s","exit_code":1}}
```

Operators whose source DB has pre-existing FK corruption (e.g. residue
from a corruption-recovery event on an older schema where the FKs were
not actively enforced) get wedged: every CLI call re-runs the cutover,
re-fails identically, and the daemon never starts. The error discards
the per-row detail SQLite would have given us, and there is no
operator-facing recovery path.

## Diagnosis

The reporter (issue #43) attributed the failure to all 62 orphan rows
in their source DB (13 events, 39 links, 10 comments). Tracing the
existing exporters narrows it:

- `exportComments` (`internal/jsonl/export.go:467`) uses
  `JOIN issues ON issues.id = comments.issue_id` — INNER JOIN, comment
  orphans are silently dropped at export and never reach the importer.
- `exportLinks` (`internal/jsonl/export.go:523`) uses INNER JOIN on
  both endpoints — link orphans are silently dropped at export.
- `exportIssueLabels` (`internal/jsonl/export.go:489`) uses INNER JOIN
  on `issues` — issue_label orphans are silently dropped at export.
- `exportEvents` (`internal/jsonl/export.go:664`) reads `FROM events`
  directly with only a `LEFT JOIN` for the `related_issue_id` peer
  scrub. Orphan `events.issue_id` rows are *not* filtered, so they
  reach the importer and trigger the FK violation. Orphan
  `events.related_issue_id` rows are NULL-scrubbed *only* for
  `type = 'issue.links_changed'` events whose peer is soft-deleted —
  fully-missing peers (and other event types) still leak through.

So in the reported scenario, only the 13 orphan events actually cause
the cutover to fail; the 49 comment/link orphans were silently dropped
on every prior cutover attempt without anyone noticing. The fix needs
to (a) bring events into line with the existing
silent-drop-at-export precedent, (b) make the failure mode visible
when it happens, and (c) hard-fail with detail when an *unknown*
class of FK violation appears.

The reporter also referenced a `lost_and_found` table as "already in
the schema." It is not — `internal/db/schema.sql` has no such table.
This design does not add one.

## Goals

- Reported scenario unwedges on the next CLI invocation, without
  operator action.
- Operator gets a one-line stderr summary so the silent data loss is
  not actually silent.
- Pre-existing FK corruption in classes the cutover does *not* know
  how to handle aborts the cutover with actionable detail (table,
  rowid, parent, FK column) so the operator can repair the source DB
  manually rather than chasing a generic message.
- No source-DB mutation — the cutover stays read-only against the
  source, builds a fresh DB, and swaps. Operator's original DB and
  the timestamped backup remain untouched.
- No new CLI commands, flags, or schema. Single-PR fix.

## Non-goals

- `kata db repair` / `kata db fsck` style command. Discussed and
  rejected — would shift recovery onto the operator for a class of
  corruption the cutover can already handle.
- A `lost_and_found` table or any source-DB mutation. The fresh DB
  has no orphans by construction; we don't need a quarantine table.
- A `--accept-orphan-loss` flag. Conditional gating adds UX surface
  for a behavior we already do silently for comments/links/labels.
- Hard-failing on comment/link/issue_label orphans at preflight.
  That would change long-standing behavior and re-wedge any operator
  who has been silently surviving those orphans on prior cutovers.
  Codex's competing design proposed this; we deliberately reject it.

## Design

### Three layers, one direction of flow

```
runDaemonWithListen              cmd/kata/daemon_cmd.go:201
  └ AutoCutover                  internal/jsonl/cutover.go
      ├ preflightSourceFKs       NEW: classifier; halt on unknown classes
      ├ exportCutoverSource      export.go: events scrub joins added
      ├ importCutoverTarget      import.go: validateBeforeCommit detail
      ├ backup + rename          (unchanged)
      └ stderr summary           NEW: one line, nonzero classes only
```

The preflight is a **classifier and report**, not the safety
mechanism. Cutover is made safe by the export-time scrubs. The
preflight names what export will drop (and what export *cannot*
drop, in which case the cutover halts before the export wastes work
and surfaces a clearer error than the importer's defense-in-depth
catch).

### Layer 1 — `preflightSourceFKs` (new, read-only)

Open the source DB read-only. Run `PRAGMA foreign_key_check`. For
each row `(child_table, rowid, parent_table, fkid)`:

1. Resolve `fkid` to the actual offending column by querying
   `PRAGMA foreign_key_list(<child_table>)` once per child table and
   matching `id = fkid`. SQLite's `foreign_key_list` returns the FK
   constraint at index `id` whose `from` column is the FK column
   name in the child table. Caching the lookup per table is a small
   optimization but worth doing — the orphan list could be long.
2. Classify the violation against the known-orphan-class table
   below. Increment the per-class counter in `OrphanReport`, or
   append to `UnknownViolations` if no class matches.

**Known orphan classes** (these ones cutover handles via export):

| Child table     | FK column         | Parent | Disposition at export      |
|-----------------|-------------------|--------|----------------------------|
| `comments`      | `issue_id`        | issues | drop entire row            |
| `links`         | `from_issue_id`   | issues | drop entire row            |
| `links`         | `to_issue_id`     | issues | drop entire row            |
| `issue_labels`  | `issue_id`        | issues | drop entire row            |
| `events`        | `issue_id`        | issues | drop entire event          |
| `events`        | `related_issue_id`| issues | NULL-scrub, preserve event |

If `len(UnknownViolations) > 0`: return an error wrapping the list.
The error message names every violation (capped at 20 rows per child
table to bound output) plus a remediation hint:

```
preflight: source DB at <path> has unhandled foreign-key corruption
that cutover cannot resolve. Inspect with
`sqlite3 <path> 'PRAGMA foreign_key_check;'` and repair before
retrying. Found:
  project_aliases rowid=1 parent=projects column=project_id
  ...
```

### Layer 2 — Export-time scrub for events

Change `exportEvents` (the schema_version >= 8 path,
`internal/jsonl/export.go:618`) and the V1/V2/V3 variants
(`exportEventsV1` / `V2` / `V3`) to:

1. **Drop events whose `issue_id` is an orphan.** Add
   `LEFT JOIN issues primary ON primary.id = events.issue_id` and
   filter with `WHERE events.issue_id IS NULL OR primary.id IS NOT NULL`.
   Keeps project-level events (NULL `issue_id`) unchanged; drops
   events whose `issue_id` references a now-missing issue.
2. **NULL-scrub `events.related_issue_id` for any orphan peer**, not
   just the `issue.links_changed` + soft-deleted-peer case. The
   existing `LEFT JOIN issues peer ON peer.id = events.related_issue_id`
   already supplies the join; broaden the `CASE` expression so that
   `peer.id IS NULL AND events.related_issue_id IS NOT NULL` also
   triggers the NULL scrub for both `related_issue_id` and
   `related_issue_uid`. The existing soft-deleted-peer rule
   (`peer.deleted_at IS NOT NULL` for live-only export) stays in
   place as a special case alongside the new fully-missing rule.

**Semantic justification for the asymmetry between the two columns.**
`events.issue_id` is the event's primary subject — it identifies
which issue the event happened *to*. An event whose subject no
longer exists has no anchor in the post-cutover DB and is dropped.
`events.related_issue_id` is contextual: it points to a peer
involved in the event (e.g. the other endpoint of a link change).
The event's meaning survives losing that context; we preserve the
event row and NULL the related fields, matching the precedent the
soft-deleted-peer scrub already established.

### Layer 3 — `validateBeforeCommit` diagnostic detail

Rewrite `validateBeforeCommit` (`internal/jsonl/import.go:704`) to
scan every `foreign_key_check` row and produce a grouped, detailed
error:

```
foreign_key_check: 3 violations:
  events rowid=17 parent=issues column=issue_id
  events rowid=23 parent=issues column=related_issue_id
  comments rowid=8 parent=issues column=issue_id
```

The exact wording is constrained by an existing test edit at
`internal/jsonl/import_test.go:168` which asserts the substring
`"project_aliases rowid=1 parent=projects"`. We adopt this as the
canonical row format: `<table> rowid=<rowid> parent=<parent>` with
`column=<col>` appended. The `column` resolution uses the same
`foreign_key_list` lookup as the preflight; reuse the helper.

Cap output at 20 rows per child table (same as preflight) to bound
log size when corruption is widespread.

This layer should never fire after preflight + export scrub, but it
catches the failure mode where a future schema change adds a NOT
NULL FK we didn't update the export scrub for.

### Stderr summary

After cutover succeeds, print exactly one line to `os.Stderr`:

```
kata cutover: discarded N orphan rows from old DB (events: 13, comments: 10, links: 39)
```

Format rules:
- Total `N` is the sum of all nonzero counts.
- Only nonzero classes are listed, in the fixed order
  `events, comments, links, issue_labels`. This avoids `(comments: 0, links: 0, ...)` noise on the
  more common case where only one class has orphans.
- If every count is zero, no line is printed at all.
- `events` here represents events whose primary `issue_id` was
  dropped. NULL-scrubbed `related_issue_id` events are *not*
  counted in the summary — the event row survived, and counting
  scrubs in the same total as drops would mislead the operator
  about what was actually lost.

Print directly to `os.Stderr` from inside `AutoCutover`. Do not add
an injectable writer to `AutoCutover`'s signature for testability —
the formatter can be tested separately, and tests that need to
capture stderr can redirect `os.Stderr` for the duration of the
call. Keeping the public surface unchanged is worth more than
test ergonomics for one print statement.

## Files touched

| File                                  | Change                                                                                  |
|---------------------------------------|-----------------------------------------------------------------------------------------|
| `internal/jsonl/cutover.go`           | Add `preflightSourceFKs`, `OrphanReport` type, `FKViolation` type; thread report through `AutoCutover`; print stderr summary on success |
| `internal/jsonl/export.go`            | Extend `exportEvents`, `exportEventsV3`, `exportEventsV2`, `exportEventsV1` with `issue_id` orphan filter and broadened `related_issue_id` NULL-scrub |
| `internal/jsonl/import.go`            | Rewrite `validateBeforeCommit` to scan, group, format with `foreign_key_list` lookup    |
| `internal/jsonl/cutover_test.go`      | Three new cutover tests (below)                                                         |
| `internal/jsonl/import_test.go`       | One new test for the diagnostic format; existing line 168 edit is incorporated as-is    |

## Tests

Test selection follows the bug report — all four known orphan
classes appeared together in the wedged DB, so the happy-path
cutover test seeds all four, not just events.

1. **`TestAutoCutover_DropsAllKnownOrphanClasses`** —
   Build a v6 DB with: 3 valid issues, 1 event with orphan
   `issue_id`, 1 event with orphan `related_issue_id` (and a valid
   `issue_id`), 1 valid event, 2 orphan comments, 2 orphan links,
   1 orphan issue_label. Run `AutoCutover`. Assert success. Assert
   the resulting DB is at `CurrentSchemaVersion`, contains the
   3 valid issues, contains the valid event and the
   `related_issue_id`-scrubbed event (with NULL `related_issue_id`
   and `related_issue_uid`), and contains zero of the orphan rows
   from any class.

2. **`TestAutoCutover_HaltsOnUnknownFKClass`** —
   Build a DB with an orphan class the cutover does not handle
   (e.g. a `project_aliases` row whose `project_id` references a
   missing project, inserted with `foreign_keys=OFF`). Run
   `AutoCutover`. Assert error mentions `project_aliases`, `rowid`,
   `parent=projects`, and `column=project_id`. Assert the source
   DB is unchanged byte-for-byte (or at least row-count-equivalent)
   and tmp files are cleaned up.

3. **`TestAutoCutover_PrintsOrphanSummary`** —
   Reuse the test #1 setup. Redirect `os.Stderr` to a pipe for the
   duration of `AutoCutover`. Assert exactly one line matching the
   expected summary with the right per-class counts and only
   nonzero classes listed.

4. **`TestValidateBeforeCommit_GroupsAndFormats`** —
   The existing edit at `import_test.go:168` is the seed. Extend
   the test (or add a sibling) to also cover multi-row,
   multi-table grouping — seed two violations across two child
   tables, assert both rows appear in the formatted error.

## Open compatibility note

The summary's wording (`"discarded N orphan rows from old DB"`)
implies data loss, which is what's actually happening. We have
silently dropped comment/link/label orphans on every cutover for as
long as cutover has existed, so this is the first time the loss is
made visible. Existing operators won't see the line unless their
source DB actually has orphans — for clean DBs the line is
suppressed. This is consistent with not regressing the silent path.

## What we deliberately did *not* design

- A `kata db repair` command. Out of scope.
- A `lost_and_found` quarantine table. Schema doesn't have one and
  the cutover's "fresh DB swap" model doesn't need one.
- A `--accept-orphan-loss` flag or interactive prompt. The behavior
  is identical to existing comment/link/label drops; gating events
  separately would be inconsistent.
- Source-DB repair. The source stays read-only; the backup
  alongside it lets the operator recover anything they care about.
