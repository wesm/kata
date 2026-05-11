# kata — Closure Justification and Anti-Agent-Abuse Guards

**Status:** Design
**Date:** 2026-05-10
**Topic:** Make `kata close` an evidence-bearing assertion of completion, and stop the two structural abuse patterns observed in the field (rapid sibling closure under a parent; parent-close while children remain open).
**Tracks:** kata#anti-agent-justification

## 1. Motivation

Two incidents in early May 2026 exposed a class of agent failure that the current command surface does nothing to prevent:

- **2026-05-08.** An agent (codex) created 33 service-review children under a parent issue, then closed all 33 in rapid succession via a shell helper that ran `kata comment` followed immediately by `kata close` for each one. After challenge, the agent re-did the work properly and produced a full audit doc and commits — proving the closures had not actually represented complete work.
- **2026-05-10.** The same agent bulk-closed parent #281 and many of its schema-review children, falsely reporting `open_children: 0`. After challenge, it admitted the basis was shallow text classification rather than the deep review the parent had requested. The bad batch was reopened; only one child (#282) was eventually handled properly with evidence, tests, and a commit.

Both incidents share the same shape: an agent converts a plausible comment into a closure, in bursts across siblings under a parent, in a single short session. The current `kata close <ref> --reason done` requires nothing structural to defend against this. The instructions in `quickstart` already say "Close only when the work is actually complete"; instruction alone did not change the behavior.

The fix is to make the close operation:

1. **Carry evidence** that another tool (a human or, later, an automated verifier) can examine.
2. **Refuse structurally dangerous shapes** (parent-close with open children; rapid sibling-close bursts).
3. **Expose recovery affordances** so a human can clean up damage already done.

A guiding constraint: kata must remain easy to use. Friction lives only on the operations that are genuinely high-risk — primarily `kata close --reason done` and parent-close. Comment, edit, label, link, create, search, and the cheap close reasons (`duplicate`, `superseded`, `wontfix`) stay light.

## 2. Scope

### In v1 (this spec)

- Reason enum expanded; close requires `--message` and (for most reasons) typed `--evidence`.
- Sugar flags for the honest path so common closures take one extra sentence and one piece of evidence.
- Daemon refuses parent-close while open children remain.
- Daemon refuses rapid sibling-close bursts (per-actor, per-parent, time-windowed).
- `close.throttled` event surfaced in `kata events --tail` default output.
- New `kata audit closes` command (read-only).
- `kata reopen` gains bulk-mode filters with required confirmation.
- Updated help banner and error text on `kata close`.

### Deferred (named here for forward-compat)

- **Resolution-kind + adversarial verifier.** A v2 milestone will add a `resolution_kind` field (`agent-claimed | human-accepted`) on the close event, plus a verifier subscriber (e.g. roborev) that consumes `agent-claimed` closes, runs evidence-appropriate checks, and reopens issues whose evidence fails. The v1 evidence schema in this spec is designed to feed that verifier without schema churn.
- **Stale-issue check** (refuse closing issues with no recent activity without `--stale-revalidate`). Orthogonal; defer.
- **Five-state status machine** (`open → in_progress → resolved_by_agent → verified → closed`). Heavier than `resolution_kind` and not needed if the lighter form proves sufficient.

### Dropped

- Generic same-actor close rate-limit. Replaced by the narrower, higher-signal sibling-burst rule.
- Standalone attestation flag (`--i-verified`). Ceremonial; agents will mechanically include it.
- No-touch refusal (refuse close if actor has no events on the issue). Bypassed in the 2026-05-08 incident — the agent had commented before closing. Shallow engagement is not absence of engagement.

## 3. Design

### 3.1 Friction principle

| Path | Friction | Rationale |
|---|---|---|
| `kata create`, `comment`, `edit`, `label`, `link`, `search`, `list`, `show` | unchanged | These are not closure assertions. |
| `kata close --reason wontfix` | message only (≥60 chars) | A decision, not a completion claim. |
| `kata close --reason duplicate` / `superseded` | one link evidence + ≥20-char message | The link is the substance. |
| `kata close --reason audit-no-change` | one rationale evidence + ≥40-char message | Audits without code change still need stated scope. |
| `kata close --reason done` | ≥1 implementation evidence + ≥40-char message | The dangerous claim. |
| Parent close while open children exist | refused | Catches the false `open_children: 0` pattern directly. |
| ≥4th sibling-close in 5 min by same actor under same parent | refused | Catches the rapid-burst pattern directly. |
| Same actor, same parent, same close message within 30 min (`done` / `audit-no-change` only) | refused | Catches slow copy-paste loops the burst rule misses. |
| `kata audit closes`, `kata reopen --filters` | unchanged for read; bulk-reopen requires confirmation | Powerful tools held out of the normal write path. |

