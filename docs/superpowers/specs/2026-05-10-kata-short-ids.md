# kata — Short IDs

**Status:** Design
**Date:** 2026-05-10
**Topic:** Replace per-project sequential `#N` issue numbers with short IDs derived from each issue's ULID, qualified by project name in cross-project references. Stable within a daemon; probabilistically unique across daemons (the federation merge story is deferred to a future spec).

## 1. Motivation

Each kata issue today has two identifiers:

- **`uid`** — a 26-char Crockford base32 ULID, the canonical/storage ID. Federation-stable, time-ordered, immutable. Used in FKs, links, JSONL, hooks, and event ordering.
- **`number`** — a per-project sequential integer (`#1`, `#2`, …) used as the display label.

Sequential numbers are stable only within one daemon. Two failure modes follow:

1. **Federation collision.** When kata grows a shared/server mode (per the existing shared-server-mode spec) or sync-by-export tooling, two daemons that have not yet synced can each create an issue at `#5` of the same project. After they merge, "`#5`" no longer denotes one specific issue.
2. **Cross-context reference rot.** Agents are expected to write references to issues in commit messages, comments, and external systems ("fixes #42"). When that commit is read by another daemon — or even by the same daemon after a project's number sequence is reset — `#42` resolves to a different issue or to nothing. The display label is doing the work of a stable identifier without the durability of one.

This spec replaces `#N` as kata's user-facing identifier with a **short ID derived from the ULID**, qualified by project name in cross-project references (`kata#abc4`). The replacement is wholesale: `#N` ceases to be a canonical reference at the cutover point.

## 2. Goals and non-goals

**Goals**

- A display ID that is **stable within a daemon** — once set, an issue's display reference never changes outside an explicit operator-driven cutover.
- A display ID that is **probabilistically unique across daemons**, with collisions rare enough at typical project sizes to be a documented edge case rather than routine. Coordination-free uniqueness across unsynced daemons is **not** a property of this design at the chosen length; see §4.3.
- Short enough to type and remember (4-character minimum) and to embed in commit messages without ceremony.
- No new generated identity to migrate, version, or collide on. The short ID is a function of the existing ULID.
- Focused single-pass schema delta: drop the `*_number` columns on `issues`, `projects`, `events`, and `purge_log`; add `issues.short_id`; add a CHECK on `projects.name`. Applied via the existing JSONL cutover in `internal/jsonl/cutover.go`.

**Non-goals**

- **Sub-issue dot-notation** (e.g. `kata#abc4.1.1`). Beads does this; the reparenting story is bad — `bd-a3f8.1.1` either has its ID changed (breaking every existing reference) or stays put and lies about the current parent. We confirmed Beads' behavior at `/Users/wesm/code/beads`: reparenting keeps the ID immutable and rewrites the dependency record, leaving the ID structurally misleading. Hierarchy stays as edges in the `links` table; rendering can be hierarchical when needed (TUI tree view, future `kata tree`).
- **Backwards-compatible `#N` input.** After cutover, `kata show 12` and `kata#12` no longer resolve. The cutover is the single migration moment; kata is in early public preview, so the install base can absorb a clean break. Old commit messages and comment bodies that contain `#N` strings remain readable as text but are no longer clickable references.
- **Replacing the ULID.** ULIDs remain the canonical storage ID, used everywhere FKs, JSONL, hooks, and event ordering touch issues.
- **Federation merge semantics.** This spec is forward-looking groundwork — it removes the deterministic-collision failure mode of `#N` (per-project counter rebound across daemons or after `kata reset-counter`) and reduces it to a probabilistic and rare event. It does not specify what happens at the moment two unsynced daemons sync and discover both stored `kata#abc4` for different ULIDs. That conflict resolution — including possible mechanisms like short-ID aliases for stale references — belongs in a future federation spec, alongside the other federation/sync mechanics.

## 3. Display format

