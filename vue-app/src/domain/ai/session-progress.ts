export const AGENT_RUN_PHASES = [
  "queued",
  "thinking",
  "streaming",
  "tool_running",
  "waiting_approval",
  "waiting_input",
  "cancelling",
  "completed",
  "failed",
  "cancelled",
] as const;

export type AgentRunPhase = (typeof AGENT_RUN_PHASES)[number];
export type AgentRunSource = "agentic" | "acp";

export interface AiConversationRunNavigation {
  type: "ai-conversation";
  conversationId: string;
}

export interface ExternalSessionRunNavigation {
  type: "session";
  provider: string;
  sessionId: string;
}

export type AgentRunNavigation = AiConversationRunNavigation | ExternalSessionRunNavigation;

export interface SessionProgress {
  phase: AgentRunPhase;
  summary: string;
  createdAt: number;
  updatedAt: number;
  completedAt?: number;
  errorCode?: string;
  approvalId?: string;
  cancelRequested?: boolean;
  stepsCompleted?: number;
  stepsTotal?: number;
}

export interface AgentRun {
  runId: string;
  parentRunId?: string;
  taskId?: string;
  branch?: string;
  worktreeName?: string;
  sessionId: string;
  workspaceId: string;
  workspaceScopeId: string;
  source: AgentRunSource;
  title: string;
  revision: number;
  navigation: AgentRunNavigation;
  progress: SessionProgress;
}

export interface StartAgentRunInput {
  parentRunId?: string;
  taskId?: string;
  branch?: string;
  worktreeName?: string;
  sessionId: string;
  workspaceId: string;
  workspaceScopeId: string;
  source: AgentRunSource;
  title: string;
  phase?: Exclude<AgentRunPhase, "cancelling" | "completed" | "failed" | "cancelled">;
  summary?: string;
  navigation: AgentRunNavigation;
}

export interface UpdateAgentRunInput {
  runId: string;
  phase: AgentRunPhase;
  summary?: string;
  errorCode?: string;
  approvalId?: string;
  stepsCompleted?: number;
  stepsTotal?: number;
}

const phaseSet = new Set<string>(AGENT_RUN_PHASES);
const terminalPhases = new Set<AgentRunPhase>(["completed", "failed", "cancelled"]);
const attentionPhases = new Set<AgentRunPhase>(["waiting_approval", "waiting_input", "failed"]);

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" ? value as Record<string, unknown> : undefined;
}

function text(value: unknown) {
  return typeof value === "string" ? value : "";
}