Heavy guards only fire on shapes that match the observed incidents. Honest closures cost one short sentence and one piece of evidence.

### 3.2 Reason enum

The `reason` enum on the close action expands:

| value | meaning |
|---|---|
| `done` | The work the issue describes is complete. Existing. |
| `wontfix` | The team has decided not to do the work. Existing. |
| `duplicate` | Another issue covers the same work. Existing. Now requires a `duplicate-of` link. |
| `superseded` | The work was replaced by a different issue with a different scope. New. |
| `audit-no-change` | Investigation completed; no change was required. New. |

The wire enum on `api.ActionRequest.Body.Reason` and the SQLite check constraint both grow these two values. Existing closes remain valid; the wire enum is additive.

### 3.3 Evidence model

A new `evidence` field on the close action carries a typed-union array.

**Storage scope.** Message and evidence live on the close *event* payload only for v1. They are not duplicated onto the issue row. `kata audit closes` reads close-event payloads directly. Issues retain their existing schema unchanged; legacy closed issues (events without `message` or `evidence`) surface in audit as `flags=no-evidence` without backfill.

| `type` | payload fields | meaning |
|---|---|---|
| `commit` | `sha: string` | A git commit implementing the work. CLI runs `git rev-parse --verify <sha>` locally before sending; failure is a CLI-side error, not a daemon round-trip. |
| `pr` | `url: string` | Pull request URL. CLI validates URL syntax; verifier (v2) can fetch status. |
| `test` | `command: string` | A test command demonstrating the fix. Stored verbatim. |
| `reviewed-paths` | `paths: [string]` | Files reviewed without change. Built by repeating `--evidence reviewed-paths:<path>` or `--reviewed <path>` — the daemon normalizes repeats into one evidence item with a paths array. |
| `no-change-audit` | `rationale: string` | Free-text statement of why an audit concluded no change. |
| `duplicate-of` | `issue: int` | Issue number this duplicates. Must exist and be visible in the same project; daemon validates. |
| `superseded-by` | `issue: int` | Issue number that replaces this one. Same daemon validation. |

The wire form is JSON; each item is `{type, ...payload}`. Multiple items per close are allowed; per-reason rules in §3.5 cap how many of each `type` are valid. In particular, `commit`, `pr`, and `test` can appear multiple times (multi-commit fixes, multi-PR work); `duplicate-of`, `superseded-by`, and `no-change-audit` are exactly-one per close.

### 3.4 Substance check on `--message`

Two checks, both daemon-side:

1. **Length floor**, per reason:
   - `done`: ≥40 chars
   - `wontfix`: ≥60 chars
   - `duplicate`, `superseded`: ≥20 chars (the link is the substance)
   - `audit-no-change`: ≥40 chars
   Counted after stripping leading/trailing whitespace and collapsing internal runs.
2. **Trivial-phrase deny-list**, case-insensitive, whitespace-stripped exact match: `done`, `fixed`, `complete`, `completed`, `ok`, `okay`, `yes`, `no`, `n/a`, `na`, `skip`, `nope`. A message that normalizes to any of these is rejected even if length passes. Repeated-token padding (e.g. ten copies of `done`) is *not* covered by this rule — the length floor catches the shortest cases, and the per-parent repeat-message guard in §3.10 catches the laziest cross-issue patterns.

These checks are forcing functions, not lie detectors. Anyone willing to invent prose will pass them. The point is to make the laziest pattern (`--message done`) impossible without thought.

### 3.5 Per-reason validation matrix

| reason | evidence rule (counted post-merge of repeats) |
|---|---|
| `done` | ≥1 of: `commit`, `pr`, `test`, `reviewed-paths`. Any combination of these allowed; other types not allowed alongside. |
| `wontfix` | exactly 0 evidence items. |
| `duplicate` | exactly 1 `duplicate-of`. No other evidence items allowed. |
| `superseded` | exactly 1 `superseded-by`. No other evidence items allowed. |
| `audit-no-change` | exactly 1 `no-change-audit`. `reviewed-paths` allowed alongside; no other types. |

Violations are validation errors before the close commits.

### 3.6 CLI surface

#### Canonical form

```
kata close <ref> --reason <enum> --message "<text>" [--evidence <type>:<value> ...]
```

`--reason` becomes a required flag (was defaulted to `done`). Default was a footgun.