| Form | Example | Where it appears |
|---|---|---|
| Qualified | `kata#abc4` | Cross-project references, commit messages, comments, JSON output's `id` field |
| Bare | `abc4` | CLI input/output in a workspace bound to a project; the project is implicit from `.kata.toml` |

The `#` sigil is preserved from the old format and serves as a parser hint distinguishing an issue reference from a slug or path. Qualified form parses by splitting on the **last** `#` in the token: project name to the left, short ID to the right.

Project names cannot contain `#` (added as a CHECK constraint on `projects.name`). The current schema places no such restriction; the cutover validates that no existing project name violates this and fails loudly if one does (forcing a manual rename before the cutover proceeds).

## 4. Short ID derivation

The short ID is **not generated**. It is the lowercased suffix of the issue's ULID, of length L:

```
short_id(ulid, L) = lowercase(ulid[26-L : 26])
```

Example: ULID `01HZNQ7VFPK1XGD8R5MABCD4EX` with L=4 → short ID `d4ex`.

The length L is chosen at issue creation per the auto-extend algorithm in §5. L is then immutable for the issue's lifetime, except as part of an explicit JSONL-cutover rebuild.

The minimum L is 4. The maximum L is 26 (the full ULID, unique by construction); in practice the loop never reaches anywhere close to that.

### 4.1 Why derive from the ULID rather than generate independently

- **No new identity.** Every existing issue already has a ULID, so the cutover backfills only "which suffix length to display," not a new identifier per issue.
- **Probabilistically unique across daemons.** Two ULIDs differ by 80 bits of random entropy; the lowercased last 4 chars of two independently-generated ULIDs collide ≈1 in 1M pairs. That is not "federation-safe" but it is a strict improvement over `#N`, which collides deterministically every time two unsynced daemons each create their fifth issue in the same project.
- **Single alphabet.** ULIDs are already Crockford base32 (`0-9A-HJKMNP-TV-Z`, no `I/L/O/U`). Reusing the alphabet means the short ID inherits the same readability rules — no new "what about `0` vs `O`" ambiguity to reason about.

### 4.2 Why auto-extend rather than fixed length plus a `-N` suffix

Both designs were considered. Auto-extend wins for three reasons:

- **One mechanism.** The display ID is always "enough chars of the ULID to be unique in this project." There is no synthetic counter, no `-1`/`-2` suffix to render.
- **Deterministic given a canonical processing order.** The §5 algorithm is order-dependent: if X and Y are issues whose ULIDs both end in `abc4`, processing X→Y gives `X=abc4, Y=yabc4` while processing Y→X gives `Y=abc4, X=xabc4`. The algorithm picks a deterministic answer when the order is fixed. **The canonical order is ULID-ascending** (i.e., creation-time order). The cutover replays in this order; in steady-state local creation, every new issue has the latest ULID and is therefore processed last, which matches the canonical order automatically. Out-of-order arrivals — federation merges (§4.3) and explicit `kata projects merge` (§9) — break the canonical order and may produce different display strings than a fresh ULID-ascending replay would. The merge handlers run the auto-extend algorithm on the imported side, leaving existing local short_ids stable; §4.3 covers the resulting cross-daemon disagreement.
- **Precedent.** Git uses this mechanism for short commit hashes. Beads uses it for issue IDs (confirmed in `/Users/wesm/code/beads/internal/storage/issueops/helpers.go:122-162`). The model is familiar.

### 4.3 What happens across unsynced daemons

The auto-extend algorithm in §5 computes uniqueness against the issues a daemon **currently holds**. Two daemons that have not yet synced cannot constrain each other's choices:

- Daemon A creates issue X with ULID ending `...XABC4`. No collision in A's view; A stores `short_id = "abc4"` at length 4.
- Daemon B creates issue Y with ULID ending `...YABC4`. No collision in B's view; B stores `short_id = "abc4"` at length 4.
- A and B sync. Both now hold X and Y. The unique index `(project_id, short_id)` is violated.

