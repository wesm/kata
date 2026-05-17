# Pi Tasks Kata Plugin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an in-repo TypeScript Pi extension that uses Kata as the durable task tracker.

**Architecture:** The plugin is a small package under `plugins/pi-tasks-kata`. A `KataClient` translates tool operations to `kata --json` commands, format helpers render pi-tasks style text, and the extension entrypoint registers Pi tools plus subagent lifecycle listeners.

**Tech Stack:** TypeScript, Vitest, `typebox`, `@mariozechner/pi-coding-agent`, Kata CLI.

---

### Task 1: Package Scaffold

**Files:**
- Create: `plugins/pi-tasks-kata/package.json`
- Create: `plugins/pi-tasks-kata/tsconfig.json`
- Create: `plugins/pi-tasks-kata/src/types.ts`
- Create: `plugins/pi-tasks-kata/src/result.ts`

- [ ] Add a TypeScript package with build/typecheck/test scripts.
- [ ] Define shared task result helpers that match Pi tool output.
- [ ] Run `npm install` inside `plugins/pi-tasks-kata`.

### Task 2: Kata Adapter

**Files:**
- Create: `plugins/pi-tasks-kata/src/kata.ts`
- Create: `plugins/pi-tasks-kata/src/kata.test.ts`

- [ ] Write failing tests for `TaskCreate`, `TaskList`, `TaskGet`, and `TaskUpdate` command translation.
- [ ] Implement `KataClient` with injectable runner, workspace-aware args, JSON parsing, labels, assignment, comments, and links.
- [ ] Run `npm test -- kata.test.ts` and confirm tests pass after implementation.

### Task 3: Formatting

**Files:**
- Create: `plugins/pi-tasks-kata/src/format.ts`
- Create: `plugins/pi-tasks-kata/src/format.test.ts`

- [ ] Write failing tests for issue status mapping, list lines, detail rendering, and blockers.
- [ ] Implement formatting helpers that map Kata issues to pi-tasks style text.
- [ ] Run `npm test -- format.test.ts` and confirm tests pass after implementation.

### Task 4: Pi Extension And TaskExecute

**Files:**
- Create: `plugins/pi-tasks-kata/src/subagents.ts`
- Create: `plugins/pi-tasks-kata/src/index.ts`
- Create: `plugins/pi-tasks-kata/src/index.test.ts`

- [ ] Write failing tests that register expected tools and verify `TaskExecute` claims, labels, comments, and spawns.
- [ ] Implement Pi tool registration for `TaskCreate`, `TaskList`, `TaskGet`, `TaskUpdate`, and `TaskExecute`.
- [ ] Implement subagent completion/failure listeners that update Kata issues.
- [ ] Run `npm test` and `npm run typecheck`.

### Task 5: Documentation And Verification

**Files:**
- Create: `plugins/pi-tasks-kata/README.md`

- [ ] Document install/load commands, environment variables, tool behavior, and TaskExecute lifecycle.
- [ ] Run `npm run build`.
- [ ] Run repository verification relevant to the new package.
- [ ] Commit all accepted changes.