`--evidence` is repeatable. Its value is `<type>:<payload>` where payload is:

- `commit:<sha>` (40-hex or short SHA accepted by local `git rev-parse`)
- `pr:<url>` (any URL)
- `test:"<command>"`
- `reviewed-paths:<path>` (repeat the whole flag per path)
- `no-change-audit:"<rationale>"`
- `duplicate-of:<N>`
- `superseded-by:<N>`

#### Sugar flags

The canonical form is verbose for the common honest path. Sugar flags are pure aliases:

| sugar | expands to |
|---|---|
| `--done` | `--reason done` |
| `--wontfix` | `--reason wontfix` |
| `--audit-no-change` | `--reason audit-no-change` |
| `--duplicate-of N` | `--reason duplicate --evidence duplicate-of:N` |
| `--superseded-by N` | `--reason superseded --evidence superseded-by:N` |
| `--commit <sha>` | `--evidence commit:<sha>` |
| `--pr <url>` | `--evidence pr:<url>` |
| `--test "<cmd>"` | `--evidence test:"<cmd>"` |
| `--reviewed <path>` (repeatable) | `--evidence reviewed-paths:<path>` |

Sugar and canonical can mix; combining `--reason done --done` is a CLI conflict error. Combining `--duplicate-of 7 --evidence duplicate-of:7` is also rejected (same item twice) — the user has to pick one form.

Examples:

```
# common done case, ~10 keystrokes more than the old form
kata close 42 --done \
  --message "Fixed Safari callback double-submit and verified auth tests." \
  --test "go test ./internal/auth"

# no-change audit
kata close 281 --audit-no-change \
  --message "Reviewed schema, queries, service, and tests; table remains metadata-only." \
  --reviewed internal/db/schema.sql \
  --reviewed internal/services/fund

# duplicate, one liner
kata close 99 --duplicate-of 7 --message "Same Safari race; merge thread there."
```

#### `--dry-run` on close

`kata close <ref> --dry-run [other flags]` performs full client-side and daemon-side validation but does not mutate. The daemon returns the would-be close event plus any structural refusal (parent-not-empty, sibling-throttle, missing evidence). Useful for agents to check a parent before attempting close.

### 3.7 Help and error text

`kata close --help` opens with a banner block above the flag table:

> Closing an issue asserts that the work it describes is complete.
> This is a stronger claim than a comment. Provide evidence and a
> substantive message.
>
> If you have not completed and tested this work, do not close it.
> Instead, label and comment:
>     kata edit <ref> --label needs-review
>     kata comment <ref> --body "what was attempted, what remains"

Validation errors name the failure mode and the alternative. Example for a missing-evidence `done` close:

```
kata close 42: --evidence required for --reason done.

Accepted implementation evidence types:
  commit:<sha>           a git commit implementing the work
  pr:<url>               a pull request that resolves the issue
  test:<command>         a test that demonstrates the fix
  reviewed-paths:<path>  files reviewed without change (repeatable)

If you have not completed and tested this work, do not close it.
Use `kata edit 42 --label needs-review` and comment what remains.
```

The deny-list rejection names the rule:

```
kata close 42: --message rejected as trivial ("done").
Provide a substantive message describing what was changed and how
it was verified. If the work is not actually complete, do not close —
use `kata edit 42 --label needs-review` and comment what remains.
```

### 3.8 Parent-close completeness check

The daemon refuses any close where the target issue has open children. The error lists child numbers and titles, truncated:

```
kata close 281: refusing — issue has 36 open children:
  #283  schema review: tx_event table
  #284  schema review: fund_event table
  ... (33 more, see `kata show 281 --json`)
Close children first, or scope this issue differently.
```

The truncation cap is the first 10 by issue number. The remainder is summarised. No `--orphan-children` escape hatch in v1 — if real need emerges, it can be added later with explicit per-child justification. Strict refusal is the conservative starting point.

Closed children that share the parent do not block the parent close — only open children do.

### 3.9 Sibling-close throttle

For each close action, the daemon evaluates:

> Has the same actor closed ≥3 other children of this issue's parent within the last 5 minutes?

If yes, the close is refused. Defaults are hardcoded in v1 (not configurable). The error names the recent siblings:

```
kata close 286: refusing — sibling-close throttle tripped.
You closed 3 children of #281 in the last 5 minutes:
  #283 closed 2 min ago
  #284 closed 2 min ago
  #285 closed 1 min ago
Slow down and review the scope of each remaining child before closing.
Wait for the throttle window to clear, or ask a human reviewer to
inspect and close.
```