The merge operation must reconcile by extending one (or both) issues to a longer length. Whichever issue is extended now has a different display reference than it had on its origin daemon — references already written there (in commit messages, comments, external tools) become stale. The collision rate at length 4 makes this rare (~1/1M per pair) but not impossible; at scale, expect roughly N²/(2·1M) collision pairs across N issues in a project.

**Stale references on extension are accepted as a tolerable trade-off.** A future federation spec may layer mitigations on top — for example, an issue-alias table that records old short_ids and resolves them with a "this reference moved" warning — but that mechanism, the merge policy itself, and any other federation/sync semantics are out of scope here.

## 5. Auto-extend algorithm

At creation:

```
L = 4
loop:
  candidate = lowercase(ulid[26-L : 26])
  if no other issue in this project has short_id = candidate:
    store (short_id = candidate, length = L); return
  L = L + 1
```

The collision check considers all issues in the project that still occupy a row in the `issues` table — both live and soft-deleted. Soft-deleted issues retain their short_id so a `kata restore` always returns the issue under the same name it had before deletion. Purged issues (which are irreversibly removed from the table) free their short_ids; a new issue could in principle be assigned a short_id previously held by a purged one, but this is rare at the 32⁴ namespace and consistent with purge's "explicit destructive" framing in the existing kata model.

### 5.1 Collision math

With Crockford base32 (32 chars) and a 4-char minimum:

- Namespace at length 4: 32⁴ ≈ 1.05M.
- Project with 1,000 live issues: ~0.5 expected collision pairs (i.e. roughly *one* issue extended to length 5; everything else stays at 4).
- Project with 10,000 live issues: ~50 expected collision pairs (some issues at 5 chars, very few at 6+).

In practice, almost all issues are 4 chars. Length 5 is the rare extension. Length 6+ is a footnote.

### 5.2 Stability

Within a single daemon, an issue's short_id is set once at creation and never changes for any reason that occurs in normal operation. New issues can only extend *their own* length to avoid colliding with existing ones; existing issues are never bumped by new arrivals.

Three operator-driven situations *can* shift an issue's short_id, all explicit and infrequent:

1. **The initial JSONL cutover** introducing this feature derives every issue's short_id from the ULID in ULID-ascending order. There are no prior short_ids to preserve, so this is a one-time derivation event.
2. **`kata projects merge`** (§9.4). Source-side issues whose short_ids collide with target-side issues are extended; existing target short_ids are not bumped. The merge response reports each shifted source-side short_id with its pre- and post-merge values.
3. **Federation merge** (§4.3). The cross-daemon analog of project merge; covered by a future federation spec.

**Future cutovers preserve stored short_ids.** Any subsequent JSONL cutover (for an unrelated schema change) reads `short_id` from each issue record and persists it as-is. Auto-extend re-derivation runs only for records that lack a stored `short_id` — i.e., when importing legacy backups created before this feature shipped. This rule is what keeps post-merge short_ids stable across future schema migrations: a project that has been through `kata projects merge` and then through a later cutover will not see the merge's shifted IDs revert to a fresh ULID-ascending derivation.

In the steady state — single-daemon use without one of the three events above — short_ids are immutable.

## 6. Lookup and resolution

The CLI accepts these forms when given an issue reference:

| Input | Interpretation |
|---|---|
| `kata#abc4` | Qualified: project named `kata`, short_id `abc4`. Exact match. |
| `abc4` (in workspace context) | Bare: project from `.kata.toml`, short_id `abc4`. Exact match. |
| `01HZNQ7VFPK1XGD8R5MABCD4EX` (26 chars, valid ULID) | Direct ULID lookup. Already supported; behavior unchanged. |

