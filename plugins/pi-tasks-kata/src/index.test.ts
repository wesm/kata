import { afterEach, describe, expect, it, vi } from "vitest";
import plugin from "./index.js";
import type { KataRunner } from "./kata.js";

function json(data: Record<string, unknown>) {
  return JSON.stringify({ kata_api_version: 1, ...data });
}

function fakePi(
  runner: KataRunner,
  options: { spawnError?: string; completeDuringSpawnReply?: boolean } = {},
) {
  const tools = new Map<string, any>();
  const handlers = new Map<string, (data: unknown) => void>();
  const emitted: Array<{ channel: string; data: any }> = [];
  const pi: any = {
    registerTool(tool: any) {
      tools.set(tool.name, tool);
    },
    registerCommand() {},
    on() {},
    events: {
      on(channel: string, handler: (data: unknown) => void) {
        handlers.set(channel, handler);
        return () => handlers.delete(channel);
      },
      emit(channel: string, data: any) {
        emitted.push({ channel, data });
        if (channel === "subagents:rpc:spawn") {
          queueMicrotask(() => {
            handlers.get(`subagents:rpc:spawn:reply:${data.requestId}`)?.({
              ...(options.spawnError
                ? { success: false, error: options.spawnError }
                : { success: true, data: { id: "agent-123" } }),
            });
            if (options.completeDuringSpawnReply) {
              handlers.get("subagents:completed")?.({ id: "agent-123", result: "fast" });
            }
          });
        }
      },
    },
    __kataRunner: runner,
    __env: { author: "pi-agent" },
  };
  return { pi, tools, handlers, emitted };
}

