import { describe, expect, it } from "vitest";
import { formatTaskDetail, formatTaskList, statusForIssue } from "./format.js";
import type { KataIssueDetail } from "./types.js";

describe("format helpers", () => {
  it("maps Kata issue status and in_progress labels to pi task statuses", () => {
    expect(statusForIssue({ status: "open", labels: [] })).toBe("pending");
    expect(statusForIssue({ status: "open", labels: ["in_progress"] })).toBe("in_progress");
    expect(statusForIssue({ status: "closed", labels: ["in_progress"] })).toBe("completed");
  });

  it("formats task lists with owner and open blockers", () => {
    const lines = formatTaskList([
      { number: 2, title: "Blocked task", status: "open", owner: "agent-a", labels: [], blockedBy: [1] },
      { number: 1, title: "Active task", status: "open", labels: ["in_progress"], blockedBy: [] },
    ]);

    expect(lines).toBe([
      "#1 [in_progress] Active task",
      "#2 [pending] Blocked task (agent-a) [blocked by #1]",
    ].join("\n"));
  });

  it("formats task details from Kata show output", () => {
    const detail: KataIssueDetail = {
      issue: { number: 4, title: "Ship plugin", body: "Make it useful", status: "open", owner: "codex" },
      labels: ["agent:worker", "in_progress"],
      comments: [{ author: "codex", body: "Started" }],
      links: [{ type: "blocks", from_number: 2, to_number: 4 }],
    };

    expect(formatTaskDetail(detail)).toContain("Task #4: Ship plugin");
    expect(formatTaskDetail(detail)).toContain("Status: in_progress");
    expect(formatTaskDetail(detail)).toContain("Blocked by: #2");
    expect(formatTaskDetail(detail)).toContain("Labels: agent:worker, in_progress");
  });
});
