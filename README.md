# kata カタ

Local-first issue tracking for humans and coding agents.

kata gives agents a structured place to record tasks, decisions, links,
comments, and state changes without turning GitHub Issues, markdown plans, or
chat transcripts into the source of truth.

The CLI is built for agents and automation: stable commands, JSON output, and
predictable failure modes. The TUI is built for people: browse, triage, edit,
and supervise agent-written work without reading raw JSON. Both talk to the
same local daemon and SQLite database.

Status: early public preview. The CLI, daemon, and TUI are usable, but command
contracts and UI details may still change before a stable release.

## What kata does today

What you can do:

- Track issues separately per project, with issue numbers that restart per
  project.
- Create, list, edit, close, reopen, comment, label, assign, and link issues.
- Search, idempotent-create, soft-delete, restore, and irreversibly purge.
- Browse and triage in a TUI (`kata tui`) over the same data.
- Stream state changes as durable events for polling, live tailing, hooks, and
  TUI updates.

How it's built:

- Workspace-to-project binding lives in `.kata.toml`, falling back to a git
  remote URL when no binding file exists.
- Data lives locally in SQLite under `KATA_HOME` behind a long-running daemon.
- Issues have stable ULID `uid` values in JSON; `#N` remains the project-scoped
  display label.
- `kata export` and `kata import` provide a git-friendly JSONL backup and
  schema cutover path.
- Successful commands emit JSON for reliable parsing by agents and scripts.

## Goals

Three priorities:

- Agent ergonomics: stable commands, JSON-first workflows, explicit workspace
  binding, search-before-create, idempotency keys, and predictable exit codes.
- Human oversight: a TUI that helps people browse, triage, edit, and supervise
  agent activity without reading raw JSON.
- Auditability: append-only comments, event history, actor attribution, and
  explicit destructive operations.

Longer term, kata is intended to support a shared server mode for teams, CI,
and multiple agents. That mode should be a real authenticated deployment, not
the local daemon exposed on a public interface.

## Why kata, and how is it different from Beads or git-bug?

kata is intentionally small. It is not a project-management suite, a git
workflow engine, or an agent worker pool. It is a durable task ledger that
humans and agents can both understand.

