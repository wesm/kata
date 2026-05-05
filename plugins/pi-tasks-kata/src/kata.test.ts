import { describe, expect, it } from "vitest";
import { KataClient, type KataRunner } from "./kata.js";

function json(data: Record<string, unknown>) {
  return JSON.stringify({ kata_api_version: 1, ...data });
}

function recordingRunner(responses: string[] = []): { runner: KataRunner; calls: string[][] } {
  const calls: string[][] = [];
  return {
    calls,
    runner: async (args) => {
      calls.push(args);
      return responses.shift() ?? json({ issue: { number: 1, title: "ok", status: "open" }, changed: true });
    },
  };
}

describe("KataClient", () => {
  it("creates a Kata issue with body, agent label, idempotency key, and workspace", async () => {
    const { runner, calls } = recordingRunner([
      json({ issue: { number: 7, title: "Fix auth", body: "Details", status: "open" }, changed: true }),
    ]);
    const kata = new KataClient({ runner, workspace: "/repo" });

    const issue = await kata.createTask({
      subject: "Fix auth",
      description: "Details",
      agentType: "worker",
      idempotencyKey: "fix-auth",
    });

    expect(issue.number).toBe(7);
    expect(calls).toEqual([
      [
        "--workspace",
        "/repo",
        "create",
        "Fix auth",
        "--body",
        "Details",
        "--label",
        "agent:worker",
        "--idempotency-key",
        "fix-auth",
        "--json",
      ],
    ]);
  });

  it("lists all Kata issues as task candidates", async () => {
    const { runner, calls } = recordingRunner([
      json({ issues: [{ number: 1, title: "First", status: "open" }] }),
    ]);
    const kata = new KataClient({ runner });

    const issues = await kata.listTasks();

    expect(issues).toHaveLength(1);
    expect(calls).toEqual([["list", "--status", "all", "--limit", "200", "--json"]]);
  });

  it("updates details, status labels, and dependency links with Kata commands", async () => {
    const { runner, calls } = recordingRunner();
    const kata = new KataClient({ runner });

    await kata.updateTask("3", {
      subject: "New title",
      description: "New body",
      owner: "agent-a",
      status: "in_progress",
      addBlocks: ["4"],
      addBlockedBy: ["2"],
    });

    expect(calls).toEqual([
      ["edit", "3", "--title", "New title", "--body", "New body", "--owner", "agent-a", "--json"],
      ["label", "add", "3", "in_progress", "--json"],
      ["block", "3", "4", "--json"],
      ["block", "2", "3", "--json"],
    ]);
  });

  it("claims and starts an executable task before returning spawn context", async () => {
    const { runner, calls } = recordingRunner([
      json({
        issue: { number: 5, title: "Write tests", body: "Cover adapter", status: "open", owner: null },
        labels: [{ label: "agent:worker" }],
        links: [],
        comments: [],
      }),
    ]);
    const kata = new KataClient({ runner, author: "pi-agent" });

    const execution = await kata.claimForExecution("5", { additionalContext: "Use Vitest" });

    expect(execution.agentType).toBe("worker");
    expect(execution.prompt).toContain("Write tests");
    expect(execution.prompt).toContain("Use Vitest");
    expect(calls).toEqual([
      ["show", "5", "--json"],
      ["assign", "5", "pi-agent", "--json"],
      ["label", "add", "5", "in_progress", "--json"],
      ["comment", "5", "--body", "TaskExecute started by pi-agent using agent type worker.", "--json"],
    ]);
  });

  it("rejects tasks that are already in progress before claiming", async () => {
    const { runner, calls } = recordingRunner([
      json({
        issue: { number: 6, title: "Already running", body: "Do work", status: "open", owner: "pi-agent" },
        labels: [{ label: "agent:worker" }, { label: "in_progress" }],
        links: [],
        comments: [],
      }),
    ]);
    const kata = new KataClient({ runner, author: "pi-agent" });

    await expect(kata.claimForExecution("6")).rejects.toThrow("already in progress");

    expect(calls).toEqual([["show", "6", "--json"]]);
  });

  it("compensates assignment and in-progress label when claim comments fail", async () => {
    const calls: string[][] = [];
    const runner: KataRunner = async (args) => {
      calls.push(args);
      if (args[0] === "show") {
        return json({
          issue: { number: 8, title: "Partial claim", body: "Do work", status: "open", owner: null },
          labels: [{ label: "agent:worker" }],
          links: [],
          comments: [],
        });
      }
      if (args[0] === "comment") {
        throw new Error("comment failed");
      }
      return json({ issue: { number: 8, title: "Partial claim", status: "open" }, changed: true });
    };
    const kata = new KataClient({ runner, author: "pi-agent" });

    await expect(kata.claimForExecution("8")).rejects.toThrow("comment failed");

    expect(calls).toEqual([
      ["show", "8", "--json"],
      ["assign", "8", "pi-agent", "--json"],
      ["label", "add", "8", "in_progress", "--json"],
      ["comment", "8", "--body", "TaskExecute started by pi-agent using agent type worker.", "--json"],
      ["label", "rm", "8", "in_progress", "--json"],
      ["unassign", "8", "--json"],
    ]);
  });
});
