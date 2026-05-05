export type TaskStatus = "pending" | "in_progress" | "completed";

export type TaskUpdateStatus = TaskStatus | "deleted";

export interface KataIssue {
  number: number;
  title: string;
  body?: string;
  status: "open" | "closed" | string;
  owner?: string | null;
  labels?: string[];
  blockedBy?: number[];
}

export interface KataComment {
  author?: string;
  body: string;
}

export interface KataLink {
  type: "parent" | "blocks" | "related" | string;
  from_number: number;
  to_number: number;
}

export interface KataIssueDetail {
  issue: KataIssue;
  comments: KataComment[];
  labels: string[];
  links: KataLink[];
}

export interface CreateTaskInput {
  subject: string;
  description: string;
  activeForm?: string;
  agentType?: string;
  idempotencyKey?: string;
  metadata?: Record<string, unknown>;
}

export interface UpdateTaskInput {
  status?: TaskUpdateStatus;
  subject?: string;
  description?: string;
  activeForm?: string;
  owner?: string;
  metadata?: Record<string, unknown>;
  addBlocks?: string[];
  addBlockedBy?: string[];
}

export interface ClaimOptions {
  agentType?: string;
  additionalContext?: string;
  model?: string;
  maxTurns?: number;
}

export interface ExecutionClaim {
  issue: KataIssue;
  agentType: string;
  prompt: string;
  assignedByClaim: boolean;
  claimLockPath?: string;
}
