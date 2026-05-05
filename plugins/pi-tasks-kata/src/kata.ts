import { spawn } from "node:child_process";
import { blockersFor } from "./format.js";
import type {
  ClaimOptions,
  CreateTaskInput,
  ExecutionClaim,
  KataIssue,
  KataIssueDetail,
  KataLink,
  UpdateTaskInput,
} from "./types.js";

export type KataRunner = (args: string[], options?: { signal?: AbortSignal }) => Promise<string>;

export interface KataClientOptions {
  runner?: KataRunner;
  workspace?: string;
  author?: string;
}

interface KataEnvelope {
  issue?: KataIssue;
  issues?: KataIssue[];
  comments?: Array<{ author?: string; body: string }>;
  labels?: Array<{ label: string }> | string[];
  links?: KataLink[];
}

export class KataClient {
  private runner: KataRunner;
  private workspace?: string;
  readonly author: string;

  constructor(options: KataClientOptions = {}) {
    this.runner = options.runner ?? defaultKataRunner;
    this.workspace = options.workspace ?? process.env.KATA_WORKSPACE;
    this.author = options.author ?? process.env.KATA_AUTHOR ?? process.env.PI_AGENT_NAME ?? process.env.USER ?? "pi-agent";
  }

  async createTask(input: CreateTaskInput): Promise<KataIssue> {
    const args = ["create", input.subject, "--body", input.description];
    if (input.agentType) args.push("--label", `agent:${input.agentType}`);
    if (input.idempotencyKey) args.push("--idempotency-key", input.idempotencyKey);
    args.push("--json");
    const env = await this.runJSON(args);
    if (!env.issue) throw new Error("kata create did not return an issue");
    if (input.activeForm || (input.metadata && Object.keys(input.metadata).length > 0)) {
      const metadata = JSON.stringify({ activeForm: input.activeForm, ...input.metadata });
      await this.comment(String(env.issue.number), `Task metadata: ${metadata}`);
    }
    return env.issue;
  }

  async listTasks(limit = 200): Promise<KataIssue[]> {
    const env = await this.runJSON(["list", "--status", "all", "--limit", String(limit), "--json"]);
    return normalizeIssues(env.issues ?? []);
  }

  async showTask(taskId: string): Promise<KataIssueDetail> {
    const env = await this.runJSON(["show", taskId, "--json"]);
    if (!env.issue) throw new Error(`Task #${taskId} not found`);
    return {
      issue: env.issue,
      comments: env.comments ?? [],
      labels: normalizeLabels(env.labels),
      links: env.links ?? [],
    };
  }

  async updateTask(taskId: string, input: UpdateTaskInput): Promise<string[]> {
    const changed: string[] = [];
    const editArgs = ["edit", taskId];
    if (input.subject !== undefined) editArgs.push("--title", input.subject);
    if (input.description !== undefined) editArgs.push("--body", input.description);
    if (input.owner !== undefined) editArgs.push("--owner", input.owner);
    if (editArgs.length > 2) {
      await this.runJSON([...editArgs, "--json"]);
      changed.push("details");
    }

    if (input.status === "in_progress") {
      await this.addLabel(taskId, "in_progress");
      changed.push("status");
    } else if (input.status === "pending") {
      await this.removeLabel(taskId, "in_progress");
      await this.runJSON(["reopen", taskId, "--json"]);
      changed.push("status");
    } else if (input.status === "completed") {
      await this.removeLabel(taskId, "in_progress");
      await this.runJSON(["close", taskId, "--reason", "done", "--json"]);
      changed.push("status");
    } else if (input.status === "deleted") {
      throw new Error("TaskUpdate status=deleted is not supported by the Kata-backed plugin; use kata delete explicitly.");
    }

    for (const target of input.addBlocks ?? []) {
      await this.runJSON(["block", taskId, target, "--json"]);
      changed.push("blocks");
    }
    for (const blocker of input.addBlockedBy ?? []) {
      await this.runJSON(["block", blocker, taskId, "--json"]);
      changed.push("blockedBy");
    }

    if (input.metadata && Object.keys(input.metadata).length > 0) {
      await this.comment(taskId, `Task metadata update: ${JSON.stringify(input.metadata)}`);
      changed.push("metadata");
    }
    if (input.activeForm) {
      await this.comment(taskId, `Task active form: ${input.activeForm}`);
      changed.push("activeForm");
    }
    return [...new Set(changed)];
  }

