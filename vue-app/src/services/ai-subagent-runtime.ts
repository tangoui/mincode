import type { AiCapabilityRegistry } from "@/domain/ai/capability-registry";
import type { AiResourceRegistry } from "@/domain/ai/resource-registry";
import {
  normalizeSubagentPlan,
  runSubagentPlan as scheduleSubagentPlan,
  type NormalizedSubagentTask,
  type SubagentPlanDefinition,
  type SubagentPlanSnapshot,
  type SubagentTaskExecutionResult,
} from "@/domain/ai/subagent-orchestration";
import type { AgentRun, AgentRunPhase } from "@/domain/ai/session-progress";
import type { AiTokenUsage } from "@/domain/ai/token-usage";
import {
  runAiLoop,
  type AiLoopConfig,
  type AiLoopMessage,
  type AiLoopToolCall,
  type AiLoopToolExecution,
} from "@/services/ai-agent-loop";
import { streamAiTurnWithFailover } from "@/services/ai-model-failover";

interface SubagentWorkspaceContext {
  workspaceId: string;
  workspaceScopeId: string;
  projectRoot: string;
}

export interface SubagentRuntimeDependencies {
  capabilities: AiCapabilityRegistry;
  config: AiLoopConfig;
  models: string[];
  conversationId: string;
  parentRunId: string;
  workspace: SubagentWorkspaceContext;
  signal: AbortSignal;
  invoke<T>(command: string, args: Record<string, unknown>): Promise<T>;
  createResources(projectRoot: string, name?: string): AiResourceRegistry;
  executeTool(
    call: AiLoopToolCall,
    resources: AiResourceRegistry,
    projectRoot: string,
    signal: AbortSignal,
    allowedTools: ReadonlySet<string>,
  ): Promise<AiLoopToolExecution>;
  startRun(input: {
    parentRunId: string;
    taskId: string;
    branch?: string;
    worktreeName?: string;
    sessionId: string;
    workspaceId: string;
    workspaceScopeId: string;
    source: "agentic";
    title: string;
    phase: "thinking";
    summary: string;
    navigation: { type: "ai-conversation"; conversationId: string };
  }): Promise<AgentRun>;
  updateRun(input: {
    runId: string;
    phase: AgentRunPhase;
    summary?: string;
    errorCode?: string;
  }): Promise<AgentRun>;
  applyRun(run: AgentRun): void;
  conversationForTask?(task: NormalizedSubagentTask): string | undefined;
  taskSignal?(task: NormalizedSubagentTask): AbortSignal | undefined;
  requestForTask?(task: NormalizedSubagentTask): { config: AiLoopConfig; models: string[] } | undefined;
  onTaskTurn?(conversationId: string, message: AiLoopMessage): void;
  onPlanUpdate?(snapshot: SubagentPlanSnapshot): void;
  toolProgress?(name: string): string;
  errorMessage?(error: unknown): string;
  onUsage?(usage: AiTokenUsage): void;
  onModelAttempt?(model: string): void;
  /** Builds a fresh per-request reminder source for each subagent loop (see RunAiLoopOptions.systemReminder). */
  createSystemReminder?(): () => string | undefined;
}

const NO_TOOLS = new Set<string>();

export const SUBAGENT_MODEL_IDLE_TIMEOUT_MS = 90_000;

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

function throwIfExecutionAborted(signal: AbortSignal) {
  if (signal.aborted) {
    throw signal.reason ?? new DOMException("Subagent execution cancelled", "AbortError");
  }
}

function dependencyContext(dependencies: ReadonlyMap<string, SubagentTaskExecutionResult>) {
  if (!dependencies.size) return "None.";
  return [...dependencies.entries()].map(([id, result]) => {
    const revision = result.commitHash ? ` commit=${result.commitHash}` : "";
    const branch = result.branch ? ` branch=${result.branch}` : "";
    return `- ${id}:${branch}${revision}\n${result.summary.slice(0, 2_000)}`;
  }).join("\n");
}

function systemPrompt() {
  return "You are a MinCode subagent completing a delegated task. Work from the task prompt and the context supplied there, then write a final answer that the parent conversation can use. You have no tools in this conversation.";
}

function taskTools(dependencies: SubagentRuntimeDependencies, allowed: ReadonlySet<string>) {
  return dependencies.capabilities.listCapabilities()
    .filter((capability) => allowed.has(capability.tool.name))
    .map((capability) => ({ ...capability.tool }));
}

async function report(
  dependencies: SubagentRuntimeDependencies,
  runId: string,
  phase: AgentRunPhase,
  summary: string,
  errorCode?: string,
) {
  const run = await dependencies.updateRun({ runId, phase, summary, errorCode });
  dependencies.applyRun(run);
}

