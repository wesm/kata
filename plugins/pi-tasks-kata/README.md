# pi-tasks-kata

`pi-tasks-kata` is a Pi extension that exposes the core `@tintinweb/pi-tasks`
tool workflow while storing tasks in Kata.

## Load

From this repository:

```sh
cd plugins/pi-tasks-kata
npm install
pi -e ./src/index.ts
```

The extension expects `kata` on `PATH` and the current workspace to be
initialized with `kata init`.

## Tools

- `TaskCreate`: creates a Kata issue. `agentType` is stored as label
  `agent:<type>`.
- `TaskList`: lists Kata issues as `pending`, `in_progress`, or `completed`.
- `TaskGet`: renders `kata show --json` as a pi-tasks style detail view.
- `TaskUpdate`: edits title/body/owner, adds dependency links, adds or removes
  the `in_progress` label, and closes issues for `completed`.
- `TaskExecute`: claims open unblocked issues, assigns the owner, adds
  `in_progress`, comments that execution started, and spawns a pi subagent.

`TaskExecute` listens for `subagents:completed` and `subagents:failed`. On
success it closes the Kata issue with `reason=done` and comments with the
result. On failure it leaves the issue open, removes `in_progress`, and
comments with the error.

## Environment

- `KATA_WORKSPACE`: passed to `kata --workspace` when set.
- `KATA_AUTHOR`: preferred owner/actor for `TaskExecute` claims.
- `PI_AGENT_NAME` or `USER`: fallback owner when `KATA_AUTHOR` is unset.

Deletion through `TaskUpdate status=deleted` is intentionally unsupported.
Use Kata's explicit destructive commands when deletion is truly intended.
