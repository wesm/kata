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
- Minimal schema impact: one column on `issues`, one CHECK on `projects.name`.
- Single-pass cutover via the existing JSONL roundtrip in `internal/jsonl/cutover.go`.

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

- **One mechanism.** The display ID is always "enough chars of the ULID to be unique in this project." There is no synthetic counter, no per-daemon arrival order to track, no `-1`/`-2` suffix to render.
- **Globally derivable given a shared issue set.** Any daemon that holds the same set of issues for a project derives the same minimum length and the same display string for each issue — no per-daemon arrival order enters the calculation. (Across daemons that have *not* synced, two daemons may independently pick `length=4` for issues whose ULIDs share a 4-char suffix; that is the federation collision case in §4.3.)
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

Two operator-driven situations *can* shift an issue's short_id, both explicit and infrequent:

1. **JSONL cutover** that rebuilds the database (e.g., the cutover introducing this feature). Cutovers re-derive every issue's length in ULID-ascending order — the same order in which the original daemon assigned them — so for a database that has not been federated, the post-cutover short_ids match what the daemon originally chose.
2. **Federation merge** (§4.3). When a remote daemon's issues are imported and a short_id collision is detected, the merge extends one of the colliding issues. References to the extended issue written on its origin daemon become stale.

In the steady state — single-daemon use without cutover or merge — short_ids are immutable.

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
   - Replay JSONL grouped by project, with each project's issues processed in **ULID-ascending order**. ULIDs are time-ordered, so this matches original creation order.
   - For each issue, run the §5 algorithm against the issues already replayed in the same project. Persist the resulting `short_id` on the row.
   - All other state (links, comments, labels, events, import mappings, purge_log) replays with the schema-level changes in §9.1 applied (number-bearing columns dropped or renamed). The `number` field on issue records in the JSONL stream is read for older inputs and discarded after the issue is replayed; new issue records carry `short_id` instead.
4. **Verify** the rebuilt database round-trips: re-export to JSONL, diff against the cutover input modulo the schema diff. This is the existing pattern from `cutover_test.go`.

The JSONL `issue` envelope at the new version carries `short_id` and drops `number`. Older JSONL files (e.g. archived backups) remain importable through the cutover path; new exports always emit the current version.

## 9. CLI, API, and schema surface

The number-bearing surface is wider than `issues.number` alone. Every place that currently exposes or stores a project-scoped issue number is enumerated here with an explicit decision. The hard cut applies uniformly at cutover, matching the precedent in `2026-05-07-kata-relationship-flags.md` §2.7.

### 9.1 Schema columns dropped or renamed

- `issues.number` — **dropped**.
- `projects.next_issue_number` — **dropped** (no counter).
- `events.issue_number`, `events.related_issue_number` — **dropped**. The events table already carries `issue_uid` (and `related_issue_uid` where applicable); display callers render the short_id at the API/SSE boundary by joining against `issues.short_id`. Historical events therefore reflect the *current* short_id of the referenced issue, which is what consumers want; if the cutover or a federation merge later shifts the short_id, old events render correctly via the join.
- `purge_log.issue_number` — **dropped**. `purge_log.issue_uid` remains as the audit reference.

(If the codebase has additional `*_number` columns on tables not yet enumerated, the cutover surfaces them in the schema-completeness test and the spec is amended.)

### 9.2 Wire/payload field changes

REST and SSE payloads renamed in lockstep with the schema:

- Link payloads (`POST /api/v1/projects/{p}/issues/{ref}/links`, link arrays in create/edit, link records in API responses): `from_number` and `to_number` are dropped; replaced with `from` and `to` objects of shape `{"uid": "01H...", "short_id": "abc4"}`. Wire input on `kata create`/`kata edit` link flags accepts a string ref (`abc4`, `kata#abc4`, or a 26-char ULID) and the daemon resolves it to both forms in the response.
- Issue references on event payloads (`issue.created`, `issue.commented`, `issue.linked`, `issue.links_changed`, etc.): drop `issue_number`, `to_number`, `from_number`, `parent_number`, `related_issue_number` from the payload. Add `issue_short_id`, `to_short_id`, `from_short_id`, `parent_short_id`, `related_short_id` denormalized at emit time. UID fields (`issue_uid`, `to_uid`, `from_uid`, `parent_uid`, `related_uid`) remain canonical; short_ids in stored event payloads are display snapshots and may be stale across a cutover or federation merge — that is acceptable because UIDs are the source of truth for downstream resolution.
- Idempotency mismatch responses (when `kata create` is called with a key that matches a prior request whose body differs): the response identifies the existing issue by `{"uid": "...", "short_id": "abc4", "qualified_id": "kata#abc4"}`. The previous `number` field is dropped.
- `kata create` / `kata show` / `kata list` JSON: replace `number` with `short_id`. Add `qualified_id` (`"kata#abc4"`). `uid` is unchanged.

