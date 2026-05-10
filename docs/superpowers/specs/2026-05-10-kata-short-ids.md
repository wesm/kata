# kata — Short IDs (Federation-Stable Display)

**Status:** Design
**Date:** 2026-05-10
**Topic:** Replace per-project sequential `#N` issue numbers with federation-stable short IDs derived from each issue's ULID, qualified by project name in cross-project references.

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

- A display ID that is stable across daemons without coordination — derivable from data each daemon already holds (the ULID).
- Short enough to type and remember (4-character minimum) and to embed in commit messages without ceremony.
- No new generated identity to migrate, version, or collide on. The short ID is a function of the existing ULID.
- Minimal schema impact: one column on `issues`, one CHECK on `projects.name`.
- Single-pass cutover via the existing JSONL roundtrip in `internal/jsonl/cutover.go`.

**Non-goals**

- **Sub-issue dot-notation** (e.g. `kata#abc4.1.1`). Beads does this; the reparenting story is bad — `bd-a3f8.1.1` either has its ID changed (breaking every existing reference) or stays put and lies about the current parent. We confirmed Beads' behavior at `/Users/wesm/code/beads`: reparenting keeps the ID immutable and rewrites the dependency record, leaving the ID structurally misleading. Hierarchy stays as edges in the `links` table; rendering can be hierarchical when needed (TUI tree view, future `kata tree`).
- **Backwards-compatible `#N` input.** After cutover, `kata show 12` and `kata#12` no longer resolve. The cutover is the single migration moment; kata is in early public preview, so the install base can absorb a clean break. Old commit messages and comment bodies that contain `#N` strings remain readable as text but are no longer clickable references.
- **Replacing the ULID.** ULIDs remain the canonical storage ID, used everywhere FKs, JSONL, hooks, and event ordering touch issues.
- **Federation itself.** This spec is forward-looking groundwork. Federation/sync mechanics live in their own spec. The display change here is valuable on its own (cross-project unambiguous reference within a single daemon, no per-project number-reset hazards) and clears one prerequisite for federation.

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
- **Federation-stable by construction.** Two daemons creating different issues will derive different short IDs because the ULIDs are different by construction. No coordination needed.
- **Single alphabet.** ULIDs are already Crockford base32 (`0-9A-HJKMNP-TV-Z`, no `I/L/O/U`). Reusing the alphabet means the short ID inherits the same readability rules — no new "what about `0` vs `O`" ambiguity to reason about.

### 4.2 Why auto-extend rather than fixed length plus a `-N` suffix

Both designs were considered. Auto-extend wins for three reasons:

- **One mechanism.** The display ID is always "enough chars of the ULID to be unique in this project." There is no synthetic counter, no per-daemon arrival order to track, no `-1`/`-2` suffix to render.
- **Globally derivable.** Any daemon, given the ULIDs of all issues in a project, derives the same minimum length and the same display string for each issue. Daemon-local arrival order does not enter the calculation.
- **Precedent.** Git uses this mechanism for short commit hashes. Beads uses it for issue IDs (confirmed in `/Users/wesm/code/beads/internal/storage/issueops/helpers.go:122-162`). The model is familiar.

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

An issue's short_id is set once at creation and never changes outside an explicit cutover. New issues can only extend their own length to avoid colliding with existing ones; existing issues are never bumped. This guarantees that any reference written today resolves to the same issue forever within the same daemon.

The one situation that *can* shift an issue's short_id is a JSONL cutover that rebuilds the database (e.g., the cutover that introduces this feature). Cutovers are explicit, infrequent, and operator-driven; they re-derive every issue's length in ULID-ascending order, which is the same order in which the original daemon assigned them. So in the steady state, short IDs are immutable.

## 6. Lookup and resolution

The CLI accepts these forms when given an issue reference:

| Input | Interpretation |
|---|---|
| `kata#abc4` | Qualified: project named `kata`, short_id `abc4`. Exact match. |
| `abc4` (in workspace context) | Bare: project from `.kata.toml`, short_id `abc4`. Exact match. |
| `01HZNQ7VFPK1XGD8R5MABCD4EX` (26 chars, valid ULID) | Direct ULID lookup. Already supported; behavior unchanged. |

