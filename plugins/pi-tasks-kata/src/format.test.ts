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
      { short_id: "cd34", title: "Blocked task", status: "open", owner: "agent-a", labels: [], blockedBy: ["ab12"] },
      { short_id: "ab12", title: "Active task", status: "open", labels: ["in_progress"], blockedBy: [] },
    ]);

    expect(lines).toBe([
      "ab12 [in_progress] Active task",
      "cd34 [pending] Blocked task (agent-a) [blocked by ab12]",
    ].join("\n"));
  });

  it("formats task details from Kata show output", () => {
    const detail: KataIssueDetail = {
      issue: { uid: "01HZNQ7VFPK1XGD8R5MABCD4EX", short_id: "ef56", title: "Ship plugin", body: "Make it useful", status: "open", owner: "codex" },
      labels: ["agent:worker", "in_progress"],
      comments: [{ author: "codex", body: "Started" }],
      links: [{ type: "blocks", from: { uid: "01HZNQ7VFPK1XGD8R5MABCD4AA", short_id: "ab12" }, to: { uid: "01HZNQ7VFPK1XGD8R5MABCD4EX", short_id: "ef56" } }],
    };

    expect(formatTaskDetail(detail)).toContain("Task ef56: Ship plugin");
    expect(formatTaskDetail(detail)).toContain("Status: in_progress");
    expect(formatTaskDetail(detail)).toContain("Blocked by: ab12");
    expect(formatTaskDetail(detail)).toContain("Labels: agent:worker, in_progress");
  });
});
