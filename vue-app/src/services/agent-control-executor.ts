import {
  type AiCapabilityExecutionDependencies,
  type AiCapabilityExecutionResult,
  type AiCapabilityRegistry,
} from "@/domain/ai/capability-registry";
import { createAbortError, runWithAbort, throwIfAborted } from "@/domain/async/abort";
import { redactSensitiveText, sanitizeAiResult } from "@/services/ai-tools/shared";

const excludedTools = new Set(["ask_user", "agent.run_parallel", "agent.integrate_result"]);
const visualTools = new Set([
  "browser.capture_view", "browser.computer_action", "computer.capture_screen",
  "computer.open_app", "computer.open_url", "computer.execute",
]);
const textLimit = 200_000;
const imageLimit = 5_000_000;

export type AgentControlMessage = "cancelled" | "excludedTool" | "unknownTool" | "imageTooLarge";
export interface AgentControlOrigin {
  kind: "pane" | "external";
  paneId?: string;
  cwd?: string;
}
export interface AgentControlPaneInfo {
  id: string;
  title: string;
  cwd: string;
  alive: boolean;
  state: "working" | "idle" | "waiting_input" | "exited";
  agent?: "claude" | "codex";
}
export interface AgentControlPaneRequest {
  requestId: string;
  op: "split" | "tab" | "close";
  pane?: string;
  direction?: "right" | "down";
  cwd?: string;
  title?: string;
  command?: string;
}
interface ToolRequest {
  requestId: string;
  name: string;
  arguments?: Record<string, unknown>;
  origin: AgentControlOrigin;
}

type Content = { type: "text"; text: string } | { type: "image"; mimeType: string; data: string };

export interface AgentControlExecutorDeps {
  capabilities: AiCapabilityRegistry;
  operations: AiCapabilityExecutionDependencies["operations"];
  listen<T>(name: string, handler: (payload: T) => void): () => void;
  invoke(command: "agent_control_ready" | "agent_control_result" | "agent_control_pane_result", args: Record<string, unknown>): Promise<unknown>;
  resolveProjectRoot(origin: AgentControlOrigin): string;
  resources(root: string, name: string): Promise<AiCapabilityExecutionDependencies["resources"]>;
  resolveWriteRoot?(input: Record<string, unknown>, resource: import("@/domain/ai/resource-registry").AiResource, root: string): string | undefined;
  beforeWrite?(requestId: string, root: string): Promise<void>;
  afterWrite?(requestId: string): Promise<void>;
  paneOperation(request: AgentControlPaneRequest, signal: AbortSignal): Promise<AgentControlPaneInfo | undefined>;
  message(key: AgentControlMessage): string;
}

function boundedText(text: string): string {
  const encoder = new TextEncoder();
  if (encoder.encode(text).length <= textLimit) return text;
  const marker = "\n[truncated]";
  const bytes = new Uint8Array(textLimit - encoder.encode(marker).length);
  const { read } = encoder.encodeInto(text, bytes);
  return text.slice(0, read) + marker;
}

function contentFor(name: string, result: AiCapabilityExecutionResult, message: AgentControlExecutorDeps["message"]): Content[] {
  let output = result;
  let image: Content | undefined;
  if (visualTools.has(name) && result.result && typeof result.result === "object") {
    const { data, mimeType = "image/png", ...capture } = result.result as Record<string, unknown>;
    if (typeof data === "string" && data && typeof mimeType === "string" && /^image\/(png|jpeg|webp)$/i.test(mimeType)) {
      const padding = data.endsWith("==") ? 2 : data.endsWith("=") ? 1 : 0;
      if (Math.floor(data.length * 3 / 4) - padding > imageLimit) {
        output = { ...result, result: { ...capture, mimeType, visualObservation: message("imageTooLarge") } };
      } else {
        image = { type: "image", mimeType, data };
        output = { ...result, result: { ...capture, mimeType } };
      }
    }
  }
  const text = boundedText(JSON.stringify(sanitizeAiResult(output)) ?? "");
  return image ? [{ type: "text", text }, image] : [{ type: "text", text }];
}

function publicError(error: unknown): string {
  return boundedText(redactSensitiveText(error instanceof Error ? error.message : String(error)));
}