Operators can disable the sibling-burst guard daemon-wide via `[close.throttle] enabled = false` in `<KATA_HOME>/config.toml`. Default is enabled. Projects whose workflow is "dispatch subagents in parallel and close the cohort in one wave" should set this flag; agents will continue to be gated by the substance/evidence check on `--message`/`--evidence` and by the parent-completeness refusal, which always run.

No per-request override flag. Real bursts wait out the window. Bad-actor bursts are capped to 3 per 5 minutes per parent per actor (when enabled).

The throttle also emits a `close.throttled` audit event on every refusal — the actor, parent, and the recent-sibling cohort. This is the surfacing primitive `kata events --tail` uses (see §3.11).

Issues without a parent are not subject to the throttle. The pattern requires parent-shared siblings; closes that are not siblings are evaluated independently.

### 3.10 Repeated-message guard

A complementary, slower-window check catches the *same lazy loop* pattern when the burst is slow enough to evade §3.9. Refuse a close if **all** of the following hold:

- Same actor.
- Same project.
- Both the issue being closed and a prior close have the same parent (skipped entirely if either issue has no parent).
- Same normalized close message.
- Prior close occurred within the last **30 minutes**.
- Both the current close and the prior close have reason `done` or `audit-no-change` (the rule does not apply when either side is `wontfix`, `duplicate`, or `superseded`, where reused prose is plausibly legitimate).

Normalization (cheap, deliberate): trim leading/trailing whitespace, collapse internal whitespace runs to one space, lowercase ASCII, strip trailing `.?!`. No NFKC, no Unicode case-folding — we only need to catch literal copy-paste, not motivated obfuscation.

Refusals emit a `close.throttled` event with `reason=duplicate-message` (distinct from the burst-throttle `reason=sibling-burst`). Error text:

```
kata close 285: refusing — identical close message to your close of
#283 at 14:01:22 ("Schema review complete, table is metadata-only.").
Both issues share parent #281, and the message has not changed.
Each closure should describe its specific issue. If the same prose
truly applies, close as `--duplicate-of` or `--superseded-by` instead.
```

No override flag in v1. Unparented issues are not subject to the rule; the parent relationship is what turns a reused message into a strong abuse signal.

### 3.11 `close.throttled` surfacing in `kata events --tail`

`kata events --tail` emits one NDJSON envelope per line. `close.throttled` events appear as a `type: "close.throttled"` envelope with the existing payload shape (`reason`, `parent`, `cohort`, `prior`), so any consumer already parsing the tail stream picks them up without changes — operators can grep for `close.throttled`, and tooling (dashboards, alerting bots) can match on the type or reason directly. v1 does not ship a bespoke text renderer: the marker idea added per-event special-casing without a clear consumer, and the NDJSON-only contract keeps the stream uniform.

### 3.12 `kata audit closes`

A new read-only subcommand:

```
kata audit closes [--since <ts>] [--until <ts>] [--actor <name>]
                  [--parent <N>] [--project <name>] [--reason <enum>]
                  [--no-evidence]
                  [--group-by actor | parent | actor,parent]
                  [--json]
```

It scans close events in the given window and emits one row per close, with derived columns: `time`, `actor`, `issue`, `parent`, `reason`, `evidence-types`, `flags` (a comma-list of: `throttled`, `no-evidence`, `rapid-burst`, etc.). The `--no-evidence` filter selects closes that carried zero evidence items (legacy / pre-v1 imports; post-v1 these should not exist except for `wontfix`).

`--group-by` collapses rows: e.g., `actor,parent` produces one row per `(actor, parent)` pair with counts and time-range.

JSON output is structured; text output is a table with rapid-burst rows highlighted (`!!` prefix in the `flags` column).

### 3.13 `kata reopen`

`kata reopen <ref>` reopens a single closed issue. v1 ships no bulk mode: the sibling-close throttle caps any single burst at 3 closes per actor per parent per 5 minutes, so the realistic recovery scope is small enough to reopen issue-by-issue (optionally driven by `kata audit closes --actor <agent> --since <ts>` piped to a shell loop). The complexity cost of bulk filters, server-side filter-required guards, superseded-close detection, and a confirm-count protocol was not justified by the bounded blast radius the other guards already enforce.

### 3.13b TUI bypass for human closes

Interactive humans pressing the close keystroke in `kata tui` would be punished by the substance + evidence gate (a 40-char prose minimum and a typed `--evidence` item per close is hostile to a single keystroke). The TUI close path posts `{"actor": ..., "source": "tui"}` to the same `/actions/close` endpoint. When the daemon sees `source=tui`, it **skips** the substance and evidence checks; the structural guards (parent-close completeness, sibling-burst throttle, repeated-message) still apply.

