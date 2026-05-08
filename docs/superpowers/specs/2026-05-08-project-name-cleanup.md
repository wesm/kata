# Collapse project identity into project name

Status: draft, awaiting review.

## Motivation

A kata project today carries two user-facing strings:

```toml
[project]
identity = "github.com/example/foo"
name     = "foo"
```

`identity` is the canonical lookup key; `name` is a display label. The split was designed for three things:

1. A globally-unique key independent of display name (collision-proof under a hypothetical multi-tenant deployment).
2. Auto-derivation from a git remote so two clones converge on the same project.
3. Stable project reference that survives display-name renames, decoupled from alias bookkeeping.

In the current single-user local model, none of those pulls its weight. The duplication surfaces in `.kata.toml`, in CLI flag semantics (`kata init --project github.com/example/foo` is the documented workflow), in events, in hook env vars, in JSONL exports, in agent docs. Layering a `--project <selector>` flag for cross-project commands on top *cements* the confusion by giving agents a third way to reference the same thing.

## Target

A project is identified by a single string: its `name`. The word **identity** disappears from every user-facing surface (CLI help, `.kata.toml`, error messages, event payloads, hook env vars, JSONL field names, docs).

`.kata.toml` becomes:

```toml
[project]
name = "foo"
```

CLI surface:

- `kata init` derives `name` from the last segment of the git remote (`github.com/example/foo` → `foo`), or from the workspace directory name outside git.
- `kata init --project foo` creates project `foo` if missing, attaches this repo if it exists.
- `kata create --project foo "title"`, `kata list --project foo`, etc. — `--project foo` resolves to the project named `foo` on every project-scoped command. Errors if it doesn't exist (only `kata init` may create).

Internal identifiers, kept as-is:

- `projects.id` — sqlite int, daemon-internal, never user-facing.
- `projects.uid` — ULID, durable cross-cutover identity in JSONL exports and event payloads. Stays.
- `project_aliases.alias_identity` — git remote URL or local path bound to a project. Stays. The word "identity" inside the alias table is internal and accurate (alternative identifiers for the project) and never leaves the table.

## Schema change

`projects`:
- Drop the `identity` column.
- `name` becomes `NOT NULL UNIQUE`.

`project_aliases`:
- Unchanged.

`projects.uid` and `projects.id` are unaffected.

## Resolution rules (post-cutover)

The resolver hierarchy when a workspace command runs:

