/**
 * 浏览器本地模式的 AI 运行登记：`agent_run_*` 五个命令的内存实现，代替
 * backend/agentprogress（同样只在内存中，刷新页面即清空）。
 *
 * 助手抽屉用它登记每轮对话（会话的「运行中」状态、停止按钮、任务列表都由运行记录投影而来）。
 * 输入输出形状、校验与状态迁移与 Go 一致；Go 经 后端事件广播的进度与「请求停止」，
 * 这里改为 window 上的同名 CustomEvent（session-progress-service 在浏览器本地模式下监听）。
 */
import type { AgentRun, AgentRunPhase } from "@/domain/ai/session-progress";
import { AGENT_RUN_CANCEL_REQUESTED_EVENT, AGENT_RUN_PROGRESS_EVENT } from "@/services/session-progress-service";
import { registerBrowserHandler } from "./registry";

const COMPLETED_RETENTION_MS = 5 * 60_000;
const FAILED_RETENTION_MS = 30 * 60_000;
const CANCEL_SETTLE_DELAY_MS = 10_000;
const MAX_RETAINED_RUNS = 200;

const PHASES = new Set<string>([
  "queued", "thinking", "streaming", "tool_running", "waiting_approval",
  "waiting_input", "cancelling", "completed", "failed", "cancelled",
]);