export async function executeSubagentPlan(
  definition: SubagentPlanDefinition,
  dependencies: SubagentRuntimeDependencies,
) {
  const plan = normalizeSubagentPlan(definition);
  if (plan.tasks.some((task) => task.mode === "write")) {
    throw new Error("Subagent tasks support read mode.");
  }
  const snapshot = await scheduleSubagentPlan(plan, {
    onUpdate: (current) => dependencies.onPlanUpdate?.(current),
    taskSignal: dependencies.taskSignal,
    execute: async (task, execution) => {
      throwIfExecutionAborted(execution.signal);
      const request = dependencies.requestForTask?.(task) ?? dependencies;
      const projectRoot = "";
      const resources = dependencies.createResources(projectRoot);
      const taskConversationId = dependencies.conversationForTask?.(task);
      const child = await dependencies.startRun({
        parentRunId: dependencies.parentRunId,
        taskId: task.id,
        sessionId: `${dependencies.conversationId}:subagent:${task.id}:${execution.attempt}`,
        workspaceId: dependencies.workspace.workspaceId,
        workspaceScopeId: dependencies.workspace.workspaceScopeId,
        source: "agentic",
        title: task.title,
        phase: "thinking",
        summary: `Subagent attempt ${execution.attempt} started`,
        navigation: { type: "ai-conversation", conversationId: taskConversationId ?? dependencies.conversationId },
      });
      dependencies.applyRun(child);
      const allowed = NO_TOOLS;
      const messages: AiLoopMessage[] = [
        { role: "system", content: systemPrompt() },
        {
          role: "user",
          content: `${task.prompt}\n\nCompleted dependency results:\n${dependencyContext(execution.completedDependencies)}`,
        },
      ];
      try {
        throwIfExecutionAborted(execution.signal);
        const result = await runAiLoop({
          config: request.config,
          messages,
          tools: taskTools(dependencies, allowed),
          maxLoops: 30,
          signal: execution.signal,
          systemReminder: dependencies.createSystemReminder?.(),
          canExecuteToolInParallel: (call) =>
            dependencies.capabilities.get(call.name)?.risk === "read",
          executeTool: (call, signal) =>
            dependencies.executeTool(call, resources, projectRoot, signal, allowed),
          streamTurn: (config, history, tools, onEvent, signal) =>
            streamAiTurnWithFailover({
              config,
              models: request.models,
              messages: history,
              tools,
              onEvent,
              signal,
              idleTimeoutMs: SUBAGENT_MODEL_IDLE_TIMEOUT_MS,
              onAttempt: (model) => {
                dependencies.onModelAttempt?.(model);
                void report(
                  dependencies,
                  child.runId,
                  "thinking",
                  `Waiting for model ${model}`,
                ).catch(() => undefined);
              },
            }),
          onUsage: dependencies.onUsage,
          onTurnComplete: (message) => {
            if (taskConversationId && !execution.signal.aborted) dependencies.onTaskTurn?.(taskConversationId, message);
          },
          onToolCall: (call) => {
            if (call.name !== "task_complete") {
              void report(
                dependencies,
                child.runId,
                "tool_running",
                dependencies.toolProgress?.(call.name) ?? call.name,
              ).catch(() => undefined);
            }
          },
          onToolResult: () => {
            void report(
              dependencies,
              child.runId,
              "thinking",
              "Reviewing subagent results",
            ).catch(() => undefined);
          },
        });
        // A wrap-up summary does not complete the task; it only tells the parent what was done.
        if (result.outcome === "loop_limit") {
          throw new Error(`Subagent reached its loop limit before completion.\n\n${result.summary}`);
        }
        const directReadAnswer = task.mode === "read" && result.outcome === "response"
          && result.usedTools === false && Boolean(result.summary.trim());
        if (result.outcome !== "task_completed" && !directReadAnswer) {
          throw new Error("Subagent must finish with task_complete after using the required tools");
        }
        const taskResult: SubagentTaskExecutionResult = { summary: result.summary };
        await report(dependencies, child.runId, "completed", taskResult.summary || "Subagent completed");
        return taskResult;
      } catch (error) {
        const cancelled = execution.signal.aborted;
        await report(
          dependencies,
          child.runId,
          cancelled ? "cancelled" : "failed",
          cancelled ? "Subagent cancelled" : (dependencies.errorMessage?.(error) ?? errorText(error)),
          cancelled ? undefined : "SUBAGENT_EXECUTION_FAILED",
        ).catch(() => undefined);
        throw error;
      }
    },
  }, dependencies.signal);

  return {
    title: snapshot.title,
    state: snapshot.state,
    tasks: snapshot.tasks.map((task) => ({
      id: task.id,
      title: task.title,
      state: task.state,
      attempts: task.attempts,
      summary: task.result?.summary,
      branch: task.result?.branch,
      commitHash: task.result?.commitHash,
      changedFiles: task.result?.changedFiles,
      verification: task.result?.verification,
      baseCommit: typeof task.result?.metadata?.baseCommit === "string"
        ? task.result.metadata.baseCommit
        : undefined,
      error: task.error,
    })),
    integrationRequired: snapshot.tasks.some((task) => task.result?.commitHash),
  };
}