[Beads](https://github.com/gastownhall/beads) is a substantial tool in the
same space: a Dolt-powered distributed graph issue tracker for AI agents. Its
default shape is project-local: `bd init` creates a `.beads/` Dolt database
alongside the code, with native Dolt history, branching, merging, push/pull,
and optional server mode for concurrent writers. That does not mean Beads
requires git; it also supports git-free workflows.

kata makes a different architectural bet: the issue ledger should be a local
service adjacent to workspaces, not a database owned by each repository. A
repository that uses kata gets only a small, secret-free `.kata.toml` binding;
the canonical state lives in `KATA_HOME` behind a daemon API. That keeps task
state out of code history while still giving agents a structured coordination
layer and giving humans a TUI over the same event stream.

It also has a different complexity budget. Beads is a large, capable system
with distributed database semantics, merge behavior, federation, MCP
integration, and agent workflow machinery. kata is deliberately smaller: one
daemon, one local store, one HTTP API, one TUI, and a narrow issue model that
should stay easy to understand, operate, and teach to agents.

[git-bug](https://github.com/git-bug/git-bug) takes the most git-native
approach of the three: it stores issues, comments, and identities as git
objects under custom refs in the repository itself, and distributes them
through ordinary `git push` / `git pull`. Every clone carries the full issue
history offline, and bridges sync with GitHub, GitLab, and Jira. kata sits at
the other end of that spectrum — issue state lives outside git entirely, so
the workspace stays clean, non-git workspaces work identically, and issue
history is not interleaved with code history. The trade-off is that kata
cannot piggyback on git remotes for sharing; that is what the future
authenticated server is for.

| Design choice | Beads | kata |
|---|---|---|
| Storage boundary | Project-local `.beads/` Dolt database by default | User-local `KATA_HOME` SQLite database behind a daemon |
| Repository footprint | Owns issue state near the repo by default; can sync via Dolt remotes | Repo stores only `.kata.toml` project binding |
| Collaboration model | Dolt push/pull, Dolt server mode, federation, MCP tooling | Local daemon today; future authenticated shared server |
| IDs | Hash-based IDs by default; counter IDs optional | Per-project sequential numbers (`#12`) |
| Workflow shape | Rich graph tasks, priorities, claiming, messages, dependencies | Deliberately small issue ledger: status, comments, labels, owner, links, events |
| Git relationship | Git integration is optional but first-class; commit conventions and doctor checks can connect code history to issues | Git can help identify workspaces; kata does not infer issue state from commits |

All three approaches are useful. Beads is strongest when you want
distributed, database-versioned task memory that can travel with a project and
merge across branches or agents. git-bug is strongest when you want issue
state to live inside the repository's own git history and ride the same
remotes the code does. kata is aimed at a smaller, API-first issue system
that can span workspaces and eventually teams without forcing every user and
agent to understand the repository, git remote, or distributed database that
carries the issue state.

## Install

kata is built with Go. To build from source you need Go 1.26 or later and a
clone of this repository:

```sh
make build
make install
```

`make install` places `kata` in `~/.local/bin`. Make sure that directory is on
your `PATH`.

For development:

```sh
make test
```

## Quick Start

Initialize kata in a workspace:

```sh
kata init
```

`kata init` creates or resolves the project and writes `.kata.toml` when
needed. In a git workspace, the default project identity is derived from the
remote URL. For a non-git workspace or an explicit shared identity:

```sh
kata init --project github.com/example/product --name product
```

Create and inspect issues:

```sh
kata create "fix login race" --body "Safari can double-submit the callback."
kata list
kata show 1
kata comment 1 --body "Reproduced on macOS."
kata close 1 --reason done
```

Open the TUI for human triage:

```sh
kata tui
```

Press `?` inside the TUI for keybindings.

Use `--workspace <path>` when running from outside the project directory:

```sh
kata --workspace ~/code/product list --status all
```

Set the actor for a session:

```sh
export KATA_AUTHOR=codex-wesm-laptop
kata whoami
```

Actor precedence is `--as` > `KATA_AUTHOR` > `git config user.name` >
`anonymous`.

## Core Commands

Common issue commands:

```sh
kata create <title> [--body TEXT | --body-file PATH | --body-stdin]
                  [--label LABEL] [--owner NAME]
                  [--parent N] [--blocks N] [--idempotency-key KEY]
kata list [--status open|closed|all] [--limit N]
kata show <issue-ref>
kata edit <number> [--title TEXT] [--body TEXT] [--owner NAME]
kata comment <number> [--body TEXT | --body-file PATH | --body-stdin]
kata close <number> [--reason done|wontfix|duplicate]
kata reopen <number>
```

Labels, ownership, and relationships:

```sh
kata label add <number> <label>
kata label rm <number> <label>
kata labels
kata assign <number> <owner>
kata unassign <number>

kata parent <child-ref> <parent-ref> [--replace]
kata unparent <child-ref>
kata block <blocker-ref> <blocked-ref>
kata unblock <blocker-ref> <blocked-ref>
kata relate <a-ref> <b-ref>
kata unrelate <a-ref> <b-ref>
kata link <from-ref> <parent|blocks|related> <to-ref>
kata unlink <from-ref> <parent|blocks|related> <to-ref>
```

For `show` and relationship commands, an issue ref can be `#N`, `N`, a full
UID, or a unique UID prefix of at least 8 characters.

Search, readiness, events, and project inspection:

```sh
kata search <query> [--limit N] [--include-deleted]
kata ready [--limit N]
kata events [--after N] [--limit N]
kata events --tail [--last-event-id N]
kata digest --since 24h [--until ...] [--project-id N | --all-projects]
            [--actor NAME ...]
kata projects list
kata projects show <project>
kata projects rename <project> <name>
kata projects merge <source> <target> [--rename-target NAME]
kata export [--output PATH]
kata import --input PATH --target PATH [--force]
```

`kata digest` summarizes activity over a time window. It groups events by
actor and lists per-issue actions (created, commented:N, closed:done,
labeled:bug, unblocks:#7, ...) so you can see at a glance what each agent or
person did overnight. `--since` accepts a duration (`24h`, `7d`) or an
RFC3339 timestamp; `--until` defaults to now. The default scope is the
current workspace's project; pass `--all-projects` for a cross-project
digest, or `--project-id N` for an explicit one. `--actor` is repeatable to
limit the report to one or more actors.

Destructive operations are explicit:

```sh
kata delete <number> --force --confirm "DELETE #<number>"
kata restore <number>
kata purge <number> --force --confirm "PURGE #<number>"
```

`delete` is reversible. `purge` is not.

Daemon, diagnostics, and agent instructions:

```sh
kata daemon status
kata daemon stop
kata daemon reload
kata daemon logs --hooks [--tail]
kata health
kata whoami
kata quickstart
kata tui
```

## Agent Quickstart

This is the short version to give any coding agent, regardless of whether that
agent supports skills, memories, or custom instructions. It is also shipped
with the CLI:

```sh
kata quickstart
kata agent-instructions   # alias
```

Session setup:

- Run from the project workspace, or pass `--workspace <path>`.
- Set `KATA_AUTHOR` once at session start.
- Prefer `--json` for reads and writes when you need to parse output.

Per-task guidelines:

- Never create a project implicitly. If the workspace is not initialized,
  report that `kata init` is needed.
- Search before creating; pass an idempotency key when you do create.
- Prefer updating existing issues over opening duplicates.
- Close only when the work is actually complete.
- Do not run `delete` or `purge` unless the user explicitly asks for that
  exact destructive action and issue number.

Use relationships deliberately. The link types mean:

| Type | Meaning |
|---|---|
| `parent` | This issue is part of a larger issue. |
| `blocks` | The first issue must be resolved before the second can proceed. |
| `related` | Useful context, but not ordering. |

Example session:

```sh
# Search before creating
kata search "login race" --json

# If no existing issue fits, create with an idempotency key
kata create "fix login race" \
  --body "Observed double-submit in Safari callback." \
  --idempotency-key "login-race-2026-05-02" \
  --json

# Update an existing issue rather than open a duplicate
kata show 12 --json
kata comment 12 --body "Found another reproduction path." --json
kata label add 12 safari --json
kata block 12 18 --json

# Close when done
kata close 12 --reason done --json
```

For long-running agents, poll events and remember the returned cursor; resume
from it on the next call. If a response says `reset_required`, discard cached
kata state and resume from the reset cursor.

```sh
kata events --after 0 --limit 100 --json
```

For live streams, `--tail` emits newline-delimited JSON:

```sh
kata events --tail
```

## Sharing and multi-user workflows

Today kata is local-first:

- one local daemon;
- one local SQLite database;
- no authentication;
- trusted same-user CLI and TUI clients.

Multiple checkouts or repositories can share one kata project when they use
the same `.kata.toml` project identity and run `kata init` in each checkout.
That shares issue numbering, labels, links, and events across those workspaces
in the same local database.

If a repository rename accidentally creates a second project, merge the old
source into the surviving target, for example:

```sh
kata projects merge old-repo new-repo --rename-target new-repo
```

Future shared mode should be a distinct deployment:

- a shared kata server reachable over HTTPS, SSH tunnel, or a private network;
- authenticated users and service tokens;
- server-derived actor identity;
- server-side hooks and backups;
- the same project, issue, event, and relationship model.

The local daemon should not be exposed directly to a LAN or public network.

## Configuration

Useful environment variables:

- `KATA_HOME`: data directory. Defaults to `~/.kata`.
- `KATA_DB`: explicit SQLite database path.
- `KATA_AUTHOR`: default actor for mutations.
- `KATA_HTTP_TIMEOUT`: per-request CLI timeout for non-streaming daemon calls
  (any `time.ParseDuration` string, e.g. `30s`, `2m`). Defaults to `5s`. Bump
  this for bulk imports where create requests can exceed the default.
- `XDG_RUNTIME_DIR`: runtime socket parent on Unix.

The workspace binding file is intentionally secret-free:

```toml
version = 1

[project]
identity = "github.com/example/product"
name = "product"
```

Commit `.kata.toml` when multiple agents, clones, or worktrees should resolve
to the same kata project.
