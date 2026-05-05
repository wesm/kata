import { describe, expect, it } from "vitest";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { KataClient, KataCommandError, kataCommandForError, type KataRunner } from "./kata.js";

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
  it("redacts value-bearing CLI arguments in runner error labels", () => {
    const label = kataCommandForError([
      "--workspace",
      "/secret/repo",
      "comment",
      "7",
      "--body",
      "token=secret",
      "--json",
    ]);

    expect(label).toBe("kata comment");
    expect(label).not.toContain("secret");
  });

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
    const { runner, calls } = recordingRunner([
      json({
        issue: { number: 3, title: "New title", body: "New body", status: "open", owner: "agent-a" },
        labels: [],
        links: [],
        comments: [],
      }),
    ]);
    const kata = new KataClient({ runner, author: "agent-a" });

    await kata.updateTask("3", {
      subject: "New title",
      description: "New body",
      owner: "agent-a",
      status: "in_progress",
      addBlocks: ["4"],
      addBlockedBy: ["2"],
    });

    expect(calls).toEqual([
      ["show", "3", "--json"],
      ["edit", "3", "--title", "New title", "--body", "New body", "--owner", "agent-a", "--json"],
      ["label", "add", "3", "in_progress", "--json"],
      ["block", "3", "4", "--json"],
      ["block", "2", "3", "--json"],
    ]);
  });

  it("rejects option-like task ids before calling Kata", async () => {
    const { runner, calls } = recordingRunner();
    const kata = new KataClient({ runner });

    await expect(kata.showTask("--help")).rejects.toThrow("positive integer");

    expect(calls).toEqual([]);
  });

  it("rejects updates to tasks owned by another agent", async () => {
    const { runner, calls } = recordingRunner([
      json({
        issue: { number: 24, title: "Owned", body: "No touch", status: "open", owner: "other-agent" },
        labels: [],
        links: [],
        comments: [],
      }),
    ]);
    const kata = new KataClient({ runner, author: "pi-agent" });

    await expect(kata.updateTask("24", { status: "pending" })).rejects.toThrow("already owned by other-agent");

    expect(calls).toEqual([["show", "24", "--json"]]);
  });

  it("validates dependency ids before applying any update mutation", async () => {
    const { runner, calls } = recordingRunner();
    const kata = new KataClient({ runner });

    await expect(kata.updateTask("25", { subject: "New title", addBlocks: ["--help"] })).rejects.toThrow("positive integer");

    expect(calls).toEqual([]);
  });

  it("reopens closed tasks before moving them in progress", async () => {
    const { runner, calls } = recordingRunner([
      json({
        issue: { number: 22, title: "Restart", body: "Do it", status: "closed", owner: null },
        labels: [],
        links: [],
        comments: [],
      }),
    ]);
    const kata = new KataClient({ runner });

    await kata.updateTask("22", { status: "in_progress" });

    expect(calls).toEqual([
      ["show", "22", "--json"],
      ["reopen", "22", "--json"],
      ["label", "add", "22", "in_progress", "--json"],
    ]);
  });

  it("does not reopen already-open tasks when moving them pending", async () => {
    const { runner, calls } = recordingRunner([
      json({
        issue: { number: 23, title: "Pause", body: "Later", status: "open", owner: null },
        labels: [{ label: "in_progress" }],
        links: [],
        comments: [],
      }),
    ]);
    const kata = new KataClient({ runner });

    await kata.updateTask("23", { status: "pending" });

    expect(calls).toEqual([
      ["show", "23", "--json"],
      ["label", "rm", "23", "in_progress", "--json"],
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
    expect(execution.assignedByClaim).toBe(true);
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

  it("still unassigns when claim rollback cannot remove the in-progress label", async () => {
    const calls: string[][] = [];
    const runner: KataRunner = async (args) => {
      calls.push(args);
      if (args[0] === "show") {
        return json({
          issue: { number: 14, title: "Rollback label failure", body: "Do work", status: "open", owner: null },
          labels: [{ label: "agent:worker" }],
          links: [],
          comments: [],
        });
      }
      if (args[0] === "comment") {
        throw new Error("comment failed");
      }
      if (args[0] === "label" && args[1] === "rm") {
        throw new Error("label removal failed");
      }
      return json({ issue: { number: 14, title: "Rollback label failure", status: "open" }, changed: true });
    };
    const kata = new KataClient({ runner, author: "pi-agent" });

    await expect(kata.claimForExecution("14")).rejects.toThrow("comment failed");

    expect(calls).toContainEqual(["label", "rm", "14", "in_progress", "--json"]);
    expect(calls).toContainEqual(["unassign", "14", "--json"]);
  });

  it("preserves the original claim failure when unassign rollback fails", async () => {
    const calls: string[][] = [];
    const runner: KataRunner = async (args) => {
      calls.push(args);
      if (args[0] === "show") {
        return json({
          issue: { number: 15, title: "Rollback unassign failure", body: "Do work", status: "open", owner: null },
          labels: [{ label: "agent:worker" }],
          links: [],
          comments: [],
        });
      }
      if (args[0] === "comment") {
        throw new Error("comment failed");
      }
      return json({ issue: { number: 15, title: "Rollback unassign failure", status: "open" }, changed: true });
    };
    class ThrowingUnassignKataClient extends KataClient {
      override async unassign(taskId: string): Promise<void> {
        calls.push(["unassign-throw", taskId]);
        throw new Error("unassign failed");
      }
    }
    const kata = new ThrowingUnassignKataClient({ runner, author: "pi-agent" });

    await expect(kata.claimForExecution("15")).rejects.toThrow("comment failed");

    expect(calls).toContainEqual(["unassign-throw", "15"]);
  });

  it("rejects tasks owned by another agent before claiming", async () => {
    const { runner, calls } = recordingRunner([
      json({
        issue: { number: 19, title: "Owned task", body: "Do work", status: "open", owner: "other-agent" },
        labels: [{ label: "agent:worker" }],
        links: [],
        comments: [],
      }),
    ]);
    const kata = new KataClient({ runner, author: "pi-agent" });

    await expect(kata.claimForExecution("19")).rejects.toThrow("already owned by other-agent");

    expect(calls).toEqual([["show", "19", "--json"]]);
  });

  it("does not unassign pre-existing ownership when claim rollback fails", async () => {
    const calls: string[][] = [];
    const runner: KataRunner = async (args) => {
      calls.push(args);
      if (args[0] === "show") {
        return json({
          issue: { number: 20, title: "Already mine", body: "Do work", status: "open", owner: "pi-agent" },
          labels: [{ label: "agent:worker" }],
          links: [],
          comments: [],
        });
      }
      if (args[0] === "comment") {
        throw new Error("comment failed");
      }
      return json({ issue: { number: 20, title: "Already mine", status: "open" }, changed: true });
    };
    const kata = new KataClient({ runner, author: "pi-agent" });

    await expect(kata.claimForExecution("20")).rejects.toThrow("comment failed");

    expect(calls).not.toContainEqual(["unassign", "20", "--json"]);
  });

  it("refuses to claim while a durable task lock exists", async () => {
    const lockRoot = await mkdtemp(join(tmpdir(), "kata-claim-lock-"));
    await mkdir(join(lockRoot, ".kata", "pi-tasks-kata", "claims", "task-21.lock"), { recursive: true });
    const { runner, calls } = recordingRunner();
    const kata = new KataClient({ runner, author: "pi-agent", claimLockRoot: lockRoot });

    try {
      await expect(kata.claimForExecution("21")).rejects.toThrow("claim lock is already held");
      expect(calls).toEqual([]);
    } finally {
      await rm(lockRoot, { recursive: true, force: true });
    }
  });

  it("serializes concurrent claims for the same task", async () => {
    const calls: string[][] = [];
    let claimed = false;
    const runner: KataRunner = async (args) => {
      calls.push(args);
      if (args[0] === "show") {
        return json({
          issue: { number: 16, title: "One worker only", body: "Do work", status: "open", owner: claimed ? "pi-agent" : null },
          labels: claimed ? [{ label: "agent:worker" }, { label: "in_progress" }] : [{ label: "agent:worker" }],
          links: [],
          comments: [],
        });
      }
      if (args[0] === "label" && args[1] === "add") {
        claimed = true;
      }
      return json({ issue: { number: 16, title: "One worker only", status: "open" }, changed: true });
    };
    const kata = new KataClient({ runner, author: "pi-agent" });

    const results = await Promise.allSettled([
      kata.claimForExecution("16"),
      kata.claimForExecution("16"),
    ]);

    expect(results.filter((result) => result.status === "fulfilled")).toHaveLength(1);
    expect(results.filter((result) => result.status === "rejected")).toHaveLength(1);
    expect(calls.filter((args) => args[0] === "label" && args[1] === "add")).toHaveLength(1);
  });

  it("propagates unexpected in-progress label removal failures", async () => {
    const calls: string[][] = [];
    const runner: KataRunner = async (args) => {
      calls.push(args);
      if (args[0] === "label" && args[1] === "rm") {
        throw new Error("kata daemon unavailable");
      }
      return json({ issue: { number: 17, title: "Cleanup", status: "open" }, changed: true });
    };
    const kata = new KataClient({ runner, author: "pi-agent" });

    await expect(kata.failExecution("17", "agent-123", "boom")).rejects.toThrow("kata daemon unavailable");

    expect(calls).toEqual([["label", "rm", "17", "in_progress", "--json"]]);
  });

  it("ignores absent in-progress label removal", async () => {
    const calls: string[][] = [];
    const runner: KataRunner = async (args) => {
      calls.push(args);
      if (args[0] === "label" && args[1] === "rm") {
        throw new Error('kata label rm 18 in_progress failed with exit 1: label "in_progress" not found');
      }
      return json({ issue: { number: 18, title: "Cleanup", status: "open" }, changed: true });
    };
    const kata = new KataClient({ runner, author: "pi-agent" });

    await kata.failExecution("18", "agent-123", "boom");

    expect(calls).toContainEqual(["comment", "18", "--body", "TaskExecute failed via agent agent-123.\n\nError:\nboom", "--json"]);
  });

  it("detects absent label errors from sanitized command output", async () => {
    const calls: string[][] = [];
    const runner: KataRunner = async (args) => {
      calls.push(args);
      if (args[0] === "label" && args[1] === "rm") {
        throw new KataCommandError("kata label failed with exit 1 (output omitted)", 'label "in_progress" not found');
      }
      return json({ issue: { number: 26, title: "Cleanup", status: "open" }, changed: true });
    };
    const kata = new KataClient({ runner, author: "pi-agent" });

    await kata.failExecution("26", "agent-123", "boom");

    expect(calls).toContainEqual(["comment", "26", "--body", "TaskExecute failed via agent agent-123.\n\nError:\nboom", "--json"]);
  });

  it("closes completed executions before removing the in-progress label", async () => {
    const { runner, calls } = recordingRunner();
    const kata = new KataClient({ runner, author: "pi-agent" });

    await kata.completeExecution("27", "agent-123", "done");

    expect(calls.slice(0, 2)).toEqual([
      ["close", "27", "--reason", "done", "--json"],
      ["label", "rm", "27", "in_progress", "--json"],
    ]);
  });
});
