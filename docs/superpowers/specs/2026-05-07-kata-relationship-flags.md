# kata — Relationship Flags on Create + Edit

**Status:** Design
**Date:** 2026-05-07
**Topic:** Consolidate the eight dedicated relationship-editing commands into matched flag pairs on `kata create` and `kata edit`.
**Tracks:** kata#1

## 1. Motivation

The current relationship surface has three problems for agents:

1. **Eight commands for one concept.** `link`, `unlink`, `parent`, `unparent`, `block`, `unblock`, `relate`, `unrelate` all edit the same three link types. Agents must memorize argument orders that vary between commands.
2. **Direction confusion is frequent.** `kata block <blocker> <blocked>` and `kata parent <child> <parent>` put the relationship's subject in different argument positions. A misordered call corrupts the link graph silently.
3. **Asymmetric and incomplete flags on `create`.** `--parent` (singular) and `--blocks` (repeatable) are present; `--blocked-by` and `--related` are not. `kata edit` has zero link flags, so an agent who created an issue and then needs to add a "blocked-by" must issue a separate `kata block <other> <new>` call — two RPCs, two events, partial-state risk if the second fails.

The wire protocol already accepts a `links: [{type, to_number}]` array on `POST /api/v1/projects/{pid}/issues` (used by `kata create` today). Consolidation is therefore a CLI-surface change, with daemon-side work limited to extending the issue-PATCH endpoint to accept the same delta shape.

## 2. Design

### 2.1 Flag table

Add flags work on **both** `kata create` and `kata edit`. Remove flags work only on `kata edit`.

| Flag | Add or Remove | Repeatable | Notes |
|---|---|---|---|
| `--parent N` | add | no | Replaces any existing parent on `edit`. |
| `--blocks N` | add | yes | This issue blocks N. |
| `--blocked-by N` | add | yes | This issue is blocked by N. |
| `--related N` | add | yes | This issue is related to N. |
| `--remove-parent N` | remove | no | Strict: fails if current parent ≠ N. |
| `--remove-blocks N` | remove | yes | Idempotent no-op if link absent. |
| `--remove-blocked-by N` | remove | yes | Idempotent no-op if link absent. |
| `--remove-related N` | remove | yes | Idempotent no-op if link absent. |

### 2.2 Naming rationale

Every flag's subject is the issue under the verb — the issue being created or edited. `kata edit 42 --blocked-by 15` reads as "issue 42 is blocked by 15." There is no argument-order ambiguity to remember.

