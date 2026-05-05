import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
import { Type } from "typebox";
import { formatTaskDetail, formatTaskList } from "./format.js";
import { KataClient, type KataRunner } from "./kata.js";
import { textResult } from "./result.js";
import { spawnSubagent } from "./subagents.js";
import type { UpdateTaskInput } from "./types.js";

type PiWithTestHooks = ExtensionAPI & {
  __kataRunner?: KataRunner;
  __env?: {
    workspace?: string;
    author?: string;
  };
};

export default function (pi: ExtensionAPI) {
  const hooks = pi as PiWithTestHooks;
  const kata = new KataClient({
    runner: hooks.__kataRunner,
    workspace: hooks.__env?.workspace,
    author: hooks.__env?.author,
  });
  const agentTaskMap = new Map<string, string>();

  pi.registerTool({
    name: "TaskCreate",
    label: "TaskCreate",
    description: "Create a Kata-backed task. The task is stored as a Kata issue and can later be started with TaskExecute.",
    parameters: Type.Object({
      subject: Type.String({ description: "A brief title for the task" }),
      description: Type.String({ description: "Detailed task context and acceptance criteria" }),
      activeForm: Type.Optional(Type.String({ description: "Present continuous form for UI display" })),
      agentType: Type.Optional(Type.String({ description: "Subagent type to use with TaskExecute; stored as label agent:<type>" })),
      idempotencyKey: Type.Optional(Type.String({ description: "Kata idempotency key for safe retries" })),
      metadata: Type.Optional(Type.Record(Type.String(), Type.Any(), { description: "Metadata recorded as a Kata comment" })),
    }),
    async execute(_toolCallId, params) {
      const issue = await kata.createTask(params);
      return textResult(`Task #${issue.number} created successfully: ${issue.title}`);
    },
  });

  pi.registerTool({
    name: "TaskList",
    label: "TaskList",
    description: "List Kata-backed tasks with pi-tasks style statuses.",
    parameters: Type.Object({}),
    async execute() {
      const issues = await kata.listTasks();
      return textResult(formatTaskList(issues));
    },
  });

  pi.registerTool({
    name: "TaskGet",
    label: "TaskGet",
    description: "Get full details for a Kata-backed task.",
    parameters: Type.Object({
      taskId: Type.String({ description: "Kata issue number" }),
    }),
    async execute(_toolCallId, params) {
      const detail = await kata.showTask(params.taskId);
      return textResult(formatTaskDetail(detail));
    },
  });

  pi.registerTool({
    name: "TaskUpdate",
    label: "TaskUpdate",
    description: "Update a Kata-backed task. Status in_progress adds the in_progress label; completed closes the issue.",
    parameters: Type.Object({
      taskId: Type.String({ description: "Kata issue number" }),
      status: Type.Optional(Type.Unsafe<UpdateTaskInput["status"]>({
        type: "string",
        enum: ["pending", "in_progress", "completed", "deleted"],
      })),
      subject: Type.Optional(Type.String({ description: "New task title" })),
      description: Type.Optional(Type.String({ description: "New task body" })),
      activeForm: Type.Optional(Type.String({ description: "Recorded as a Kata comment" })),
      owner: Type.Optional(Type.String({ description: "Kata issue owner" })),
      metadata: Type.Optional(Type.Record(Type.String(), Type.Any(), { description: "Recorded as a Kata comment" })),
      addBlocks: Type.Optional(Type.Array(Type.String(), { description: "Task IDs this task blocks" })),
      addBlockedBy: Type.Optional(Type.Array(Type.String(), { description: "Task IDs that block this task" })),
    }),
    async execute(_toolCallId, params) {
      const { taskId, ...fields } = params;
      const changed = await kata.updateTask(taskId, fields);
      return textResult(changed.length > 0 ? `Updated task #${taskId} ${changed.join(", ")}` : `Task #${taskId} unchanged`);
    },
  });

  pi.registerTool({
    name: "TaskExecute",
    label: "TaskExecute",
    description: "Claim one or more Kata tasks, mark them in progress, and execute them as pi subagents.",
    parameters: Type.Object({
      task_ids: Type.Array(Type.String(), { description: "Kata issue numbers to execute" }),
      agent_type: Type.Optional(Type.String({ description: "Override agent type; otherwise label agent:<type> is used" })),
      additional_context: Type.Optional(Type.String({ description: "Extra context appended to each subagent prompt" })),
      model: Type.Optional(Type.String({ description: "Model override for subagents" })),
      max_turns: Type.Optional(Type.Number({ description: "Max turns per subagent", minimum: 1 })),
    }),
    async execute(_toolCallId, params) {
      const launched: string[] = [];
      const failures: string[] = [];
      for (const taskId of params.task_ids) {
        let claimed = false;
        try {
          const claim = await kata.claimForExecution(taskId, {
            agentType: params.agent_type,
            additionalContext: params.additional_context,
            model: params.model,
            maxTurns: params.max_turns,
          });
          claimed = true;
          const agentId = await spawnSubagent(pi.events, claim.agentType, claim.prompt, {
            model: params.model,
            maxTurns: params.max_turns,
          });
          agentTaskMap.set(agentId, taskId);
          await kata.recordAgentSpawn(taskId, agentId);
          launched.push(`#${taskId} -> agent ${agentId}`);
        } catch (error) {
          const message = error instanceof Error ? error.message : String(error);
          if (claimed) {
            await kata.failExecution(taskId, "spawn", message);
          }
          failures.push(`#${taskId}: ${message}`);
        }
      }
      const lines = [];
      if (launched.length > 0) lines.push(`Launched ${launched.length} agent(s):\n${launched.join("\n")}`);
      if (failures.length > 0) lines.push(`Failed:\n${failures.join("\n")}`);
      return textResult(lines.join("\n\n") || "No tasks launched");
    },
  });

  pi.events.on("subagents:completed", (data) => {
    const event = data as { id?: string; result?: string };
    if (!event.id) return;
    const taskId = agentTaskMap.get(event.id);
    if (!taskId) return;
    agentTaskMap.delete(event.id);
    void kata.completeExecution(taskId, event.id, event.result);
  });

  pi.events.on("subagents:failed", (data) => {
    const event = data as { id?: string; error?: string; status?: string };
    if (!event.id) return;
    const taskId = agentTaskMap.get(event.id);
    if (!taskId) return;
    agentTaskMap.delete(event.id);
    void kata.failExecution(taskId, event.id, event.error ?? event.status);
  });

  pi.registerCommand("kata-tasks", {
    description: "Show Kata-backed task tool help",
    handler: async (_args, ctx) => {
      ctx.ui.notify("Kata task tools loaded: TaskCreate, TaskList, TaskGet, TaskUpdate, TaskExecute", "info");
    },
  });
}