**Exact match only.** Typing `abc4` does not match an issue stored as `xabc4`, even though `abc4` is a suffix of `xabc4`. Each issue has one canonical display string; lookup is keyed on it. (Compare to git, which prefix-matches and reports ambiguity; we trade that flexibility for stable references — kata's whole motivation here is that references must not become ambiguous after creation.)

**Legacy `#N` does not resolve.** After cutover, `kata#12` and bare `12` produce a "not found" error. Old `#N` strings inside comment bodies remain plain text.

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

The cutover follows the existing pattern in `internal/jsonl/cutover.go`. Schema version bumps from `v4` (the current version per `cutover_v4_test.go`) to `v5`.

1. **Validate prerequisites.** Reject if any project name contains `#`. The error message names the offending project(s) and instructs the operator to rename them before retrying.
2. **Export** the current SQLite database to JSONL at v4.
3. **Rebuild** at v5:
   - Apply the v5 schema (which has `short_id` and lacks `number` / `next_issue_number`).
   - Replay JSONL grouped by project, with each project's issues processed in **ULID-ascending order**. ULIDs are time-ordered, so this matches original creation order.
   - For each issue, run the §5 algorithm against the issues already replayed in the same project. Persist the resulting `short_id` on the row.
   - All other state (links, comments, labels, events, import mappings) replays unchanged. The `number` field on issue records in the JSONL stream is read for v4 inputs and discarded after the issue is replayed; v5 issue records carry `short_id` instead.
4. **Verify** the rebuilt database round-trips: re-export to JSONL, diff against the cutover input modulo the new column. This is the existing pattern from `cutover_test.go`.

The JSONL `issue` envelope at v5 carries `short_id` and drops `number`. v4 JSONL files (e.g. archived backups) remain importable through the cutover path; new exports always emit v5.

## 9. CLI and API impact

**Breaking changes** (acceptable given early-preview status — same precedent as the relationship-flags hard cut in `2026-05-07-kata-relationship-flags.md` §2.7):

- `kata show <id>`, `kata edit <id>`, `kata close <id>`, `kata comment <id>`, etc. accept `abc4` (bare) or `kata#abc4` (qualified). They no longer accept `12` or `kata#12`.
- `kata create` returns the assigned short_id in the JSON response: `{"id": "kata#abc4", "short_id": "abc4", "uid": "01HZ...", ...}`. The `number` field is removed.
- `kata list` JSON output replaces `number` with `short_id`. The `id` field becomes the qualified form.
- TUI rendering replaces the `#N` column with the short ID column. Detail-pane headers show `kata#abc4` instead of `#12`.
- Hook and event payloads carry `short_id` and `qualified_id`. The `number` field is removed from payloads. Subscribers using `number` must migrate at the same release.

**Non-breaking**:

- `uid` in JSON output is unchanged.
- Link records continue to reference issues by `from_issue_uid` / `to_issue_uid` in the database (existing schema). Display rendering converts UIDs to short IDs at the API/TUI boundary.
- Idempotency keys, search, and label semantics are unaffected.

## 10. Beads importer interaction

The Beads importer in `internal/db/imports.go` maps `bd-<hash>` source IDs to fresh kata ULIDs (existing behavior). Under this design:

- The kata short ID for an imported issue is derived from the **kata ULID**, not from the Beads hash.
- The Beads source ID continues to live in `import_mappings` as the record of origin and remains queryable, but is not used for display.
- An imported Beads issue `bd-a3f8` becomes (e.g.) `kata#x9km` in kata. The original Beads hash is recoverable via the import mapping; the kata short ID is the canonical reference going forward.

This is consistent with the existing importer's treatment of Beads-origin metadata as opaque audit data rather than user-facing identity.

## 11. Testing

- Unit tests on the auto-extend algorithm: 4-char baseline, single collision forces 5, double collision forces 6, terminal-loop sanity (no infinite loop possible because at L=26 the suffix is the full ULID, which is unique).
- Schema tests: insertion of a duplicate `(project_id, short_id)` pair on live issues fails the unique index; insertion of a `short_id` that is not the suffix of its ULID fails the CHECK.
- JSONL roundtrip test: export → cutover → re-export produces byte-identical JSONL modulo the v4→v5 envelope difference.
- Cutover test: a v4 fixture database with deliberately collision-inducing ULIDs (one project containing several issues whose ULIDs share a 4-char suffix) lands at the correct mix of length-4 and length-5 short IDs after cutover.
- Cutover refusal test: a v4 fixture with a project name containing `#` fails the cutover with the documented error message.
- CLI tests: `kata show abc4`, `kata show kata#abc4`, and `kata show 01HZ...` (matching ULID) all resolve to the same issue. `kata show 12` returns "not found." `kata show kata#12` returns "not found."
- TUI snapshot tests update to assert `kata#abc4` rendering.

## 12. Decisions

- **Replace, don't add.** Short IDs replace `#N` as the canonical display. The transition is a hard cut at JSONL cutover; legacy `#N` input is not accepted.
- **Derive from ULID, don't generate.** No new identity to maintain; federation-stable by construction.
- **Auto-extend, don't suffix.** Variable-length prefix of the ULID (4 to 26 chars), not a fixed `abc4-N` counter. Display widths in practice: almost all 4, occasionally 5, rarely 6+.
- **Crockford base32 (lowercased), not hex or base36.** Matches the ULID alphabet; reuses its readability properties. Diverges from Beads' base36 by one alphabet character; not enough to matter at the namespace sizes in use.
- **Project name in qualified form, separated by `#`.** `kata#abc4`, not `kata/abc4` or `kata-abc4`. Preserves the existing sigil and avoids project-name-with-dash parsing ambiguity. Project names cannot contain `#`.
- **Skip sub-issue dot-notation.** Beads' implementation has IDs that lie about structure after reparenting; the cost of that for kata's parent-link model isn't worth the visual benefit. Hierarchy is rendered, not encoded.