The four link types in the codebase are `parent`, `blocks`, `related`. The CLI exposes the parent slot from one direction only (the child's perspective); to make #5 a sub-issue of #1, an agent runs `kata edit 5 --parent 1` rather than the inverse. This drops a flag pair and matches how agents naturally think about parentage. The capability is preserved from the other side of the edge.

`blocks`/`blocked-by` are first-class because either end is a natural perspective from which to file. `related` is symmetric.

### 2.3 Strictness and idempotency

The eight flags split into two semantic classes:

- **Strict (parent only):** `--remove-parent N` requires N to equal the current parent. If the issue has no parent, or has a different parent, the call fails with a validation error and no other mutations in the same `edit` apply. This is an optimistic-concurrency check against agents acting on stale state.
- **Idempotent (everything else):** `--remove-blocks N` on an issue that does not block N is a successful no-op. `--related 7` on an issue already related to 7 is a successful no-op. Matches the existing idempotent flavor of label add/remove.

The asymmetry is intentional. Multi-valued removes naturally express "ensure this link is gone"; the parent slot is unique enough that an unexpected current-parent value is more likely a stale-state bug than a benign duplicate request.

### 2.4 Conflicts

The following are validation errors detected client-side before any daemon call:

- `--parent N` and `--remove-parent M` in the same call (any N, M).
- `--blocks N` and `--remove-blocks N` in the same call (same N). Same for `blocked-by` and `related`.
- Self-links: any link flag whose target equals the issue under edit.
- Duplicate `--parent N --parent M` with N ≠ M (cobra default last-wins, but we reject explicitly to surface the typo).
- Multiple identical add flags for the same multi-valued link (e.g. `--blocks 50 --blocks 50`) collapse to one and are not an error.

### 2.5 Atomicity

A single `kata edit` call applies all link mutations *and* all field changes (title, body, owner, priority) in one daemon transaction. Either the entire delta succeeds or none of it applies.

Today, `cmd/kata/edit.go` makes two HTTP calls when both PATCH-able fields and `--priority` are passed: PATCH for title/body/owner, then POST to the priority action endpoint. This spec folds priority into PATCH so the entire edit is one call and one transaction. The standalone priority action endpoint remains in service for the TUI but is not used by the CLI after this change.

The daemon emits up to three events per `kata edit` call, all post-commit, all in this order:

1. `issue.updated` if any of title / body / owner actually changed.
2. `issue.priority_set` or `issue.priority_cleared` if priority actually changed. Priority keeps its own typed event (matching the standalone priority action endpoint) so the payload can carry both the old and new values — information `issue.updated` doesn't preserve. Subscribers that key off priority transitions get the same shape regardless of whether the change came in via `kata edit` or the legacy action endpoint.
3. `issue.links_changed` if any link op actually changed, with a payload listing every applied add and remove.

A single edit that touches all three classes therefore produces three events. Subscribers that previously consumed per-link `issue.link_added` / `issue.link_removed` events must migrate to the aggregated `issue.links_changed` form (kata is in early preview; no deprecation window).

Cycle detection (e.g. parenting #1 under one of its descendants) is inherited from the existing link-creation path. The daemon validates the resulting graph after the proposed delta is applied and rejects the entire edit if a cycle would be introduced.

### 2.6 Output

`kata edit --json` returns the issue plus a `changes` block describing what actually changed. Empty arrays/keys are omitted. If nothing changed (all link mutations were idempotent no-ops and no other field flags were passed), `changes` is `{}`, exit code is 0, and an `issue.updated` event is **not** emitted.

```json
{
  "kata_api_version": 1,
  "issue": { "...full state..." },
  "changes": {
    "parent_set": 10,
    "parent_removed": 7,
    "blocks_added": [50, 51],
    "blocks_removed": [],
    "blocked_by_added": [15],
    "related_removed": [7]
  }
}
```

`parent_set` and `parent_removed` are independent fields, both omitted when not used. A pure set populates `parent_set`. A pure remove (via `--remove-parent N`) populates `parent_removed` with the asserted current parent's number. A replace populates both — `parent_set` is the new parent, `parent_removed` is the old parent — so consumers can render the transition without having to consult prior state. The "no change" case omits both. This carries strictly more information than the alternative `parent_set: null` shape, which would lose the prior parent's number on a replace and is indistinguishable from "field omitted" on the remove path.

The aggregated `issue.links_changed` event payload mirrors `changes` field-for-field and additionally carries parallel UID slices (`parent_set_uid`, `blocks_added_uids`, etc.) for stable peer identity. UIDs are required for downstream consumers (the TUI's detail-pane refetch logic, JSONL export tooling) to identify referenced peers safely after a project's number sequence has been reset (numbers can collide; UIDs cannot). The wire `changes` block keeps only the numeric forms — agents read peer numbers, not internal UIDs.

**Purge / soft-delete semantics on linked peers.** When an issue is purged or soft-deleted, the FK cascade still removes events whose `issue_id` or `related_issue_id` refers to that issue (this is the long-standing per-link `issue.linked` / `issue.unlinked` behavior, plus iteration-16's single-peer envelope reference for aggregated events). Events on OTHER live issues that merely mention the purged/soft-deleted issue in their payload — `issue.created` initial links and multi-peer `issue.links_changed` events — are intentionally PRESERVED. Erasing the historical context that another issue was once linked to the now-removed peer is a worse outcome than leaving an orphan reference in the surviving issue's payload. Live-only JSONL exports therefore include those payload references intact.

`kata create --json` retains its existing shape. Link flags on create are reflected in the returned issue; no separate `changes` block is added there because the issue is new.

### 2.7 Hard cut of old commands

The eight commands are removed without deprecation aliases:

- `kata link`, `kata unlink`
- `kata parent`, `kata unparent`
- `kata block`, `kata unblock`
- `kata relate`, `kata unrelate`

`cmd/kata/link.go` is deleted along with its tests. The README, CLAUDE.md, AGENTS.md, and `kata quickstart` text are updated in the same change. kata's "early public preview" status (README §status) is the basis for the hard cut; no users have stable contracts on these commands.

## 3. Daemon API changes

The existing `POST /api/v1/projects/{pid}/issues` already accepts a `links` array. The PATCH endpoint at `PATCH /api/v1/projects/{pid}/issues/{n}` is extended to accept an optional `links_delta` field:

```json
{
  "title": "...",
  "links_delta": {
    "set_parent": 10,
    "remove_parent": null,
    "add": [{"type": "blocks", "to_number": 50}, {"type": "blocked_by", "to_number": 15}],
    "remove": [{"type": "related", "to_number": 7}]
  }
}
```

Exactly one of `set_parent` and `remove_parent` may be present. `remove_parent` is the integer the caller asserts is the current parent (matches the `--remove-parent N` strict semantics). The daemon validates: parent assertion match (if `remove_parent` given), no conflicts within the delta, no self-links, no resulting cycles, then applies the delta in one transaction.

Existing dedicated link endpoints (`POST /api/v1/projects/{pid}/issues/{n}/links`, etc.) remain in service for the TUI and for any internal callers, but are not exposed by the CLI after this change. They become candidates for removal in a follow-up.

## 4. Out of scope

- **Owner and label flags on `edit`.** Already partially supported via `--owner`; label add/remove and assign/unassign remain on their own commands. Folding them in is a candidate for a separate spec.
- **MCP server tools.** The CLI surface change is the prerequisite; MCP shape will follow once the CLI is settled.
- **TUI changes.** The TUI's relationship-editing widgets continue to call the existing dedicated link endpoints. A later TUI pass may consolidate them.

## 5. Migration impact

- All scripts and agent skills that call `kata block/unblock/parent/unparent/relate/unrelate/link/unlink` break at the next release. The CLAUDE.md and quickstart updates carry agents to the new surface.
- The example session in `kata quickstart` is rewritten to use only `create` and `edit` for relationship work.
- `roborev` and any other tools that shell out to kata's CLI need to be checked for old-command usage. The plan's audit step covers this.

## 6. Decisions

- **`--remove-parent` always requires the asserted current parent number.** No empty-value escape hatch. Agents read the current parent first and pass it; the safety check is unconditional.
- **One aggregated `issue.links_changed` event per edit** that mutates any links. Cleaner for tail-watchers and matches the "one edit, one logical change" mental model. Subscribers consuming the per-link events from the old API must migrate.
- **Priority folds into PATCH.** The CLI no longer makes a second HTTP call for `--priority`; one `kata edit` is one daemon transaction across all PATCH-able fields and link deltas.