### 9.3 REST URL paths

Issue-scoped paths change from `/issues/{number}` to `/issues/{ref}`:

- `GET /api/v1/projects/{pid}/issues/{ref}`
- `PATCH /api/v1/projects/{pid}/issues/{ref}`
- `POST /api/v1/projects/{pid}/issues/{ref}/comments`
- `POST /api/v1/projects/{pid}/issues/{ref}/links`
- `POST /api/v1/projects/{pid}/issues/{ref}/actions/{action}`
- (and any others where the path component is currently `{number}` or `{n}`)

`{ref}` accepts a 4-to-26-char Crockford-base32 short_id, optionally with `#`-style qualification (`kata#abc4` URL-encoded if needed), or a 26-char ULID. The daemon's path resolver picks the appropriate column at query time.

### 9.4 CLI commands

- `kata show <ref>`, `kata edit <ref>`, `kata close <ref>`, `kata reopen <ref>`, `kata comment <ref>`, `kata label <ref>`, `kata delete <ref>`, `kata restore <ref>`, `kata purge <ref>`: accept `abc4` (bare, in workspace), `kata#abc4` (qualified), or a 26-char ULID. `12` and `kata#12` no longer resolve.
- `kata create` returns the new issue's `qualified_id`, `short_id`, and `uid` in JSON. The `number` field is dropped.
- `kata list` / `kata search` JSON output drops `number`, adds `short_id` and `qualified_id`.
- **`kata reset-counter` is removed.** With `next_issue_number` gone there is no counter to reset. The command's CLI registration, daemon endpoint (if any), TUI affordances, and tests are deleted.
- Destructive-confirmation prompts (`kata purge`, project archival/removal) display the qualified short_id (`kata#abc4`), not `#12`.

### 9.5 TUI

- The `#N` column in list views becomes the short_id column.
- Detail-pane headers render `kata#abc4`.
- All client types in `internal/tui/client_types.go` swap `Number int64` / `*int64` fields for `ShortID string` (and where appropriate, `Qualified string`); `*Uid string` fields remain. The TUI's UID-aware peer matching machinery (currently used for `kata reset-counter` survival in `internal/tui/sse_update_test.go`) keeps working unchanged because UIDs are the keys.

### 9.6 Hooks

Hook payloads follow the same shape as event payloads (§9.2): UIDs are canonical, short_ids denormalized for display. Subscribers reading `issue_number` must migrate to `issue_short_id` (or `issue_uid` for stable references) at the same release.

### 9.7 Non-breaking

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
- **Wire-format renames.** `kata create --json` includes `short_id`, `qualified_id`, and `uid` and excludes `number`. Idempotency mismatch responses include `{uid, short_id, qualified_id}`. Event payloads emitted on the SSE stream include `*_short_id` and `*_uid` fields and exclude `*_number` fields.
- **`kata reset-counter` is gone.** Cobra registration tests assert the command is absent. Daemon endpoint tests assert any matching path returns 404. TUI snapshot tests assert no UI surface offers the affordance.
- **TUI snapshots.** List-view, detail-view, and event-pane snapshots update to assert `kata#abc4` rendering and the absence of `#N`.

## 12. Decisions

- **Replace, don't add.** Short IDs replace `#N` as the canonical display. The transition is a hard cut at JSONL cutover; legacy `#N` input is not accepted.
- **Derive from ULID, don't generate.** No new identity to maintain. Within a daemon, the short_id is a deterministic function of the ULID and the issues already present; across unsynced daemons, collisions are rare-but-possible (§4.3) and resolved by the future federation spec.
- **Auto-extend, don't suffix.** Variable-length suffix of the ULID (4 to 26 chars), not a fixed `abc4-N` counter. Display widths in practice: almost all 4, occasionally 5, rarely 6+.
- **Crockford base32 (lowercased), not hex or base36.** Matches the ULID alphabet; reuses its readability properties. Diverges from Beads' base36 by one alphabet character; not enough to matter at the namespace sizes in use.
- **Project name in qualified form, separated by `#`.** `kata#abc4`, not `kata/abc4` or `kata-abc4`. Preserves the existing sigil and avoids project-name-with-dash parsing ambiguity. Project names cannot contain `#`.
- **Skip sub-issue dot-notation.** Beads' implementation has IDs that lie about structure after reparenting; the cost of that for kata's parent-link model isn't worth the visual benefit. Hierarchy is rendered, not encoded.
