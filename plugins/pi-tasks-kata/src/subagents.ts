import { randomUUID } from "node:crypto";

type EventBus = {
  on(channel: string, handler: (data: unknown) => void): () => void;
  emit(channel: string, data: unknown): void;
};

type RpcReply<T> = { success: true; data: T } | { success: false; error: string };

export async function spawnSubagent(
  events: EventBus,
  type: string,
  prompt: string,
  options: { model?: string; maxTurns?: number } = {},
  timeoutMs = 30_000,
  onSpawned?: (agentId: string) => void,
): Promise<string> {
  const requestId = randomUUID();
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      unsubscribe();
      reject(new Error("subagents:rpc:spawn timeout"));
    }, timeoutMs);
    const unsubscribe = events.on(`subagents:rpc:spawn:reply:${requestId}`, (raw) => {
      clearTimeout(timer);
      unsubscribe();
      const reply = raw as RpcReply<{ id: string }>;
      if (reply.success) {
        onSpawned?.(reply.data.id);
        resolve(reply.data.id);
      } else {
        reject(new Error(reply.error));
      }
    });
    events.emit("subagents:rpc:spawn", {
      requestId,
      type,
      prompt,
      options: {
        model: options.model,
        maxTurns: options.maxTurns,
      },
    });
  });
}
