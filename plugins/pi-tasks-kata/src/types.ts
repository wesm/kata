export type TaskStatus = "pending" | "in_progress" | "completed";

export type TaskUpdateStatus = TaskStatus | "deleted";

export interface KataLinkPeer {
  uid?: string;
  short_id: string;
}

export interface KataIssue {
  id?: number;
  uid?: string;
  short_id?: string;
  qualified_id?: string;
  number?: number;
  title: string;
  body?: string;
  status: "open" | "closed" | string;
  owner?: string | null;
  labels?: string[];
  blockedBy?: string[];
  blocked_by?: KataLinkPeer[];
}

export interface KataComment {
  author?: string;
  body: string;
}

export interface KataLink {
  type: "parent" | "blocks" | "related" | string;
  from: KataLinkPeer;
  to: KataLinkPeer;
  from_number?: number;
  to_number?: number;
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