  async claimForExecution(taskId: string, options: ClaimOptions = {}): Promise<ExecutionClaim> {
    const detail = await this.showTask(taskId);
    if (detail.issue.status === "closed") throw new Error(`Task #${taskId} is already completed`);
    if (detail.labels.includes("in_progress")) throw new Error(`Task #${taskId} is already in progress`);

    const openBlockers: number[] = [];
    for (const blocker of blockersFor(detail.issue.number, detail.links)) {
      const blockerDetail = await this.showTask(String(blocker));
      if (blockerDetail.issue.status !== "closed") openBlockers.push(blocker);
    }
    if (openBlockers.length > 0) {
      throw new Error(`Task #${taskId} is blocked by ${openBlockers.map((id) => `#${id}`).join(", ")}`);
    }

    const agentType = options.agentType ?? agentTypeFromLabels(detail.labels);
    if (!agentType) throw new Error(`Task #${taskId} has no agent type; add label agent:<type> or pass agent_type.`);

    let assigned = false;
    let labeled = false;
    try {
      await this.assign(taskId, this.author);
      assigned = true;
      await this.addLabel(taskId, "in_progress");
      labeled = true;
      await this.comment(taskId, `TaskExecute started by ${this.author} using agent type ${agentType}.`);
    } catch (error) {
      if (labeled) await this.removeLabel(taskId, "in_progress");
      if (assigned) await this.unassign(taskId);
      throw error;
    }

    return {
      issue: detail.issue,
      agentType,
      prompt: buildExecutionPrompt(detail, options.additionalContext),
    };
  }

  async recordAgentSpawn(taskId: string, agentId: string): Promise<void> {
    await this.comment(taskId, `TaskExecute spawned subagent ${agentId}.`);
  }

  async completeExecution(taskId: string, agentId: string, result?: string): Promise<void> {
    await this.removeLabel(taskId, "in_progress");
    await this.runJSON(["close", taskId, "--reason", "done", "--json"]);
    const suffix = result ? `\n\nResult:\n${result}` : "";
    await this.comment(taskId, `TaskExecute completed via agent ${agentId}.${suffix}`);
  }

  async failExecution(taskId: string, agentId: string, error?: string): Promise<void> {
    await this.removeLabel(taskId, "in_progress");
    const suffix = error ? `\n\nError:\n${error}` : "";
    await this.comment(taskId, `TaskExecute failed via agent ${agentId}.${suffix}`);
  }

  async assign(taskId: string, owner: string): Promise<void> {
    await this.runJSON(["assign", taskId, owner, "--json"]);
  }

  async unassign(taskId: string): Promise<void> {
    try {
      await this.runJSON(["unassign", taskId, "--json"]);
    } catch {
      // Best-effort claim compensation should preserve the original failure.
    }
  }

  async comment(taskId: string, body: string): Promise<void> {
    await this.runJSON(["comment", taskId, "--body", body, "--json"]);
  }

  async addLabel(taskId: string, label: string): Promise<void> {
    await this.runJSON(["label", "add", taskId, label, "--json"]);
  }

  async removeLabel(taskId: string, label: string): Promise<void> {
    try {
      await this.runJSON(["label", "rm", taskId, label, "--json"]);
    } catch {
      // Removing an absent lifecycle label is harmless for this adapter.
    }
  }

  private async runJSON(args: string[]): Promise<KataEnvelope> {
    const out = await this.runner(this.withWorkspace(args));
    return JSON.parse(out) as KataEnvelope;
  }

  private withWorkspace(args: string[]): string[] {
    return this.workspace ? ["--workspace", this.workspace, ...args] : args;
  }
}

export const defaultKataRunner: KataRunner = (args, options = {}) =>
  new Promise((resolve, reject) => {
    const child = spawn("kata", args, {
      stdio: ["ignore", "pipe", "pipe"],
      signal: options.signal,
    });
    let stdout = "";
    let stderr = "";
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      stdout += chunk;
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk;
    });
    child.on("error", reject);
    child.on("close", (code) => {
      if (code === 0) {
        resolve(stdout);
        return;
      }
      reject(new Error(`kata ${args.join(" ")} failed with exit ${code}: ${stderr || stdout}`));
    });
  });

function normalizeLabels(labels: KataEnvelope["labels"]): string[] {
  if (!labels) return [];
  return labels.map((entry) => (typeof entry === "string" ? entry : entry.label)).filter(Boolean);
}

function normalizeIssues(issues: KataIssue[]): KataIssue[] {
  return issues.map((issue) => ({
    ...issue,
    labels: normalizeLabels(issue.labels as unknown as KataEnvelope["labels"]),
  }));
}

function agentTypeFromLabels(labels: string[]): string | undefined {
  const prefix = "agent:";
  return labels.find((label) => label.startsWith(prefix))?.slice(prefix.length);
}

function buildExecutionPrompt(detail: KataIssueDetail, additionalContext?: string): string {
  const parts = [
    `Work on Kata task #${detail.issue.number}: ${detail.issue.title}`,
    "",
    detail.issue.body ? `Description:\n${detail.issue.body}` : "Description: (none)",
    "",
    "When finished, report the concrete result. The parent task tracker will close or comment on the Kata issue from subagent lifecycle events.",
  ];
  if (additionalContext) {
    parts.push("", `Additional context:\n${additionalContext}`);
  }
  return parts.join("\n");
}