**Exact match only.** Typing `abc4` does not match an issue stored as `xabc4`, even though `abc4` is a suffix of `xabc4`. Each issue has one canonical display string; lookup is keyed on it. (Compare to git, which prefix-matches and reports ambiguity; we trade that flexibility for stable references — kata's whole motivation here is that references must not become ambiguous after creation.)

**Legacy `#N` does not resolve.** After cutover, `kata#12` and bare `12` produce a "not found" error. Old `#N` strings inside comment bodies remain plain text.

**Soft-deleted issues are excluded by default; specific paths bypass the filter.** Normal read and mutate paths (`kata show`, `kata edit`, `kata comment`, `kata close`, `kata label`, etc.) add `AND deleted_at IS NULL` to their lookup query, so a soft-deleted issue resolves as "not found" through these. The following paths intentionally include soft-deleted rows:

- **`kata restore <ref>`** must resolve a soft-deleted issue by short_id to undelete it.
- **`kata delete <ref>`** is idempotent: re-running on an already soft-deleted issue is a successful no-op, which requires resolving the soft-deleted row.
- **`kata purge <ref>`** prompts the operator with the issue's metadata before irreversibly removing it; resolution must include soft-deleted rows.
- **Idempotency-key collision detection** on `kata create` must check whether a prior soft-deleted issue used the same key, to return the right mismatch envelope.

The unique index in §7 covers both code paths; the filtering happens in the query, not the index.

## 7. Schema changes

Single column added to `issues`:

```sql
ALTER TABLE issues
  ADD COLUMN short_id TEXT NOT NULL DEFAULT '';
```

The `DEFAULT ''` is a SQLite ergonomics concession for `ADD COLUMN`; the JSONL cutover replays into a fresh schema where `short_id` is `NOT NULL` with no default and is populated row-by-row during replay. No row in a settled database is ever observed with `short_id = ''`.

CHECK constraints (in the fresh schema):

```sql
CHECK (length(short_id) BETWEEN 4 AND 26),
CHECK (short_id NOT GLOB '*[^0-9abcdefghjkmnpqrstvwxyz]*'),
CHECK (short_id = lower(substr(uid, 27 - length(short_id), length(short_id))))
```

The third CHECK enforces the derivation invariant: `short_id` must be the lowercased suffix of `uid` at its stored length. Combined with the existing immutable-`uid` trigger, the short_id is anchored to its ULID and cannot drift.

Unique index:

```sql
CREATE UNIQUE INDEX uniq_issues_project_short_id
  ON issues(project_id, short_id);
```

The index is unfiltered — it covers soft-deleted issues so their short_ids stay reserved across delete/restore. Lookup queries add `AND deleted_at IS NULL` in their `WHERE` clause; the index still serves them efficiently.

CHECK on `projects.name`:

```sql
CHECK (name NOT GLOB '*#*')
```

The `number` column on `issues` and the `next_issue_number` column on `projects` are **dropped** at cutover. This is the wholesale replacement.

## 8. JSONL cutover

The cutover follows the existing pattern in `internal/jsonl/cutover.go`. Schema version bumps from the current `v7` (per `db.CurrentSchemaVersion()` and `internal/db/db_test.go`'s `assert.Equal(t, 7, db.CurrentSchemaVersion())`) to `v8`. (The branch may have advanced further by the time implementation lands; in that case the spec's "v8" is shorthand for "the next version after current.")

1. **Validate prerequisites.** Reject if any project name contains `#`. The error message names the offending project(s) and instructs the operator to rename them before retrying.
2. **Export** the current SQLite database to JSONL at the current version.
3. **Rebuild** at the new version:
   - Apply the new schema (which has `short_id` and lacks `number` / `next_issue_number`).
   - Replay JSONL grouped by project, with each project's issues processed in **ULID-ascending order**.
   - For each issue, run the §5 algorithm against the issues already replayed in the same project. Persist the resulting `short_id` on the row.
   - All other state (links, comments, labels, events, import mappings, purge_log) replays with the schema-level changes in §9.1 applied (number-bearing columns dropped). The `number` field on issue records in the JSONL stream is read for older inputs and discarded after the issue is replayed; new issue records carry `short_id` instead.
4. **Verify** the rebuilt database round-trips: re-export to JSONL, diff against the cutover input modulo the schema diff. This is the existing pattern from `cutover_test.go`.

The JSONL `issue` envelope at the new version carries `short_id` and drops `number`. Older JSONL files (e.g. archived backups) remain importable through the cutover path; new exports always emit the current version.

### 8.1 Future cutovers preserve stored short_ids

This spec defines the *initial* derivation. Any later cutover — for an unrelated schema change — must persist the stored `short_id` from each JSONL issue record verbatim. Concretely:

- If the JSONL issue record has a `short_id` field, the rebuild persists it as-is and skips the auto-extend algorithm for that issue.
- If the JSONL issue record lacks `short_id` (e.g., a v7 export from before this feature shipped), the rebuild runs the §5 algorithm to derive one, in ULID-ascending order within the project.

Without this rule, a database that has been through `kata projects merge` (§9.4) — which may have extended some source-side short_ids to break collisions with the target's existing short_ids — would have those extensions silently reverted by the next cutover, since a fresh ULID-ascending derivation across the merged set would pick different winners. Preserving stored values is what makes the post-merge state durable across future schema changes.

The verify step in (4) above already catches this: a round-trip on a merged database produces byte-identical short_ids only if the rebuild preserves them.

## 9. CLI, API, and schema surface

The number-bearing surface is wide. This section enumerates the categories with their decisions and names the prominent concrete instances; the implementation plan is responsible for sweeping the codebase and applying the per-category rule to every site (a `grep` for `number` and `Number` inside `cmd/kata/`, `internal/api/`, `internal/daemon/`, and `internal/tui/` returns dozens of hits across CLI structs, API request/response types, and TUI client shapes — all are in scope). The hard cut applies uniformly at cutover, matching the precedent in `2026-05-07-kata-relationship-flags.md` §2.7.

### 9.1 Schema columns dropped

- `issues.number` — **dropped**.
- `projects.next_issue_number` — **dropped** (no counter).
- `events.issue_number` — **dropped**. The events table retains `issue_uid` and `related_issue_uid`, which remain the canonical references for historical events. Display callers render `short_id` at the API/SSE boundary by joining against `issues.short_id`; if a cutover or federation merge later shifts the short_id, old events render correctly via the join.
- `purge_log.issue_number` — **dropped**. `purge_log.issue_uid` remains as the audit reference.

`events.related_issue_number` does **not** exist in the current schema (it has `related_issue_id` and `related_issue_uid` only, per `internal/db/schema.sql`); no removal there. If the implementation discovers any `*_number` column not enumerated above, the cutover's schema-completeness test (`internal/db/schema_completeness_test.go`) flags it and the spec is amended.

### 9.2 Wire/payload field changes

REST, SSE, and event payloads change in lockstep with the schema. Rule for every site that currently emits an integer issue number:

- **References to issues** (link arrays in create/edit requests and responses, idempotency mismatches, event payloads, hooks, API responses on every issue-bearing endpoint): replace `number` / `issue_number` / `to_number` / `from_number` / `parent_number` with the corresponding `short_id` / `issue_short_id` / `to_short_id` / `from_short_id` / `parent_short_id`, and (where convenient for consumers) bundle UID + short_id together as `{"uid": "01H...", "short_id": "abc4"}` in nested objects (link records, idempotency mismatch responses).
- **Wire input** on `kata create` / `kata edit` link flags and any other ref-accepting input accepts a string (`abc4`, `kata#abc4`, or a 26-char ULID); the daemon resolves it to both forms in the response.
- **Stored event payloads.** UIDs (`issue_uid`, `to_uid`, etc.) are canonical; short_ids in stored payloads are display snapshots that may be stale across a cutover or federation merge. Consumers needing stable peer identity read UIDs.

Concrete CLI/API outputs to update (non-exhaustive, but specifically named in review):

- `kata create` JSON response: emit `qualified_id`, `short_id`, `uid`; drop `number`.
- `kata show`, `kata list`, `kata search` JSON: emit `short_id`, `qualified_id`, `uid` per issue; drop `number` and `parent_number`. Link records carry `from`/`to` objects.
- `kata events --json` (per `cmd/kata/events.go`): emit `issue_short_id`, `issue_uid`, drop `issue_number`. Same rule for any other ref fields in the event envelope.
- `kata digest --json` (per `cmd/kata/digest.go`): emit `issue_short_id`, `issue_uid`, drop `issue_number`.
- `kata ready --json` (per `cmd/kata/ready.go`): emit `short_id`, `qualified_id`, `uid` per row; drop `number`.
- `kata projects --json` and the project response shape `internal/api/types.go` `Project`: drop `next_issue_number` field. Project records emit `name`, `uid`, timestamps, and the new field set; **no counter is reported anywhere**.

### 9.3 REST URL paths and request types

Issue-scoped paths change from `/issues/{number}` to `/issues/{ref}` (the path parameter spells `ref` to make the type-flexibility explicit):

- `GET /api/v1/projects/{pid}/issues/{ref}`
- `PATCH /api/v1/projects/{pid}/issues/{ref}`
- `POST /api/v1/projects/{pid}/issues/{ref}/comments`
- `POST /api/v1/projects/{pid}/issues/{ref}/links`
- `POST /api/v1/projects/{pid}/issues/{ref}/actions/{action}`
- (and every other issue-scoped path defined in `internal/api/types.go` — the `*Request` types currently carry `Number int64 \`path:"number" required:"true"\`` and become `Ref string \`path:"ref" required:"true"\`` instead)

`{ref}` accepts a 4-to-26-char Crockford-base32 short_id, optionally with `#`-style qualification (`kata#abc4`, URL-encoded as needed), or a 26-char ULID. The daemon's path resolver picks the appropriate column at query time. The corresponding huma OpenAPI definitions are updated to reflect the path-component rename.

### 9.4 Project-merge endpoint

`POST /api/v1/projects/{pid}/merge` (`projectsMergeCmd` in `cmd/kata/projects.go`, `mergeProject` handler in `internal/daemon/handlers_projects.go`) currently fails with the error code `project_merge_issue_number_collision` when source and target share any issue numbers, because the schema enforces `UNIQUE(project_id, number)` and merge would violate it.

After cutover:

- The collision basis is `(project_id, short_id)`, not `(project_id, number)`.
- The merge handler runs the §5 auto-extend algorithm on each source-side issue when grafting it onto the target, in ULID order, extending any source issue whose short_id collides with an already-present target short_id. Existing target short_ids are not bumped (matching the §5.2 stability rule and the §4.3 federation-merge framing).
- The error code `project_merge_issue_number_collision` is **removed**. `db.ProjectMergeCollisionError`, `ErrProjectMergeIssueNumberCollision`, and the corresponding `api.NewError(409, ...)` mapping all go away. Tests that asserted the old behavior are rewritten to assert that the merge succeeds and that the source-side issues report the correct (possibly extended) post-merge short_ids in the response.
- The merge response body includes a per-issue list of `{uid, pre_merge_short_id, post_merge_short_id}` so an operator can see which issues' display IDs shifted. This is a small new field, parallel to `purge_logs_moved` and the other counts the merge already returns.

`db.ProjectMergeImportMappingCollisionError` (about import mappings, not numbers) is unaffected.

### 9.5 CLI commands

- `kata show <ref>`, `kata edit <ref>`, `kata close <ref>`, `kata reopen <ref>`, `kata comment <ref>`, `kata label <ref>`, `kata delete <ref>`, `kata restore <ref>`, `kata purge <ref>`, `kata assign <ref>`: accept `abc4` (bare, in workspace), `kata#abc4` (qualified), or a 26-char ULID. `12` and `kata#12` no longer resolve.
- `kata create`, `kata list`, `kata search`, `kata ready`, `kata events`, `kata digest`: emit `short_id`/`qualified_id`/`uid`; drop `number`.
- **`kata projects reset-counter` is removed** along with its REST endpoint `/api/v1/projects/{pid}/reset-counter` and the `api.ResetCounterRequest` / `api.ResetCounterResponse` types in `internal/api/types.go`. With `next_issue_number` gone there is no counter to reset. CLI registration in `cmd/kata/projects.go` (`projectsResetCounterCmd`), the handler in `internal/daemon/handlers_projects.go`, the `--to` flag, the human-readable output line, and all tests covering the command are deleted.
- `kata projects merge` keeps working but with the new collision behavior described in §9.4. The post-merge stdout line that currently reports `next #%d` has the `next #N` clause removed (no counter).
- Destructive-confirmation prompts (`kata purge`, project archival/removal) display the qualified short_id (`kata#abc4`), not `#12`.

### 9.6 TUI

- The `#N` column in list views becomes the short_id column.
- Detail-pane headers render `kata#abc4`.
- All client types in `internal/tui/client_types.go` swap `Number int64` / `*int64` and `ParentNumber *int64` fields for `ShortID string` and `ParentShortID *string` (plus `Qualified string` where appropriate); `*Uid string` fields remain. SSE event parsing in `internal/tui/events_sse_parse.go` switches `IssueNumber *int64` and the `to_number` / `from_number` envelopes to their short_id equivalents. The TUI's UID-aware peer matching machinery keeps working unchanged because UIDs are the keys.

### 9.7 Hooks

Hook payloads follow the same shape as event payloads (§9.2): UIDs are canonical, short_ids denormalized for display. Subscribers reading `issue_number` must migrate to `issue_short_id` (or `issue_uid` for stable references) at the same release.

### 9.8 Non-breaking

- `uid` in JSON output is unchanged everywhere.
- Link records reference issues by UID in the database (existing schema). Display rendering converts UIDs to short IDs at the API/TUI boundary.
- Idempotency keys, search, label semantics, priority, and ownership are unaffected.

## 10. Beads importer interaction

The Beads importer in `internal/db/imports.go` maps `bd-<hash>` source IDs to fresh kata ULIDs (existing behavior). Under this design:

- The kata short ID for an imported issue is derived from the **kata ULID**, not from the Beads hash.
- The Beads source ID continues to live in `import_mappings` as the record of origin and remains queryable, but is not used for display.
- An imported Beads issue `bd-a3f8` becomes (e.g.) `kata#x9km` in kata. The original Beads hash is recoverable via the import mapping; the kata short ID is the canonical reference going forward.

This is consistent with the existing importer's treatment of Beads-origin metadata as opaque audit data rather than user-facing identity.

## 11. Testing

- **Auto-extend algorithm.** Unit tests: 4-char baseline, single collision forces 5, double collision forces 6, terminal-loop sanity (no infinite loop because at L=26 the suffix is the full ULID and unique by construction).
- **Schema invariants.** Inserting a duplicate `(project_id, short_id)` pair fails the unique index. Inserting a `short_id` that is not the lowercased suffix of its `uid` at the stored length fails the CHECK. Updating `short_id` on an existing row succeeds only when the new value still satisfies the CHECK and uniqueness — and the cutover is the only path that does this in normal operation.
- **JSONL roundtrip.** Export → cutover → re-export produces byte-identical JSONL modulo the schema-diff between source and target versions.
- **Cutover with collisions.** A pre-cutover fixture database with deliberately collision-inducing ULIDs (one project containing several issues whose ULIDs share a 4-char suffix) lands at the correct mix of length-4 and length-5 short_ids after cutover, in the order ULID-ascending replay produces.
- **Cutover refusal.** A fixture with a project name containing `#` fails the cutover with the documented error message naming the offending project.
- **Soft-delete carveouts.** `kata show` and other normal paths return "not found" for a soft-deleted issue's short_id; `kata restore`, `kata delete` (idempotent re-call), `kata purge`, and idempotency-key collision detection all resolve the soft-deleted row.
- **CLI lookup.** `kata show abc4`, `kata show kata#abc4`, and `kata show 01HZ...` (matching ULID) resolve to the same issue. `kata show 12` and `kata show kata#12` return "not found." `kata show xabc4` does not resolve to an issue stored as `abc4` (no prefix expansion).
- **Wire-format renames.** `kata create --json` includes `short_id`, `qualified_id`, and `uid` and excludes `number`. `kata events --json`, `kata digest --json`, `kata ready --json`, and `kata projects --json` outputs are checked against fixtures asserting the new field set and the absence of `*_number` / `next_issue_number`. Idempotency mismatch responses include `{uid, short_id, qualified_id}`. Event payloads emitted on the SSE stream include `*_short_id` and `*_uid` fields and exclude `*_number` fields.
- **Order-dependence is documented, not hidden.** A unit test creates two issues with deliberately colliding 4-char ULID suffixes, asserts their short_ids in ULID-ascending creation order, then runs the same fixture through cutover replay and asserts the same result. A second unit test imports them in reverse order through a simulated merge path and asserts the documented behavior (existing local short_id stays; imported issue extends).
- **`kata reset-counter` is gone.** Cobra registration tests assert the command is absent. Daemon endpoint tests assert `POST /api/v1/projects/{pid}/reset-counter` returns 404. The `api.ResetCounterRequest` / `api.ResetCounterResponse` types are absent from the OpenAPI spec. TUI snapshot tests assert no UI surface offers the affordance.
- **`kata projects merge` collision behavior.** A fixture with two projects, each holding an issue whose ULIDs share a 4-char suffix, merges successfully after cutover. The response identifies the issue whose short_id was extended (`pre_merge_short_id` ≠ `post_merge_short_id`). The old `project_merge_issue_number_collision` 409 path is asserted absent.
- **Future cutovers preserve stored short_ids (§8.1).** A fixture database is run through the initial cutover, then through `kata projects merge` to deliberately extend one source short_id, then through a simulated subsequent cutover (with a no-op schema bump). Every issue's short_id post-second-cutover equals its short_id pre-second-cutover; the merge-shifted short_id is not silently reverted.
- **TUI snapshots.** List-view, detail-view, and event-pane snapshots update to assert `kata#abc4` rendering and the absence of `#N`.

## 12. Decisions

- **Replace, don't add.** Short IDs replace `#N` as the canonical display. The transition is a hard cut at JSONL cutover; legacy `#N` input is not accepted.
- **Derive from ULID, don't generate.** No new identity to maintain. Within a daemon, the short_id is a deterministic function of the ULID and the issues already present; across unsynced daemons, collisions are rare-but-possible (§4.3) and resolved by the future federation spec.
- **Auto-extend, don't suffix.** Variable-length suffix of the ULID (4 to 26 chars), not a fixed `abc4-N` counter. Display widths in practice: almost all 4, occasionally 5, rarely 6+.
- **Crockford base32 (lowercased), not hex or base36.** Matches the ULID alphabet; reuses its readability properties. Diverges from Beads' base36 by one alphabet character; not enough to matter at the namespace sizes in use.
- **Project name in qualified form, separated by `#`.** `kata#abc4`, not `kata/abc4` or `kata-abc4`. Preserves the existing sigil and avoids project-name-with-dash parsing ambiguity. Project names cannot contain `#`.
- **Skip sub-issue dot-notation.** Beads' implementation has IDs that lie about structure after reparenting; the cost of that for kata's parent-link model isn't worth the visual benefit. Hierarchy is rendered, not encoded.