describe("pi-tasks-kata extension", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("registers the core pi-tasks tool names", () => {
    const { pi, tools } = fakePi(async () => json({}));

    plugin(pi);

    expect([...tools.keys()]).toEqual(["TaskCreate", "TaskList", "TaskGet", "TaskUpdate", "TaskExecute"]);
  });

  it("TaskExecute claims the Kata task, spawns a subagent, and closes on completion", async () => {
    const calls: string[][] = [];
    const { pi, tools, handlers } = fakePi(async (args) => {
      calls.push(args);
      if (args[0] === "show") {
        return json({
          issue: { number: 9, title: "Implement adapter", body: "Use Kata", status: "open", owner: null },
          labels: [{ label: "agent:worker" }],
          links: [],
          comments: [],
        });
      }
      return json({ issue: { number: 9, title: "Implement adapter", status: "open" }, changed: true });
    });
    plugin(pi);

    const result = await tools.get("TaskExecute").execute("call-1", { task_ids: ["ab19"] });
    handlers.get("subagents:completed")?.({ id: "agent-123", result: "done" });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(result.content[0].text).toContain("ab19 -> agent agent-123");
    expect(calls).toContainEqual(["assign", "ab19", "pi-agent", "--json"]);
    expect(calls).toContainEqual(["label", "add", "ab19", "in_progress", "--json"]);
    expect(calls).toContainEqual(["close", "ab19", "--reason", "done", "--json"]);
    expect(calls).toContainEqual(["comment", "ab19", "--body", "TaskExecute completed via agent agent-123.\n\nResult:\ndone", "--json"]);
  });

  it("TaskExecute records failure when spawn fails after claim", async () => {
    const calls: string[][] = [];
    const { pi, tools } = fakePi(async (args) => {
      calls.push(args);
      if (args[0] === "show") {
        return json({
          issue: { number: 10, title: "Run worker", body: "Start it", status: "open", owner: null },
          labels: [{ label: "agent:worker" }],
          links: [],
          comments: [],
        });
      }
      return json({ issue: { number: 10, title: "Run worker", status: "open" }, changed: true });
    }, { spawnError: "subagents unavailable" });
    plugin(pi);

    const result = await tools.get("TaskExecute").execute("call-1", { task_ids: ["ab10"] });

    expect(result.content[0].text).toContain("ab10: subagents unavailable");
    expect(calls).toContainEqual(["label", "rm", "ab10", "in_progress", "--json"]);
    expect(calls).toContainEqual(["unassign", "ab10", "--json"]);
    expect(calls).toContainEqual([
      "comment",
      "ab10",
      "--body",
      "TaskExecute failed via agent spawn.\n\nError:\nsubagents unavailable",
      "--json",
    ]);
  });

  it("TaskExecute records the agent mapping before immediate lifecycle events can arrive", async () => {
    const calls: string[][] = [];
    const { pi, tools } = fakePi(async (args) => {
      calls.push(args);
      if (args[0] === "show") {
        return json({
          issue: { number: 11, title: "Fast task", body: "Finish quickly", status: "open", owner: null },
          labels: [{ label: "agent:worker" }],
          links: [],
          comments: [],
        });
      }
      return json({ issue: { number: 11, title: "Fast task", status: "open" }, changed: true });
    }, { completeDuringSpawnReply: true });
    plugin(pi);

    await tools.get("TaskExecute").execute("call-1", { task_ids: ["ab11"] });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(calls).toContainEqual(["close", "ab11", "--reason", "done", "--json"]);
    expect(calls).toContainEqual(["comment", "ab11", "--body", "TaskExecute completed via agent agent-123.\n\nResult:\nfast", "--json"]);
  });

  it("logs lifecycle mutation failures instead of dropping unhandled rejections", async () => {
    const calls: string[][] = [];
    const errors: unknown[] = [];
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    const onUnhandled = (reason: unknown) => errors.push(reason);
    process.on("unhandledRejection", onUnhandled);
    const { pi, tools, handlers } = fakePi(async (args) => {
      calls.push(args);
      if (args[0] === "show") {
        return json({
          issue: { number: 12, title: "Fragile task", body: "Handle cleanup", status: "open", owner: null },
          labels: [{ label: "agent:worker" }],
          links: [],
          comments: [],
        });
      }
      if (args[0] === "close") {
        throw new Error("kata close failed");
      }
      return json({ issue: { number: 12, title: "Fragile task", status: "open" }, changed: true });
    });
    plugin(pi);

    await tools.get("TaskExecute").execute("call-1", { task_ids: ["ab12"] });
    handlers.get("subagents:completed")?.({ id: "agent-123", result: "done" });
    await new Promise((resolve) => setTimeout(resolve, 0));
    process.off("unhandledRejection", onUnhandled);

    expect(errors).toEqual([]);
    expect(consoleError).toHaveBeenCalledWith(
      "[pi-tasks-kata] failed to record subagent completion for agent-123 / task ab12:",
      "kata close failed",
    );
  });

  it("keeps a launched agent active when recording the spawn comment fails", async () => {
    const calls: string[][] = [];
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    const { pi, tools, handlers } = fakePi(async (args) => {
      calls.push(args);
      if (args[0] === "show") {
        return json({
          issue: { number: 13, title: "Comment fragile", body: "Keep running", status: "open", owner: null },
          labels: [{ label: "agent:worker" }],
          links: [],
          comments: [],
        });
      }
      if (args[0] === "comment" && String(args[3]).includes("spawned subagent")) {
        throw new Error("spawn comment failed");
      }
      return json({ issue: { number: 13, title: "Comment fragile", status: "open" }, changed: true });
    });
    plugin(pi);

    const result = await tools.get("TaskExecute").execute("call-1", { task_ids: ["ab13"] });
    handlers.get("subagents:completed")?.({ id: "agent-123", result: "done" });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(result.content[0].text).toContain("ab13 -> agent agent-123");
    expect(result.content[0].text).not.toContain("Failed");
    expect(calls).toContainEqual(["close", "ab13", "--reason", "done", "--json"]);
    expect(calls).not.toContainEqual([
      "comment",
      "ab13",
      "--body",
      "TaskExecute failed via agent spawn.\n\nError:\nspawn comment failed",
      "--json",
    ]);
    expect(consoleError).toHaveBeenCalledWith(
      "[pi-tasks-kata] failed to record spawn comment for agent-123 / task ab13:",
      "spawn comment failed",
    );
  });

  it("reports the original spawn failure and continues when failure cleanup fails", async () => {
    const calls: string[][] = [];
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    const { pi, tools } = fakePi(async (args) => {
      calls.push(args);
      if (args[0] === "show") {
        return json({
          issue: { short_id: String(args[1]), title: `Task ${args[1]}`, body: "Run it", status: "open", owner: null },
          labels: [{ label: "agent:worker" }],
          links: [],
          comments: [],
        });
      }
      if (args[0] === "label" && args[1] === "rm" && args[2] === "ab14") {
        throw new Error("cleanup failed");
      }
      return json({ issue: { short_id: String(args[1] ?? args[2]), title: "Task", status: "open" }, changed: true });
    }, { spawnError: "subagents unavailable" });
    plugin(pi);

    const result = await tools.get("TaskExecute").execute("call-1", { task_ids: ["ab14", "ab15"] });

    expect(result.content[0].text).toContain("ab14: subagents unavailable");
    expect(result.content[0].text).toContain("ab15: subagents unavailable");
    expect(calls).toContainEqual(["show", "ab15", "--json"]);
    expect(consoleError).toHaveBeenCalledWith(
      "[pi-tasks-kata] failed to record spawn failure for task ab14:",
      "cleanup failed",
    );
  });
});
