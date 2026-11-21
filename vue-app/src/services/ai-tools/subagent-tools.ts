import type { AiCapability } from "@/domain/ai/capability-registry";
import type { SubagentPlanDefinition } from "@/domain/ai/subagent-orchestration";

export interface SubagentCapabilityDependencies {
  runPlan(
    plan: SubagentPlanDefinition,
    context: { conversationId?: string; signal: AbortSignal },
  ): Promise<unknown>;
}

export function subagentCapabilities(
  dependencies: SubagentCapabilityDependencies,
): AiCapability[] {
  return [{
    module: "agent",
    discovery: true,
    target: "none",
    mode: "write",
    risk: "dangerous",
    tool: {
      name: "agent.run_parallel",
      description:
        "Delegate 1-16 tasks to separate, user-visible child conversations and return their results to this conversation. When the user asks to hand off one task to a new conversation, call this tool with exactly one task. Include the task and relevant context in its prompt so the child can work independently. Use read mode for analysis or drafting from supplied context. For multiple tasks, use independent or dependency-ordered scopes where separate execution helps. Ready tasks are scheduled automatically without a fixed concurrency cap or shared slots across conversations. Subagents cannot create other agents.",
      inputSchema: {
        type: "object",
        properties: {
          title: { type: "string", description: "Short title for the complete parallel plan." },
          maxConcurrency: {
            type: "integer",
            minimum: 1,
            description: "Optional concurrency limit for this plan only. Omit to automatically run all ready tasks with no resource conflicts; there is no fixed concurrency cap or shared pool across conversations.",
          },
          failFast: {
            type: "boolean",
            description: "Cancel or skip remaining tasks after the first terminal failure.",
          },
          tasks: {
            type: "array",
            minItems: 1,
            maxItems: 16,
            items: {
              type: "object",
              properties: {
                id: {
                  type: "string",
                  pattern: "^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$",
                  description: "Stable task identifier used by dependencies.",
                },
                title: { type: "string", description: "Short user-visible task title." },
                prompt: {
                  type: "string",
                  description: "Self-contained implementation or investigation instructions with acceptance criteria and non-overlapping scope.",
                },
                mode: {
                  type: "string",
                  enum: ["read"],
                  description: "Read performs analysis using the supplied context.",
                },
                model: {
                  type: "string",
                  description: "Optional configured model ID from the supplied delegation model list, in the form profileId::model. Pick per task when appropriate or follow the user's requested model. Omit to use the default for this conversation.",
                },
                dependsOn: {
                  type: "array",
                  items: { type: "string" },
                  uniqueItems: true,
                  description: "Task IDs that must succeed first.",
                },
                resourceKeys: {
                  type: "array",
                  items: { type: "string" },
                  uniqueItems: true,
                  description: "Exclusive logical resources within this plan. Tasks sharing a key are serialized; keys are not locks across conversations.",
                },
                timeoutMs: {
                  type: "integer",
                  minimum: 1000,
                  maximum: 3600000,
                  description: "Per-attempt timeout; defaults to 20 minutes.",
                },
                maxAttempts: {
                  type: "integer",
                  minimum: 1,
                  maximum: 3,
                  description: "Total attempts including the first; defaults to 1.",
                },
              },
              required: ["id", "title", "prompt"],
              additionalProperties: false,
            },
          },
        },
        required: ["title", "tasks"],
        additionalProperties: false,
      },
    },
    approvalPreview: ({ input }) => {
      const count = Array.isArray(input.tasks) ? input.tasks.length : 0;
      if (count === 1) return "Delegate one task to a new conversation";
      return `Run ${count} isolated subagent tasks in parallel`;
    },
    // A plan made only of read tasks is itself read-only, so plan mode can let it
    // through; anything else (including an empty or malformed plan) stays dangerous.
    resolveRisk: ({ input }) =>
      Array.isArray(input.tasks)
        && input.tasks.length > 0
        && input.tasks.every((task) => (task as { mode?: unknown } | null)?.mode === "read")
        ? "read"
        : "dangerous",
    execute: ({ input, signal, conversationId }) =>
      dependencies.runPlan(input as unknown as SubagentPlanDefinition, {
        conversationId,
        signal,
      }),
  }];
}