export function startAgentControlExecutor(deps: AgentControlExecutorDeps): () => void {
  const active = new Map<string, AbortController>();
  let disposed = false;
  let needsPublish = false;
  let publishing = false;

  // Serialize updates so a slow IPC reply cannot replace a newer tool list.
  function updateTools() {
    needsPublish = true;
    if (publishing || disposed) return;
    publishing = true;
    void Promise.resolve().then(async () => {
      while (needsPublish && !disposed) {
        needsPublish = false;
        const tools = deps.capabilities.listTools()
          .filter(({ name }) => !excludedTools.has(name))
          .map(({ name, description, inputSchema }) => ({ name, description, inputSchema }));
        await deps.invoke("agent_control_ready", { tools }).catch(() => undefined);
      }
    }).finally(() => { publishing = false; });
  }

  async function execute(request: ToolRequest, signal: AbortSignal): Promise<Content[]> {
    if (excludedTools.has(request.name)) throw new Error(deps.message("excludedTool"));
    const capability = deps.capabilities.get(request.name);
    if (!capability) throw new Error(deps.message("unknownTool"));
    const projectRoot = deps.resolveProjectRoot(request.origin);
    const resources = await deps.resources(projectRoot, request.name);
    throwIfAborted(signal);
    const input = request.arguments ?? {};
    const resource = deps.capabilities.resolveResourceFor(request.name, input, resources);
    const args = {
      ...input,
      ...(request.name === "terminal.run_command" && projectRoot && typeof input.cwd !== "string" ? { cwd: projectRoot } : {}),
      resourceId: resource.id,
    };
    const write = capability.risk !== "read";
    try {
      const writeRoot = deps.resolveWriteRoot ? deps.resolveWriteRoot(args, resource, projectRoot) : projectRoot;
      if (write && writeRoot) await deps.beforeWrite?.(request.requestId, writeRoot);
      throwIfAborted(signal);
      const result = await deps.capabilities.execute(request.name, args, {
        resources, operations: deps.operations, projectRoot, signal,
        externalAgent: true,
        ...(write ? {
          authorization: {
            conversationId: request.requestId,
            consume: async (authorization) => authorization.toolName === request.name
              && authorization.resourceId === resource.id && authorization.parameters === args
              && authorization.risk === capability.risk && !signal.aborted,
          },
        } : {}),
      });
      return contentFor(request.name, result, deps.message);
    } finally {
      if (write) await deps.afterWrite?.(request.requestId).catch(() => undefined);
    }
  }

  function run<T extends Record<string, unknown>>(
    requestId: string,
    command: "agent_control_result" | "agent_control_pane_result",
    operation: (signal: AbortSignal) => Promise<T>,
    failed: (message: string) => Record<string, unknown>,
  ) {
    if (active.has(requestId) || disposed) return;
    const controller = new AbortController();
    active.set(requestId, controller);
    void runWithAbort(controller.signal, () => operation(controller.signal))
      .catch((error: unknown) => failed(controller.signal.aborted ? deps.message("cancelled") : publicError(error)))
      .then((payload) => disposed ? undefined : deps.invoke(command, { requestId, ...payload }))
      .catch(() => undefined)
      .finally(() => active.delete(requestId));
  }

  const stopCall = deps.listen<ToolRequest>("agent-control:call", (request) => {
    run(request.requestId, "agent_control_result",
      async (signal) => ({ content: await execute(request, signal), isError: false }),
      (text) => ({ content: [{ type: "text", text }], isError: true }));
  });
  const stopCancel = deps.listen<{ requestId: string }>("agent-control:cancel", ({ requestId }) => {
    active.get(requestId)?.abort(createAbortError());
  });
  const stopPane = deps.listen<AgentControlPaneRequest>("agent-control:pane-op", (request) => {
    run(request.requestId, "agent_control_pane_result",
      async (signal) => {
        const pane = await deps.paneOperation(request, signal);
        return pane ? { pane } : {};
      },
      (error) => ({ error }));
  });
  const stopTools = deps.capabilities.subscribe(updateTools);
  updateTools();

  return () => {
    disposed = true;
    stopCall();
    stopCancel();
    stopPane();
    stopTools();
    for (const controller of active.values()) controller.abort();
    active.clear();
  };
}
