import {
  normalizeAgentRun,
  safeAgentRunText,
  type AgentRun,
  type StartAgentRunInput,
  type UpdateAgentRunInput,
} from "@/domain/ai/session-progress";
import { canInvoke, invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import { listenResilientJSONStream } from "@/services/resilient-json-stream";
import { isStreamAvailable } from "@/services/desktop-stream";

export const AGENT_RUN_PROGRESS_EVENT = "agent-run:progress";
export const AGENT_RUN_CANCEL_REQUESTED_EVENT = "agent-run:cancel-requested";

const runUpdateQueues = new Map<string, Promise<unknown>>();

function requireAgentRun(value: unknown) {
  const run = normalizeAgentRun(value);
  if (!run) throw new Error("Invalid agent run payload from desktop backend");
  return run;
}

export async function startAgentRun(input: StartAgentRunInput) {
  const value = await invokeBackend<unknown>("agent_run_start", {
    ...input,
    title: safeAgentRunText(input.title, 120),
    summary: safeAgentRunText(input.summary ?? "", 240),
  });
  return requireAgentRun(value);
}

export function updateAgentRun(input: UpdateAgentRunInput) {
  const previous = runUpdateQueues.get(input.runId) ?? Promise.resolve();
  const operation = previous.catch(() => undefined).then(async () => {
    const value = await invokeBackend<unknown>("agent_run_update", {
      ...input,
      ...(input.summary !== undefined
        ? { summary: safeAgentRunText(input.summary, 240) }
        : {}),
    });
    return requireAgentRun(value);
  });
  runUpdateQueues.set(input.runId, operation);
  void operation.finally(() => {
    if (runUpdateQueues.get(input.runId) === operation) runUpdateQueues.delete(input.runId);
  }).catch(() => {});
  return operation;
}

export async function listAgentRuns() {
  if (!canInvoke("agent_run_list")) return [];
  const values = await invokeBackend<unknown[]>("agent_run_list", {});
  return values.map(normalizeAgentRun).filter((run): run is AgentRun => Boolean(run));
}

export async function requestAgentRunCancel(runId: string) {
  const value = await invokeBackend<unknown>("agent_run_request_cancel", { runId });
  return requireAgentRun(value);
}

export async function removeAgentRun(runId: string) {
  await invokeBackend<void>("agent_run_remove", { runId });
  runUpdateQueues.delete(runId);
}

export async function focusMainWindow() {
  if (!isDesktopRuntime()) return;
  await invokeBackend<void>("focus_main_window", {});
}

/**
 * Browser-local mode: the in-page run registry (services/browser-runtime/agent-runs) dispatches the
 * agent-run events as window CustomEvents with the Go payload as `detail`.
 */
function listenBrowserRunEvent(name: string, handler: (payload: unknown) => void) {
  const listener = (event: Event) => handler((event as CustomEvent<unknown>).detail);
  window.addEventListener(name, listener);
  return () => window.removeEventListener(name, listener);
}

export function listenAgentRunProgress(handler: (run: AgentRun) => void) {
  if (!isDesktopRuntime()) {
    return listenBrowserRunEvent(AGENT_RUN_PROGRESS_EVENT, (payload) => {
      const run = normalizeAgentRun(payload);
      if (run) handler(run);
    });
  }
  if (isStreamAvailable()) {
    // 每次订阅恢复后重新拉取完整快照，补齐断线期间遗漏的进度修订。
    let disposed = false;
    const stop = listenResilientJSONStream<unknown>({
      name: "agent-run-progress",
      isSubscribed: (value) => Boolean(
        value && typeof value === "object" && "subscribed" in value && value.subscribed,
      ),
      onMessage: (value) => {
        const run = normalizeAgentRun(value);
        if (run) handler(run);
      },
      onSubscribed: () => {
        void listAgentRuns().then((runs) => {
          if (!disposed) runs.forEach(handler);
        }).catch(() => undefined);
      },
    });
    return () => {
      disposed = true;
      stop();
    };
  }
  return listenAppEvent<unknown>(AGENT_RUN_PROGRESS_EVENT, (payload) => {
    const run = normalizeAgentRun(payload);
    if (run) handler(run);
  });
}

export function listenAgentRunCancelRequested(
  handler: (payload: { runId: string; sessionId: string }) => void,
) {
  const onPayload = (payload: unknown) => {
    if (!payload || typeof payload !== "object") return;
    const runId = "runId" in payload && typeof payload.runId === "string" ? payload.runId : "";
    const sessionId = "sessionId" in payload && typeof payload.sessionId === "string" ? payload.sessionId : "";
    if (runId && sessionId) handler({ runId, sessionId });
  };
  if (!isDesktopRuntime()) return listenBrowserRunEvent(AGENT_RUN_CANCEL_REQUESTED_EVENT, onPayload);
  return listenAppEvent<unknown>(AGENT_RUN_CANCEL_REQUESTED_EVENT, onPayload);
}
