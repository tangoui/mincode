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

interface AgentWorktreeRuntime {
  path: string;
  projectPath: string;
  name: string;
  branch: string;
  baseCommit: string;
}

interface AgentWorktreeInspection {
  name: string;
  branch: string;
  head: string;
  clean: boolean;
  commitsAhead: number;
  changedFiles: string[];
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

const READ_TOOLS = new Set([
  "workspace.list_files",
  "workspace.read_file",
  "workspace.search",
  "workspace.search_code",
  "workspace.regex_search",
  "workspace.list_all_files",
  "workspace.is_binary",
  "workspace.stat",
  "git.status",
  "git.log",
  "git.diff",
  "git.branches",
  "git.tags",
  "git.show_commit",
  "git.commit_file_diff",
  "git.blame",
  "git.conflict_list",
  "git.conflict_read",
  "git.read_file_at_ref",
  "git.staged_patch",
]);
const NO_TOOLS = new Set<string>();

export const SUBAGENT_MODEL_IDLE_TIMEOUT_MS = 90_000;

const WRITE_TOOLS = new Set([
  ...READ_TOOLS,
  "workspace.write_file",
  "workspace.replace_preview",
  "workspace.replace_apply",
  "workspace.create_directory",
  "workspace.rename_entry",
  "workspace.delete_entry",
  "workspace.copy_entry",
  "workspace.move_entry",
  "git.stage",
  "git.unstage",
  "git.stage_all",
  "git.unstage_all",
  "git.discard",
  "git.discard_all",
  "git.commit",
  "git.amend",
]);

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

function systemPrompt(mode: NormalizedSubagentTask["mode"], projectRoot: string) {
  if (mode === "read" && !projectRoot) {
    return "You are a MinCode subagent completing a delegated task. Work from the task prompt and the context supplied there, then write a final answer that the parent conversation can use. You have no tools in this conversation.";
  }
  return mode === "write"
    ? "You are an isolated MinCode coding subagent. Work only through the provided workspace and Git tools in a dedicated Git worktree. Never access external systems, execute arbitrary shell commands, or create other agents. Make only the requested scoped changes. Review your diff, stage it, create exactly one non-empty commit relative to the provided baseline (amend that commit if corrections are needed), then call task_complete exactly once with concrete changes, evidence, risks, and validation still required by the parent. Never claim a command or test ran unless a tool result proves it."
    : "You are an isolated MinCode analysis subagent. Use only the provided read-only workspace and Git tools. Do not modify files, access external systems, execute commands, or create other agents. Inspect the requested scope, cite concrete file paths and symbols, and call task_complete exactly once with findings, evidence, risks, and unanswered questions.";
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
  const worktrees = new Map<string, AgentWorktreeRuntime>();
  const completedResults = new Map<string, SubagentTaskExecutionResult>();
  let planBaseCommit = "";
  let worktreeCreationQueue = Promise.resolve();
  const createWorktree = (taskId: string, inheritedCommit?: string) => {
    let created!: AgentWorktreeRuntime;
    const operation = worktreeCreationQueue.then(async () => {
      created = await dependencies.invoke<AgentWorktreeRuntime>("git_create_agent_worktree", {
        repoPath: dependencies.workspace.projectRoot,
        taskId,
        baseCommit: inheritedCommit || planBaseCommit || undefined,
      });
      planBaseCommit ||= created.baseCommit;
    });
    worktreeCreationQueue = operation.catch(() => undefined);
    return operation.then(() => created);
  };
  const snapshot = await scheduleSubagentPlan(plan, {
    onUpdate: (current) => dependencies.onPlanUpdate?.(current),
    taskSignal: dependencies.taskSignal,
    execute: async (task, execution) => {
      throwIfExecutionAborted(execution.signal);
      const request = dependencies.requestForTask?.(task) ?? dependencies;
      const inheritedTaskId = task.inheritedWriteTaskId;
      const inheritedWorktree = inheritedTaskId ? worktrees.get(inheritedTaskId) : undefined;
      const inheritedResult = inheritedTaskId ? completedResults.get(inheritedTaskId) : undefined;
      if (inheritedTaskId && (!inheritedWorktree || !inheritedResult?.commitHash)) {
        throw new Error(`Task ${task.id} cannot resolve inherited write baseline ${inheritedTaskId}`);
      }
      let worktree = worktrees.get(task.id);
      if (task.mode === "write" && !worktree) {
        worktree = await createWorktree(task.id, inheritedResult?.commitHash);
        throwIfExecutionAborted(execution.signal);
        worktrees.set(task.id, worktree);
      }
      const activeWorktree = worktree ?? inheritedWorktree;
      const projectRoot = activeWorktree?.projectPath ?? dependencies.workspace.projectRoot;
      const resources = dependencies.createResources(projectRoot, activeWorktree?.name);
      throwIfExecutionAborted(execution.signal);
      const taskConversationId = dependencies.conversationForTask?.(task);
      const child = await dependencies.startRun({
        parentRunId: dependencies.parentRunId,
        taskId: task.id,
        branch: activeWorktree?.branch,
        worktreeName: activeWorktree?.name,
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
      const allowed = !projectRoot ? NO_TOOLS : task.mode === "write" ? WRITE_TOOLS : READ_TOOLS;
      const messages: AiLoopMessage[] = [
        { role: "system", content: systemPrompt(task.mode, projectRoot) },
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
        let taskResult: SubagentTaskExecutionResult = { summary: result.summary };
        if (task.mode === "write") {
          const inspection = await dependencies.invoke<AgentWorktreeInspection>(
            "git_inspect_agent_worktree",
            {
              repoPath: dependencies.workspace.projectRoot,
              worktreePath: worktree!.path,
              baseCommit: worktree!.baseCommit,
            },
          );
          if (!inspection.clean) {
            throw new Error("Subagent left uncommitted changes in its worktree");
          }
          if (inspection.commitsAhead !== 1 || inspection.changedFiles.length < 1) {
            throw new Error(
              "Subagent must create exactly one non-empty commit relative to its baseline",
            );
          }
          taskResult = {
            summary: result.summary,
            branch: inspection.branch,
            commitHash: inspection.head,
            changedFiles: inspection.changedFiles,
            verification: ["Backend verified a clean worktree and a non-empty committed diff."],
            metadata: { baseCommit: worktree!.baseCommit },
          };
        }
        completedResults.set(task.id, taskResult);
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