1. **Alias first.** If the workspace has a discoverable git remote or local-path alias that maps to a `project_aliases` row → resolve to that `project_id`. Authoritative.
2. **`.kata.toml` `name` second, but only when unambiguous.** If no alias matched (no git, no row), look up by name. A matched project is accepted only when there is no evidence of drift; a missing name fails rather than binding to anything else.
3. **`--project <name>` flag** overrides both for the duration of one command. The project must already exist (only `kata init` may create).
4. **Drift reconciliation.** When alias and `name` both resolve but disagree (e.g., a project was renamed and the workspace's `.kata.toml` is stale), alias wins and the resolver rewrites `.kata.toml` with the current name. One-line stderr notice on the rewrite.
5. **Refuse silent binding.** If `.kata.toml` has a `name` that does not match any current project AND no alias is available, the command fails with `project_not_initialized` rather than guessing from the legacy `identity` field, git metadata, or any fuzzy selector. Suggested fix: `kata init --project <correct-name>`.

Rule (4) handles renames. Rule (5) handles the post-cutover collision case where suffixing renamed the project.

## `kata projects rename` semantics

Renaming a project under "name is the canonical reference" is **not** cosmetic; it can stale every attached repo's `.kata.toml`. Spelled out:

- `kata projects rename <old> <new>` updates `projects.name` and:
  - Rewrites the *current workspace's* `.kata.toml` if it points at this project.
  - Lists every other attached workspace (from `project_aliases.root_path`, when present and readable) so the operator knows what may be stale on disk. The daemon does not reach across the network to rewrite remote workspace files; the alias-fallback resolver (rule 4 above) repairs them on next access.
  - Refuses if `<new>` already names another project (UNIQUE constraint).
- Workspaces without a discoverable alias (e.g., non-git, or `.kata.toml` outside any registered alias root) lose the safety net. They surface `project_not_initialized` per rule (5) on next command, with a hint: "this workspace is bound by name and that name no longer matches; run `kata init --project <new>`".

This is the contract that keeps "name is identity" honest.

## Migration via JSONL cutover

Following the established `internal/jsonl/cutover.go` pattern:

1. Bump `currentSchemaVersion`.
2. Update `internal/db/schema.sql` to the new shape (`name UNIQUE`, no `identity`).
3. Add a version-aware exporter that reads the old shape (with `identity`).
4. Importer:
   - Drops the `identity` field on project rows.
   - Where two pre-cutover rows had the same `name` (legal under the old schema), deterministically suffix `-2`, `-3`, etc., to satisfy the new uniqueness constraint. The order is: lower `id` keeps the bare name, others get suffixes in id order. Stderr notice lists each renamed project: `note: project #<id> renamed from "<old>" to "<new>" during cutover`.
5. `.kata.toml` parser tolerates a stale `identity = "..."` line as a compatibility-only field during this schema version. It is ignored on read; writer never emits it. Any notice belongs in callers that rewrite the file, not inside the parser. The legacy `identity` line is **not** consulted during resolution — only `name` and aliases. Resolution rule (5) prevents silent mis-binding when name was suffixed.

Operators with two pre-cutover projects sharing a name will have one of them renamed (e.g., `<name>-2`); that workspace's `.kata.toml` resolves via alias fallback if a git remote or local path alias is registered, otherwise via rule (5) failure with a clear fix-it.

## Surfaces touched

`identity` is leaving every user-facing place. Inventory:

| Surface | Today | After |
|---|---|---|
| `.kata.toml` | `[project] identity = "..."; name = "..."` | `[project] name = "..."` |
| `POST /api/v1/projects` body | `project_identity` | `name` |
| `POST /api/v1/projects/resolve` body | `project_identity` (`start_path` continues to work) | `name` (`start_path` continues to work) |
| API project response (`api.ProjectOut`) | `identity`, `name` | `name` |
| Event payload (`api.events`, `internal/api/events.go:48`) | `project_identity` | `project_name` |
| Hook env var (`internal/hooks/runner.go:281`) | `KATA_PROJECT_IDENTITY` | `KATA_PROJECT_NAME` |
| JSONL export (`internal/jsonl/export.go`) | `identity` field on project rows | (absent; cutover drops it) |
| JSONL import (`internal/jsonl/import.go`) | reads `identity` | reads `identity` only when importing a pre-cutover JSONL; otherwise absent |
| TUI client request (`internal/tui/client.go:194`) | `project_identity` | `name` |
| Digest, purge, list, search human output | no current `identity` field shown — verify | unchanged |
| CLI help text & error messages | "project identity" | "project name" |

This is a breaking change for anything programmatically consuming `--json` envelopes or event SSE/NDJSON payloads. Pre-stable status; we accept the break.

## Affected files (rough)

- `internal/db/schema.sql` — drop column, add UNIQUE.
- `internal/db/db.go` — bump `currentSchemaVersion`.
- `internal/db/queries.go`, `internal/db/queries_projects_*.go` — query updates; remove `ProjectByIdentity`-style helpers, add name-keyed equivalents.
- `internal/jsonl/{export,import,cutover}.go`, `internal/jsonl/fixtures_test.go` — version-aware export of the old shape; drop-identity import; deterministic suffixing on name collisions.
- `internal/api/types.go` — request/response shape; `ProjectOut.Identity` → removed.
- `internal/api/events.go` — `ProjectIdentity` → `ProjectName`.
- `internal/daemon/handlers_projects.go` — accept `name` instead of `project_identity`; collapse identity-derivation logic to name-derivation; rename helpers (`PickInitIdentity` → `PickInitName`, `ErrIdentityConflict` → `ErrNameConflict`, etc.).
- `internal/daemon/handlers_projects_test.go` — sweep `project_identity` → `name`.
- `internal/hooks/runner.go` — `KATA_PROJECT_IDENTITY` → `KATA_PROJECT_NAME`.
- `internal/tui/client.go`, `internal/tui/client_test.go` — request shape.
- `internal/config/project_identity.go` — rename and trim; `ComputeAliasIdentity` (alias bookkeeping) keeps its job and its name (it's about the *alias's* identity, internal). `PickInitIdentity` becomes `PickInitName`.
- `internal/config/project_config.go` — toml parser tolerates stale `identity` line for one cycle; writer emits only `name`.
- `cmd/kata/init.go`, `cmd/kata/projects.go`, `cmd/kata/quickstart.go` — help text, error messages, agent guidance.
- `docs/superpowers/specs/2026-04-29-kata-design.md` — sweep "identity" references in §2 (project resolution), §5.1, §6 (CLI surface).
- `README.md` — example commands.

## Out of scope

- The new `--project <name>` flag for non-init commands. Lands as the next commit on the post-cleanup model. (Originally PR #29; closed.)
- Future multi-user / shared-server namespacing. If/when shared mode ships, collisions get solved at that layer (e.g., `org/name`), not retro-fitted into the single-user schema now.
- Renaming `project_aliases.alias_identity` or its handling helpers. Internal, accurate, untouched.

## Order of operations

1. Land this cleanup as one commit on a fresh branch.
2. Add `--project <name>` to project-scoped commands as the next commit on the same branch.
3. Open one new PR covering both.

## Risks / open questions

- **Operator data with name collisions.** Handled by deterministic suffixing + stderr notice + alias-fallback resolver. Pre-stable status accepted.
- **Workspaces without git alias bound by stale name.** Surfaced explicitly via rule (5) — refuse silent mis-binding, point at `kata init --project <correct-name>`. No data loss.
- **Future multi-user mode lock-in.** Dropping identity now means a future shared mode invents its own namespacing scheme rather than reusing the existing `identity` column. Correct tradeoff: don't preserve unused complexity for a feature that may never look the way we currently think.
- **`PickInitIdentity` complexity.** The function juggles `.kata.toml`, `--project`, and git-remote derivation, with conflict detection between them. Most of that logic survives — it's still picking a string for a fresh init. The conflict-error rename (`ErrIdentityConflict` → `ErrNameConflict`) is mechanical.
