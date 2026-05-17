# Pi Tasks Kata Plugin Design

## Goal

Add an in-repository TypeScript Pi extension that presents the familiar `@tintinweb/pi-tasks` core tool surface while using Kata as the task tracker and source of truth.

## Scope

The plugin lives under `plugins/pi-tasks-kata` and is installable or loadable by Pi as a TypeScript extension. It registers `TaskCreate`, `TaskList`, `TaskGet`, `TaskUpdate`, and `TaskExecute`. It does not implement the upstream persistent widget, local JSON task store, `TaskOutput`, or `TaskStop` in the first pass.

`TaskExecute` acts as a Kata lifecycle bridge. It validates that issues are open and unblocked, claims each issue by assigning an owner, marks it in progress with the `in_progress` label, comments that execution started, and spawns a subagent through the same Pi event RPC convention used by `@tintinweb/pi-tasks`. Subagent completion closes the issue with `done` and comments with the result. Subagent failure keeps the issue open, removes `in_progress`, and comments with the error.

## Architecture

The extension is split into focused TypeScript modules:

- `src/kata.ts`: small command runner and JSON parsing adapter around the `kata` CLI.
- `src/format.ts`: maps Kata issue/comment/link JSON into pi-tasks style text output.
- `src/subagents.ts`: request/reply RPC helper for `@tintinweb/pi-subagents`.
- `src/index.ts`: Pi extension entrypoint and tool registration.

All Kata mutations use CLI commands with `--json` where supported. The plugin passes `--workspace` when `KATA_WORKSPACE` is set, otherwise it lets Kata resolve the current working directory. It uses `KATA_AUTHOR`, `PI_AGENT_NAME`, or `USER` to choose the default owner for claims.

## Data Mapping

Kata issue numbers become task IDs. Kata `open` maps to `pending` unless the issue has an `in_progress` label, which maps to `in_progress`. Kata `closed` maps to `completed`. Kata `blocks` links map to pi-tasks `blocks`/`blockedBy` summaries.

`TaskCreate` maps `subject` to Kata title and `description` to body. If `agentType` is provided, the plugin records it as a label `agent:<agentType>`. Arbitrary metadata is recorded as a creation comment so Kata remains the durable ledger without needing a new metadata column.

## Error Handling

The adapter reports command failures with stderr/stdout context. Tool handlers return user-visible text for ordinary task validation failures such as missing issues, closed issues, blocked issues, or unavailable subagents.

## Testing

Unit tests use a fake Kata runner so command translation and lifecycle behavior are verified without requiring a running daemon. Tests cover create/list/get/update formatting and `TaskExecute` claim/start command sequence.
