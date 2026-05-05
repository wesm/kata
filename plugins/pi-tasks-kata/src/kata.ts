import { spawn } from "node:child_process";
import { mkdir, rm } from "node:fs/promises";
import { dirname, join } from "node:path";
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
  claimLockRoot?: string | false;
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
  private claimLocks = new Map<string, Promise<void>>();
  private claimLockRoot?: string;
  readonly author: string;

  constructor(options: KataClientOptions = {}) {
    this.runner = options.runner ?? defaultKataRunner;
    this.workspace = options.workspace ?? process.env.KATA_WORKSPACE;
    this.claimLockRoot = options.claimLockRoot === false
      ? undefined
      : options.claimLockRoot ?? (options.runner ? undefined : this.workspace ?? process.cwd());
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
    const claimLockPath = await this.acquireClaimLock(taskId);
    try {
      const claim = await this.withClaimLock(taskId, () => this.claimForExecutionLocked(taskId, options));
      return { ...claim, claimLockPath };
    } catch (error) {
      await this.releaseClaimLock(claimLockPath);
      throw error;
    }
  }

  private async claimForExecutionLocked(taskId: string, options: ClaimOptions = {}): Promise<ExecutionClaim> {
    const detail = await this.showTask(taskId);
    if (detail.issue.status === "closed") throw new Error(`Task #${taskId} is already completed`);
    if (detail.labels.includes("in_progress")) throw new Error(`Task #${taskId} is already in progress`);
    if (detail.issue.owner && detail.issue.owner !== this.author) {
      throw new Error(`Task #${taskId} is already owned by ${detail.issue.owner}`);
    }

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

    let assignedByClaim = false;
    let labeled = false;
    try {
      await this.assign(taskId, this.author);
      assignedByClaim = !detail.issue.owner;
      await this.addLabel(taskId, "in_progress");
      labeled = true;
      await this.comment(taskId, `TaskExecute started by ${this.author} using agent type ${agentType}.`);
    } catch (error) {
      if (labeled) {
        try {
          await this.removeLabel(taskId, "in_progress");
        } catch {
          // Continue rollback so unassignment still runs; preserve the original failure.
        }
      }
      if (assignedByClaim) {
        try {
          await this.unassign(taskId);
        } catch {
          // Preserve the original claim failure even if ownership cleanup fails.
        }
      }
      throw error;
    }

    return {
      issue: detail.issue,
      agentType,
      prompt: buildExecutionPrompt(detail, options.additionalContext),
      assignedByClaim,
    };
  }

  async releaseExecutionClaim(claim: ExecutionClaim): Promise<void> {
    await this.releaseClaimLock(claim.claimLockPath);
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

  async failExecution(taskId: string, agentId: string, error?: string, options: { releaseOwner?: boolean } = {}): Promise<void> {
    await this.removeLabel(taskId, "in_progress");
    if (options.releaseOwner) {
      await this.unassign(taskId);
    }
    const suffix = error ? `\n\nError:\n${error}` : "";
    await this.comment(taskId, `TaskExecute failed via agent ${agentId}.${suffix}`);
  }

  async assign(taskId: string, owner: string): Promise<void> {
    await this.runJSON(["assign", taskId, owner, "--json"]);
  }

  async unassign(taskId: string): Promise<void> {
    await this.runJSON(["unassign", taskId, "--json"]);
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
    } catch (error) {
      if (isAbsentLabelError(error, label)) return;
      throw error;
    }
  }

  private async withClaimLock<T>(taskId: string, fn: () => Promise<T>): Promise<T> {
    const previous = this.claimLocks.get(taskId) ?? Promise.resolve();
    let release!: () => void;
    const next = new Promise<void>((resolve) => {
      release = resolve;
    });
    const tail = previous.catch(() => {}).then(() => next);
    this.claimLocks.set(taskId, tail);

    await previous.catch(() => {});
    try {
      return await fn();
    } finally {
      release();
      if (this.claimLocks.get(taskId) === tail) {
        this.claimLocks.delete(taskId);
      }
    }
  }

  private async acquireClaimLock(taskId: string): Promise<string | undefined> {
    if (!this.claimLockRoot) return undefined;
    const lockPath = join(this.claimLockRoot, ".kata", "pi-tasks-kata", "claims", `task-${safeLockName(taskId)}.lock`);
    await mkdir(dirname(lockPath), { recursive: true });
    try {
      await mkdir(lockPath);
    } catch (error) {
      if (isAlreadyExistsError(error)) {
        throw new Error(`Task #${taskId} claim lock is already held`);
      }
      throw error;
    }
    return lockPath;
  }

  private async releaseClaimLock(lockPath: string | undefined): Promise<void> {
    if (!lockPath) return;
    await rm(lockPath, { recursive: true, force: true });
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

function isAbsentLabelError(error: unknown, label: string): boolean {
  const message = error instanceof Error ? error.message : String(error);
  if (!message.includes(label)) return false;
  return /already removed|not found|no label|absent|not attached/i.test(message);
}

function isAlreadyExistsError(error: unknown): boolean {
  return typeof error === "object" && error !== null && "code" in error && error.code === "EEXIST";
}

function safeLockName(taskId: string): string {
  return taskId.replace(/[^a-zA-Z0-9._-]/g, "_");
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