const SENSITIVE_VALUE_PATTERNS = [
  /(api[\s_-]*key\s*[=:]\s*)([^\s,;"'}]+)/gi,
  /(authorization\s*[=:]\s*bearer\s+)([^\s,;"'}]+)/gi,
  /(bearer\s+)([a-z0-9._~+/=-]{12,})/gi,
  /((?:access|refresh|session)[\s_-]*token\s*[=:]\s*)([^\s,;"'}]+)/gi,
];

const runs = new Map<string, AgentRun>();
/** Pending "settle as cancelled" timers of cancel requests, cleared once the run finishes. */
const settleTimers = new Map<string, ReturnType<typeof setTimeout>>();

function clearSettleTimer(runId: string) {
  clearTimeout(settleTimers.get(runId));
  settleTimers.delete(runId);
}

function text(value: unknown) {
  return typeof value === "string" ? value : "";
}

function isTerminal(phase: string) {
  return phase === "completed" || phase === "failed" || phase === "cancelled";
}

/** Go safeText: drops control characters, collapses whitespace, redacts secrets, caps the length. */
function safeText(value: unknown, maxRunes: number) {
  // eslint-disable-next-line no-control-regex -- strips control characters like Go's unicode.IsControl.
  let result = text(value).replace(/[\u0000-\u0008\u000e-\u001f\u007f-\u0084\u0086-\u009f]/g, "");
  result = result.split(/\s+/).filter(Boolean).join(" ");
  for (const pattern of SENSITIVE_VALUE_PATTERNS) result = result.replace(pattern, "$1[REDACTED]");
  const chars = Array.from(result);
  return chars.length <= maxRunes ? result : `${chars.slice(0, maxRunes - 1).join("")}…`;
}

function publish(run: AgentRun) {
  window.dispatchEvent(new CustomEvent(AGENT_RUN_PROGRESS_EVENT, { detail: structuredClone(run) }));
}

function prune(now: number) {
  for (const [runId, run] of runs) {
    if (!isTerminal(run.progress.phase)) continue;
    const terminalAt = run.progress.completedAt ?? run.progress.updatedAt;
    const retention = run.progress.phase === "failed" ? FAILED_RETENTION_MS : COMPLETED_RETENTION_MS;
    if (now - terminalAt > retention) runs.delete(runId);
  }
  const overflow = runs.size - MAX_RETAINED_RUNS;
  if (overflow <= 0) return;
  [...runs.values()]
    .filter((run) => isTerminal(run.progress.phase))
    .sort((a, b) => a.progress.updatedAt - b.progress.updatedAt)
    .slice(0, overflow)
    .forEach((run) => runs.delete(run.runId));
}

function applyUpdate(run: AgentRun, phase: AgentRunPhase, changes: { summary?: unknown; errorCode?: unknown; approvalId?: unknown } = {}) {
  const now = Math.max(Date.now(), run.progress.updatedAt + 1);
  const progress = { ...run.progress, phase, updatedAt: now };
  if (phase === "cancelling") progress.cancelRequested = true;
  if (changes.summary !== undefined) progress.summary = safeText(changes.summary, 240);
  if (changes.errorCode !== undefined) progress.errorCode = safeText(changes.errorCode, 80) || undefined;
  if (changes.approvalId !== undefined) progress.approvalId = safeText(changes.approvalId, 120) || undefined;
  if (phase !== "waiting_approval") delete progress.approvalId;
  if (isTerminal(phase)) progress.completedAt = now;
  return { ...run, revision: run.revision + 1, progress };
}

function requireRun(runId: string) {
  const run = runs.get(runId);
  if (!run) throw new Error(`agent run not found: "${runId}"`);
  return run;
}

async function startRun(args: Record<string, unknown>): Promise<AgentRun> {
  const sessionId = text(args.sessionId).trim();
  const workspaceId = text(args.workspaceId).trim();
  const workspaceScopeId = text(args.workspaceScopeId).trim();
  const source = text(args.source).trim();
  const parentRunId = text(args.parentRunId).trim();
  const taskId = text(args.taskId).trim();
  const branch = text(args.branch).trim();
  const worktreeName = text(args.worktreeName).trim();
  if (!sessionId || !workspaceId || !workspaceScopeId) throw new Error("sessionId, workspaceId and workspaceScopeId are required");
  if (source !== "agentic" && source !== "acp") throw new Error(`unsupported agent run source: "${source}"`);
  const phase = text(args.phase).trim() || "queued";
  if (!PHASES.has(phase) || isTerminal(phase) || phase === "cancelling") throw new Error(`invalid initial agent run phase: "${phase}"`);
  const navigation = (args.navigation && typeof args.navigation === "object" ? args.navigation : {}) as Record<string, unknown>;
  const navigationType = text(navigation.type).trim();
  let runNavigation: AgentRun["navigation"];
  if (navigationType === "ai-conversation") {
    if (!text(navigation.conversationId).trim()) throw new Error("conversationId is required for ai-conversation navigation");
    runNavigation = { type: "ai-conversation", conversationId: safeText(navigation.conversationId, 160) };
  } else if (navigationType === "session") {
    if (!text(navigation.provider).trim() || !text(navigation.sessionId).trim()) {
      throw new Error("provider and sessionId are required for session navigation");
    }
    runNavigation = { type: "session", provider: safeText(navigation.provider, 40), sessionId: safeText(navigation.sessionId, 240) };
  } else {
    throw new Error(`unsupported agent run navigation type: "${navigationType}"`);
  }
  if (!parentRunId && (taskId || branch || worktreeName)) throw new Error("subagent metadata requires parentRunId");
  if (parentRunId && !taskId) throw new Error("subagent run requires taskId");

  const now = Date.now();
  const run: AgentRun = {
    runId: `run-${crypto.randomUUID().replace(/-/g, "")}`,
    ...(parentRunId ? { parentRunId: safeText(parentRunId, 120) } : {}),
    ...(taskId ? { taskId: safeText(taskId, 64) } : {}),
    ...(branch ? { branch: safeText(branch, 200) } : {}),
    ...(worktreeName ? { worktreeName: safeText(worktreeName, 120) } : {}),
    sessionId,
    workspaceId,
    workspaceScopeId,
    source,
    title: safeText(args.title, 120) || "AI task",
    revision: 1,
    navigation: runNavigation,
    progress: { phase: phase as AgentRunPhase, summary: safeText(args.summary, 240), createdAt: now, updatedAt: now },
  };
  prune(now);
  runs.set(run.runId, run);
  publish(run);
  return structuredClone(run);
}

async function updateRun(args: Record<string, unknown>): Promise<AgentRun> {
  const runId = text(args.runId).trim();
  const phase = text(args.phase).trim();
  if (!runId || !PHASES.has(phase)) throw new Error("runId and a valid phase are required");
  const run = requireRun(runId);
  if (isTerminal(run.progress.phase)) return structuredClone(run);
  if (run.progress.phase === "cancelling" && phase !== "cancelling" && !isTerminal(phase)) return structuredClone(run);
  const updated = applyUpdate(run, phase as AgentRunPhase, args);
  const stepsTotal = typeof args.stepsTotal === "number" ? args.stepsTotal : 0;
  const stepsCompleted = typeof args.stepsCompleted === "number" ? args.stepsCompleted : 0;
  // Steps are replaced on every update; omitting them clears countable progress.
  if (stepsTotal > 0 && stepsCompleted >= 0) {
    updated.progress.stepsTotal = stepsTotal;
    updated.progress.stepsCompleted = Math.min(stepsCompleted, stepsTotal);
  } else {
    delete updated.progress.stepsTotal;
    delete updated.progress.stepsCompleted;
  }
  runs.set(runId, updated);
  if (isTerminal(phase)) clearSettleTimer(runId);
  publish(updated);
  return structuredClone(updated);
}

async function listRuns(): Promise<AgentRun[]> {
  prune(Date.now());
  return [...runs.values()].sort((a, b) => b.progress.updatedAt - a.progress.updatedAt).map((run) => structuredClone(run));
}

/** Go RequestCancel: the owner gets the cancel request; the run settles as cancelled if nothing reports back. */
async function requestCancel(args: Record<string, unknown>): Promise<AgentRun> {
  const runId = text(args.runId).trim();
  const run = requireRun(runId);
  if (isTerminal(run.progress.phase) || run.progress.cancelRequested) return structuredClone(run);
  const updated = applyUpdate({ ...run, progress: { ...run.progress, cancelRequested: true } }, "cancelling");
  runs.set(runId, updated);
  publish(updated);
  window.dispatchEvent(new CustomEvent(AGENT_RUN_CANCEL_REQUESTED_EVENT, { detail: { runId, sessionId: updated.sessionId } }));
  settleTimers.set(runId, setTimeout(() => {
    settleTimers.delete(runId);
    const current = runs.get(runId);
    if (current?.progress.phase !== "cancelling") return;
    const settled = applyUpdate(current, "cancelled");
    runs.set(runId, settled);
    publish(settled);
  }, CANCEL_SETTLE_DELAY_MS));
  return structuredClone(updated);
}

async function removeRun(args: Record<string, unknown>): Promise<null> {
  const runId = text(args.runId).trim();
  if (!runId) throw new Error("runId is required");
  const run = requireRun(runId);
  if (!isTerminal(run.progress.phase)) throw new Error(`active agent run cannot be removed: "${runId}"`);
  runs.delete(runId);
  return null;
}

registerBrowserHandler("agent_run_start", startRun);
registerBrowserHandler("agent_run_update", updateRun);
registerBrowserHandler("agent_run_list", listRuns);
registerBrowserHandler("agent_run_request_cancel", requestCancel);
registerBrowserHandler("agent_run_remove", removeRun);
