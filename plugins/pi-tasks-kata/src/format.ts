import type { KataIssue, KataIssueDetail, KataLink, TaskStatus } from "./types.js";

export function statusForIssue(issue: Pick<KataIssue, "status" | "labels">): TaskStatus {
  if (issue.status === "closed") return "completed";
  if ((issue.labels ?? []).includes("in_progress")) return "in_progress";
  return "pending";
}

export function formatTaskList(issues: KataIssue[]): string {
  if (issues.length === 0) return "No tasks found";
  return [...issues]
    .sort((a, b) => a.number - b.number)
    .map((issue) => {
      let line = `#${issue.number} [${statusForIssue(issue)}] ${issue.title}`;
      if (issue.owner) line += ` (${issue.owner})`;
      if (issue.blockedBy && issue.blockedBy.length > 0) {
        line += ` [blocked by ${issue.blockedBy.map((id) => `#${id}`).join(", ")}]`;
      }
      return line;
    })
    .join("\n");
}

export function formatTaskDetail(detail: KataIssueDetail): string {
  const issue = { ...detail.issue, labels: detail.labels };
  const blockedBy = blockersFor(issue.number, detail.links);
  const blocks = blocksFor(issue.number, detail.links);
  const lines = [
    `Task #${issue.number}: ${issue.title}`,
    `Status: ${statusForIssue(issue)}`,
  ];
  if (issue.owner) lines.push(`Owner: ${issue.owner}`);
  if (issue.body) lines.push(`Description: ${issue.body}`);
  if (blockedBy.length > 0) lines.push(`Blocked by: ${blockedBy.map((id) => `#${id}`).join(", ")}`);
  if (blocks.length > 0) lines.push(`Blocks: ${blocks.map((id) => `#${id}`).join(", ")}`);
  if (detail.labels.length > 0) lines.push(`Labels: ${detail.labels.join(", ")}`);
  if (detail.comments.length > 0) {
    lines.push("Comments:");
    for (const comment of detail.comments) {
      lines.push(`- ${comment.author ?? "unknown"}: ${comment.body}`);
    }
  }
  return lines.join("\n");
}

export function blockersFor(issueNumber: number, links: KataLink[]): number[] {
  return links
    .filter((link) => link.type === "blocks" && link.to_number === issueNumber)
    .map((link) => link.from_number)
    .sort((a, b) => a - b);
}

export function blocksFor(issueNumber: number, links: KataLink[]): number[] {
  return links
    .filter((link) => link.type === "blocks" && link.from_number === issueNumber)
    .map((link) => link.to_number)
    .sort((a, b) => a - b);
}