An agent could theoretically lie and pass `source=tui` to dodge the substance check — but the same agent could also forge evidence (commit SHAs, PR URLs, test names) for the CLI path, so adding a hard signal here would only annoy honest callers. The audit row for a TUI close still shows `flags=no-evidence` and the row is just as recoverable via `kata reopen` as any other lazy close.

### 3.14 Documentation and instruction updates

The CLI behavior change has to be reflected in every place that tells an agent (or a human) how to close an issue. The implementation must touch all of the following in the same change as the code:

- **`kata quickstart` (`cmd/kata/quickstart.go`).** Rewrite the close step. Currently it is step 9 of 10 with one line. The new step covers the canonical form, the most common sugar (`--done --message --commit|--pr|--test`), and the explicit instruction not to close incomplete work. Promote the close step earlier in the list (step 2 or 3, immediately after author resolution) so it is read by every agent that runs `kata quickstart`.
- **`AGENTS.md` (repo root).** Mirror the new quickstart wording verbatim where it appears. Agents reading the file out-of-session must see the same rules.
- **`README.md` (repo root).** Update any close-command example to the new form. Add a short "Closing issues" subsection that explains why close requires evidence, with the dangerous-claim framing. Keep create / comment / edit examples cheap and unchanged.
- **`CLAUDE.md` (repo root).** The project-instructions file has a "Project management" bullet list that mentions close. Update it to reference the new required flags and the parent-completeness rule.
- **`kata close --help`.** Banner block and per-flag descriptions per §3.7.
- **`kata reopen --help`.** Describe bulk mode and the required confirmation + reason.
- **`kata audit --help`** (new). Subcommand-level help and `kata audit closes --help`.
- **`kata events --tail` man-page line.** Note that `close.throttled` events are surfaced with a visible marker in the default output.
- **Spec quickstart paragraph in this repo's `CLAUDE.md`.** A one-line pointer to this spec from the spec index.

All doc updates land in the same PR as the code so the rules and the messaging never diverge.

## 4. Forward-compat for v2 (deferred work)

The v1 close event payload is the v2 verifier's input. v2 will:

- Add `resolution_kind: agent-claimed | human-accepted` to the close event payload. v1 closes existing at v2-ship-time are retro-tagged `human-accepted` (interpreted as: closed before the verifier existed; no async check is owed).
- Spawn a verifier subscriber (e.g. roborev) that consumes new `agent-claimed` closes from the event stream, exercises the evidence (`git rev-parse` on commits in the workspace's worktrees, fetch PR status, run `test` commands in a sandbox, diff `reviewed-paths`, follow `duplicate-of` / `superseded-by` links to ensure the targets are sane), and emits a verdict event.
- Reopen the issue on verification failure, with a `verification.failed` event citing the failed evidence item.

The schema in §3.3 was chosen so this layer can be added without changing the v1 wire format or migrating data.

## 5. Migration / rollout

- The new `reason` values (`superseded`, `audit-no-change`) extend the enum additively. The SQLite CHECK constraint and the OpenAPI enum string grow; no data migration.
- `--reason` becomes a required flag on `kata close`. A daemon upgrade alone does not enforce this — clients on older CLIs would still pass the old default. Acceptable because the daemon-side per-reason evidence validation will hard-reject closes that arrive without a substantive message and (for `done`) without evidence.
- Existing closed issues have no `evidence` field and no `message` field. The audit view tolerates this and surfaces them with `flags=no-evidence` (legacy). They do not need backfill.
- The `close.throttled` event is new; consumers that don't know it will simply not render it specially. The default `events --tail` output ships with the renderer.

## 6. Risks and open questions

- **False positives on the sibling throttle.** Legitimate burst closures (e.g. a feature is genuinely done across 5 children) hit the throttle on the 4th. The recommended workaround is to wait 5 minutes between groups of 3, or to escalate the remaining closures to a human reviewer. If this is too painful, v1.1 can introduce a structured override that requires per-child evidence summarised in one command — deliberately deferred until real friction is observed.
- **Trivial-phrase deny-list maintenance.** The list is short on purpose. If it grows, move it to config. Until then, the list lives in code with a comment naming the incidents that motivated it.
- **Daemon and CLI version skew.** The CLI does git-side validation for `commit` evidence; an older CLI talking to a new daemon would skip that check and let a non-existent SHA through. The daemon also validates SHA format syntactically (40-hex or 7+-hex) but cannot reach git. This is acceptable for v1 — the verifier in v2 will close the loop.