function finiteNumber(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

export function normalizeAgentRun(value: unknown): AgentRun | undefined {
  const raw = record(value);
  const rawProgress = record(raw?.progress);
  const rawNavigation = record(raw?.navigation);
  if (!raw || !rawProgress || !rawNavigation) return undefined;

  const phase = text(rawProgress.phase);
  const source = text(raw.source);
  const navigationType = text(rawNavigation.type);
  if (!phaseSet.has(phase) || (source !== "agentic" && source !== "acp")) return undefined;

  let navigation: AgentRunNavigation;
  if (navigationType === "ai-conversation" && text(rawNavigation.conversationId)) {
    navigation = { type: navigationType, conversationId: text(rawNavigation.conversationId) };
  } else if (
    navigationType === "session"
    && text(rawNavigation.provider)
    && text(rawNavigation.sessionId)
  ) {
    navigation = {
      type: navigationType,
      provider: text(rawNavigation.provider),
      sessionId: text(rawNavigation.sessionId),
    };
  } else {
    return undefined;
  }

  const run: AgentRun = {
    runId: text(raw.runId),
    ...(text(raw.parentRunId) ? { parentRunId: text(raw.parentRunId) } : {}),
    ...(text(raw.taskId) ? { taskId: text(raw.taskId) } : {}),
    ...(text(raw.branch) ? { branch: text(raw.branch) } : {}),
    ...(text(raw.worktreeName) ? { worktreeName: text(raw.worktreeName) } : {}),
    sessionId: text(raw.sessionId),
    workspaceId: text(raw.workspaceId),
    workspaceScopeId: text(raw.workspaceScopeId),
    source,
    title: text(raw.title),
    revision: finiteNumber(raw.revision),
    navigation,
    progress: {
      phase: phase as AgentRunPhase,
      summary: text(rawProgress.summary),
      createdAt: finiteNumber(rawProgress.createdAt),
      updatedAt: finiteNumber(rawProgress.updatedAt),
      ...(finiteNumber(rawProgress.completedAt) > 0
        ? { completedAt: finiteNumber(rawProgress.completedAt) }
        : {}),
      ...(text(rawProgress.errorCode) ? { errorCode: text(rawProgress.errorCode) } : {}),
      ...(text(rawProgress.approvalId) ? { approvalId: text(rawProgress.approvalId) } : {}),
      ...(rawProgress.cancelRequested === true ? { cancelRequested: true } : {}),
      ...(finiteNumber(rawProgress.stepsTotal) > 0
        ? {
          stepsCompleted: Math.min(
            Math.max(finiteNumber(rawProgress.stepsCompleted), 0),
            finiteNumber(rawProgress.stepsTotal),
          ),
          stepsTotal: finiteNumber(rawProgress.stepsTotal),
        }
        : {}),
    },
  };
  if (
    !run.runId
    || !run.sessionId
    || !run.workspaceId
    || !run.workspaceScopeId
    || run.revision < 1
    || run.progress.createdAt < 1
    || run.progress.updatedAt < 1
  ) return undefined;
  return run;
}

export function shouldApplyAgentRun(current: AgentRun | undefined, incoming: AgentRun) {
  if (!current) return true;
  return incoming.revision > current.revision
    || (incoming.revision === current.revision && incoming.progress.updatedAt > current.progress.updatedAt);
}

export function isTerminalAgentRun(run: AgentRun) {
  return terminalPhases.has(run.progress.phase);
}

export function isActiveAgentRun(run: AgentRun) {
  return !isTerminalAgentRun(run);
}

export function needsAgentRunAttention(run: AgentRun) {
  return attentionPhases.has(run.progress.phase);
}

/**
 * Finished runs that no view needs any more: active and attention runs are always kept,
 * and only the newest `maxFinished` quiet finished runs survive.
 */
export function prunableAgentRunIds(runs: Iterable<AgentRun>, maxFinished: number) {
  const finished: AgentRun[] = [];
  for (const run of runs) {
    if (isTerminalAgentRun(run) && !needsAgentRunAttention(run)) finished.push(run);
  }
  if (finished.length <= maxFinished) return [];
  return finished
    .sort((a, b) => b.progress.updatedAt - a.progress.updatedAt)
    .slice(Math.max(0, maxFinished))
    .map((run) => run.runId);
}

/** Redacts common credential shapes before text enters the native progress registry. */
export function safeAgentRunText(value: string, maxLength = 160) {
  const redacted = value
    .replace(/\s+/g, " ")
    .trim()
    .replace(/(api[\s_-]*key\s*[=:]\s*)([^\s,;"'}]+)/gi, "$1[REDACTED]")
    .replace(/(authorization\s*[=:]\s*bearer\s+)([^\s,;"'}]+)/gi, "$1[REDACTED]")
    .replace(/(bearer\s+)([a-z0-9._~+/=-]{12,})/gi, "$1[REDACTED]")
    .replace(/((?:access|refresh|session)[\s_-]*token\s*[=:]\s*)([^\s,;"'}]+)/gi, "$1[REDACTED]");
  if (redacted.length <= maxLength) return redacted;
  return `${redacted.slice(0, Math.max(0, maxLength - 1))}…`;
}

/** Whole-number completion percentage for runs that report countable steps. */
export function agentRunStepPercent(progress: SessionProgress): number | undefined {
  if (!progress.stepsTotal) return undefined;
  return Math.round(((progress.stepsCompleted ?? 0) / progress.stepsTotal) * 100);
}
