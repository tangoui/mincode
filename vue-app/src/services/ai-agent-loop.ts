import {
  sanitizeAiAttachments,
  type AiAttachment,
} from "@/domain/ai/attachments";
import type { AiApiFormat } from "@/domain/ai/api-format";
import { plainChatHistory, toolsForModel } from "@/domain/ai/plugin-provider-profile";
import type { AiAnthropicAuth } from "@/domain/ai/anthropic-auth";
import { normalizeAiMaxLoops } from "@/domain/ai/agent-loop-limit";
import { aiLoopTurnCompletion } from "@/domain/ai/agent-loop-completion";
import { normalizeAiReasoningEffort } from "@/domain/ai/model-selection";
import { executeToolBatches, interruptedToolBatchResults } from "@/domain/ai/tool-execution-batch";
import { createAiStreamCompletion, retryAiStreamConnection } from "@/domain/ai/stream-completion";
import { createAiStreamIdleWatchdog } from "@/domain/ai/stream-idle-watchdog";
import { runWithAbort } from "@/domain/async/abort";
import type { AiTokenUsage } from "@/domain/ai/token-usage";
import { browserLocalFeatures } from "@/services/browser-runtime/registry";
import { invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import { isStreamAvailable, JSONStream, reportStreamDiagnostic } from "@/services/desktop-stream";

const INVALID_COMPLETION_RESULT =
  "The task_complete call was invalid. Call it exactly once with a non-empty summary after you have completed and verified the task.";
const PREMATURE_COMPLETION_RESULT =
  "task_complete was not accepted because other tool calls ran in the same turn. Review their results above, then call task_complete on its own.";

const INTERRUPTED_TOOL_RESULT: AiLoopToolExecution = {
  content: "The run was stopped before this tool call finished. It may or may not have taken effect; check its target before retrying.",
  status: "cancelled",
};

/** Result for calls skipped because the user declined an earlier call of the batch with an instruction. */
export const REDIRECTED_TOOL_RESULT = "Not run: the user redirected the task. Follow their instruction above.";

/** Sent once, with tools disabled, after the loop limit so the model reports its progress. */
export const LOOP_LIMIT_WRAP_UP_PROMPT =
  "The step limit for this task has been reached. Tools are disabled. Reply with text only: (1) what was accomplished, (2) what remains unfinished, (3) recommended next steps.";

const LOOP_LIMIT_FALLBACK_SUMMARY = "The task stopped after reaching the safety loop limit.";

export type AiChatRole = "user" | "assistant" | "system" | "tool";

export interface AiLoopConfig {
  baseUrl: string;
  apiKey: string;
  model: string;
  apiFormat: AiApiFormat;
  /** Anthropic-format credential header; omitted means x-api-key. */
  anthropicAuth?: AiAnthropicAuth;
  reasoningEffort?: string;
  contextWindow?: number;
  maxInputTokens?: number;
  maxOutputTokens?: number;
  openaiStreamUsage?: "auto" | "always" | "never";
  /** Models that cannot take tool definitions; requests to them carry no tools (never sent to Go). */
  noToolModels?: string[];
}

export interface AiLoopToolDefinition {
  name: string;
  description: string;
  inputSchema: Record<string, unknown>;
}

export interface AiLoopToolCall {
  id: string;
  name: string;
  arguments: string;
}

export interface AiLoopToolSource {
  kind: "native" | "mcp";
  toolName: string;
  serverId?: string;
  serverName?: string;
  resourceId?: string;
}

export interface AiLoopToolExecution {
  /** Bounded, model-visible summary of the execution result. */
  content: string;
  status?: "completed" | "failed" | "cancelled";
  attachments?: AiAttachment[];
  source?: AiLoopToolSource;
  /**
   * The user declined this call with an instruction: the batch's calls that have
   * not started yet are skipped so the model re-plans. Loop control only; never
   * stored in history.
   */
  redirect?: true;
}

/** Signed provider reasoning that must be replayed verbatim inside a tool loop (Anthropic). */
export interface AiLoopThinkingBlock {
  type: "thinking" | "redacted_thinking";
  thinking?: string;
  signature?: string;
  data?: string;
}

export interface AiLoopMessage {
  role: AiChatRole;
  content: string;
  attachments?: AiAttachment[];
  toolCalls?: AiLoopToolCall[];
  /** Only kept in memory for the running turn; never persisted. */
  thinkingBlocks?: AiLoopThinkingBlock[];
  toolCallId?: string;
  /** Execution metadata only. Raw tool payloads must never enter conversation history. */
  toolResult?: Pick<AiLoopToolExecution, "status" | "source">;
}

export interface AiLoopStreamEvent {
  type: "chunk" | "reasoning" | "thinking_block" | "tool_call" | "tool_call_progress" | "truncated" | "usage" | "done" | "error";
  /** tool_call_progress: the call's position in this response and its argument length so far. */
  index?: number;
  bytes?: number;
  text?: string;
  blockType?: AiLoopThinkingBlock["type"];
  thinking?: string;
  signature?: string;
  data?: string;
  id?: string;
  name?: string;
  arguments?: string;
  message?: string;
  inputTokens?: number;
  outputTokens?: number;
  cachedInputTokens?: number;
  cacheWriteTokens?: number;
  reasoningTokens?: number;
  requestCount?: number;
  model?: string;
}

export interface RunAiLoopOptions {
  config: AiLoopConfig;
  messages: AiLoopMessage[];
  tools: AiLoopToolDefinition[];
  /**
   * Read before every model request instead of `tools`, so tools loaded during
   * the turn (connector tools) are offered from the next request on.
   */
  currentTools?: () => AiLoopToolDefinition[];
  executeTool: (
    call: AiLoopToolCall,
    signal: AbortSignal,
  ) => Promise<AiLoopToolExecution>;
  /** Only explicitly read-only native calls may opt in; all other calls stay serialized. */
  canExecuteToolInParallel?: (call: AiLoopToolCall) => boolean;
  onAssistantDelta?: (delta: string) => void;
  onReasoningDelta?: (delta: string) => void;
  onToolCall?: (call: AiLoopToolCall) => void;
  onToolResult?: (call: AiLoopToolCall, result: AiLoopToolExecution) => void;
  onUsage?: (usage: AiTokenUsage) => void;
  onTurnComplete?: (message: AiLoopMessage) => void;
  /** Receives a snapshot after each completed model turn and tool-result batch. */
  onHistoryChange?: (history: AiLoopMessage[]) => void;
  /** Overrides a single streamed turn (for profile-scoped failover). */
  streamTurn?: typeof streamAiLoopTurn;
  /**
   * Called before every model request after the first; returning messages replaces
   * the history (e.g. compacted older turns), returning undefined keeps it.
   */
  compactHistory?: (history: readonly AiLoopMessage[]) => AiLoopMessage[] | undefined;
  /**
   * Called before every model request; a returned string is sent as a trailing
   * system message for that request only and is never stored in the history.
   */
  systemReminder?: () => string | undefined;
  /**
   * The loop corrected the model without user input: a task_complete it rejected, or a nudge to act
   * on a plan it only described. The UI shows these as steps so the extra requests are explained.
   */
  onLoopNotice?: (kind: AiLoopNotice) => void;
  /**
   * A tool call the model is still generating (name known, arguments growing). Display only: the
   * final tool_call event is the call that runs and enters the history.
   */
  onToolCallProgress?: (progress: AiLoopToolCallProgress) => void;
  signal?: AbortSignal;
  maxLoops?: number;
}

export interface AiLoopResult {
  history: AiLoopMessage[];
  completed: boolean;
  outcome: "response" | "task_completed" | "loop_limit";
  summary: string;
  loopCount: number;
  /** The turn ran business tools: a "response" ending it reports finished work, not chat. */
  usedTools?: boolean;
}

export async function runNextConversationQueueItem<T>(
  conversationId: string,
  dequeue: (conversationId: string) => T | undefined,
  send: (conversationId: string, item: T) => Promise<void>,
  canSend: (conversationId: string) => boolean = () => true,
) {
  if (!canSend(conversationId)) return;
  const next = dequeue(conversationId);
  if (next) await send(conversationId, next);
}

export function createConversationSendGate() {
  const pending = new Set<string>();

  return {
    claim(conversationId: string) {
      if (pending.has(conversationId)) return false;
      pending.add(conversationId);
      return true;
    },
    release(conversationId: string) {
      pending.delete(conversationId);
    },
    isClaimed(conversationId: string) {
      return pending.has(conversationId);
    },
    clear() {
      pending.clear();
    },
  };
}

export function continueAiLoopHistory(
  persistedHistory: AiLoopMessage[],
  currentUserMessage: AiLoopMessage,
) {
  return [
    ...persistedHistory.map((message) => ({
      ...message,
      attachments: sanitizeAiAttachments(message.attachments),
      toolCalls: message.toolCalls?.map((call) => ({ ...call })),
      toolResult: cloneToolResult(message.toolResult),
    })),
    {
      ...currentUserMessage,
      attachments: sanitizeAiAttachments(currentUserMessage.attachments),
    },
  ];
}

function createChannelId() {
  const uuid = globalThis.crypto?.randomUUID?.();
  return `vue-ai-stream-${uuid ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`}`;
}

function createAbortError() {
  return new DOMException("The AI request was cancelled.", "AbortError");
}

function asError(value: unknown) {
  return value instanceof Error ? value : new Error(String(value));
}

function thinkingBlockFromEvent(event: AiLoopStreamEvent): AiLoopThinkingBlock | undefined {
  if (event.blockType === "thinking" && event.signature) {
    return { type: "thinking", thinking: event.thinking ?? "", signature: event.signature };
  }
  if (event.blockType === "redacted_thinking" && event.data) {
    return { type: "redacted_thinking", data: event.data };
  }
  return undefined;
}

function cloneToolResult(
  toolResult: AiLoopMessage["toolResult"],
): AiLoopMessage["toolResult"] {
  if (!toolResult) return undefined;
  return {
    ...toolResult,
    source: toolResult.source ? { ...toolResult.source } : undefined,
  };
}

const PROVIDER_TOOL_NAME_PATTERN = /^[a-zA-Z0-9_-]+$/;

function providerToolName(name: string, used: Set<string>) {
  const base = PROVIDER_TOOL_NAME_PATTERN.test(name)
    ? name
    : name.replace(/[^a-zA-Z0-9_-]+/g, "_").replace(/^_+|_+$/g, "") || "tool";
  let candidate = base;
  let suffix = 2;
  while (used.has(candidate)) {
    candidate = `${base}_${suffix}`;
    suffix += 1;
  }
  used.add(candidate);
  return candidate;
}

export function createAiToolTransport(tools: AiLoopToolDefinition[]) {
  const used = new Set<string>();
  const providerByInternal = new Map<string, string>();
  const internalByProvider = new Map<string, string>();
  const definitions = tools.map((tool) => {
    const name = providerToolName(tool.name, used);
    providerByInternal.set(tool.name, name);
    internalByProvider.set(name, tool.name);
    return { ...tool, name };
  });
  const toProviderName = (name: string) => {
    const existing = providerByInternal.get(name);
    if (existing) return existing;

    // 持久化会话可能引用已卸载或本轮未注册的工具；Responses API 仍会校验
    // 历史 function_call.name，因此必须分配合法传输名，不能透传内部点号名称。
    const providerName = providerToolName(name, used);
    providerByInternal.set(name, providerName);
    internalByProvider.set(providerName, name);
    return providerName;
  };
  return {
    definitions,
    toProviderName,
    toInternalName: (name: string) => internalByProvider.get(name) ?? name,
  };
}

/**
 * Whether model requests can run here: always on the desktop; in browser-local mode once the
 * browser AI module (services/browser-runtime/ai) has registered the "ai" feature.
 */
export function canStreamAi() {
  return isDesktopRuntime() || browserLocalFeatures.some((feature) => feature.id === "ai");
}

/**
 * Browser-local mode: plain chat straight from the browser to the provider (fetch + SSE). Tools
 * are never sent. The module is loaded on demand so the browser AI code stays out of the main
 * bundle; it is already loaded with services/browser-runtime in browser-local mode.
 */
async function streamBrowserAiLoopTurn(
  config: AiLoopConfig,
  messages: AiLoopMessage[],
  onEvent: (event: AiLoopStreamEvent) => void,
  signal?: AbortSignal,
  idleTimeoutMs?: number,
): Promise<void> {
  const { streamBrowserAiTurn } = await import("@/services/browser-runtime/ai");
  await streamBrowserAiTurn(config, messages, onEvent, signal, idleTimeoutMs);
  // A stopped browser stream ends with `done`; callers expect an AbortError, as from the desktop stream.
  if (signal?.aborted) throw createAbortError();
}

/** Calls the IPC streaming API once and exposes text, reasoning and tool calls. */
export async function streamAiLoopTurn(
  config: AiLoopConfig,
  messages: AiLoopMessage[],
  tools: AiLoopToolDefinition[],
  onEvent: (event: AiLoopStreamEvent) => void,
  signal?: AbortSignal,
  idleTimeoutMs?: number,
): Promise<void> {
  if (signal?.aborted) throw createAbortError();
  // `tools` is ignored in the browser: browser-local AI is plain chat (no native tools, no MCP).
  if (!isDesktopRuntime()) return streamBrowserAiLoopTurn(config, messages, onEvent, signal, idleTimeoutMs);
  const channelId = createChannelId();
  const toolTransport = createAiToolTransport(toolsForModel(config.model, config.noToolModels, tools));
  const { noToolModels: _noToolModels, ...requestConfig } = config;
  // A chat-only model also must not see earlier tool calls (failover can switch mid-conversation).
  const chatOnly = config.noToolModels?.includes(config.model) === true;
  const providerMessages = (chatOnly ? plainChatHistory(messages) : messages).map((message) => ({
    ...message,
    toolCalls: message.toolCalls?.map((call) => ({
      ...call,
      name: toolTransport.toProviderName(call.name),
    })),
  }));
  const payload = {
    channelId,
    config: {
      ...requestConfig,
      reasoningEffort: normalizeAiReasoningEffort(config.reasoningEffort),
      tools: toolTransport.definitions,
    },
    messages: providerMessages,
  };
  if (isStreamAvailable()) {
    const connectionStartedAt = Date.now();
    return retryAiStreamConnection(
      () => streamAiLoopTurnViaStream(payload, toolTransport, onEvent, signal, idleTimeoutMs),
      signal,
      (error, attempt) => reportStreamDiagnostic(
        "handshake_failure", `AI stream ${error.reason}; attempt=${attempt}; elapsedMs=${Date.now() - connectionStartedAt}`,
      ),
    );
  }
  return streamAiLoopTurnViaEvents(payload, toolTransport, onEvent, signal, idleTimeoutMs);
}

/** 事件版（浏览器预览/降级路径）：invoke 阻塞执行 + stream:channelId 事件推送。 */
async function streamAiLoopTurnViaEvents(
  payload: { channelId: string },
  toolTransport: ReturnType<typeof createAiToolTransport>,
  onEvent: (event: AiLoopStreamEvent) => void,
  signal?: AbortSignal,
  idleTimeoutMs?: number,
): Promise<void> {
  const channelId = payload.channelId;
  let terminal = false;
  let eventError: Error | undefined;
  const cancelProvider = () => {
    void invokeBackend("ai_stream_cancel", { channelId }).catch(() => undefined);
  };
  const watchdog = createAiStreamIdleWatchdog(cancelProvider, idleTimeoutMs);
  const stop = listenAppEvent<AiLoopStreamEvent>(
    `stream:${channelId}`,
    (event) => {
      if (event.type !== "error" && event.type !== "done") watchdog.touch();
      onEvent(
        event.type === "tool_call" && event.name
          ? { ...event, name: toolTransport.toInternalName(event.name) }
          : event,
      );
      if (event.type === "error")
        eventError = new Error(event.message || "AI stream failed");
      if (event.type === "done" || event.type === "error") terminal = true;
    },
  );
  const cancel = () => {
    cancelProvider();
  };
  signal?.addEventListener("abort", cancel, { once: true });

  try {
    try {
      await Promise.race([
        invokeBackend("ai_stream_chat", payload),
        watchdog.expired,
      ]);
    } catch (error) {
      // The backend emits its canonical stream error before the command rejects.
      // Prefer it over a generic IPC wrapper so timeout handling remains identifiable.
      if (eventError) throw eventError;
      throw error;
    }
    if (signal?.aborted) throw createAbortError();
    if (eventError) throw eventError;
    // The backend's command resolution is the reliable terminal fallback when the final IPC event races its cleanup.
    if (!terminal) return;
  } finally {
    watchdog.dispose();
    stop?.();
    signal?.removeEventListener("abort", cancel);
  }
}

/** Streams 版（桌面运行时）：JSONStream("ai-chat") 双向通道，异常断流按失败处理。 */
async function streamAiLoopTurnViaStream(
  payload: { channelId: string },
  toolTransport: ReturnType<typeof createAiToolTransport>,
  onEvent: (event: AiLoopStreamEvent) => void,
  signal?: AbortSignal,
  idleTimeoutMs?: number,
): Promise<void> {
  const socket = JSONStream("ai-chat");
  const cancelProvider = () => {
    void invokeBackend("ai_stream_cancel", {
      channelId: payload.channelId,
    }).catch(() => undefined);
  };
  function closeSocket() {
    try {
      socket.close();
    } catch {
      // 连接可能已关闭
    }
  }
  const completion = createAiStreamCompletion(closeSocket);
  const watchdog = createAiStreamIdleWatchdog(() => {
    cancelProvider();
    closeSocket();
  }, idleTimeoutMs);

  socket.addEventListener("message", (event: MessageEvent) => {
    const raw = event.data as AiLoopStreamEvent | undefined;
    if (!raw || typeof raw !== "object" || !raw.type) return;
    if (raw.type !== "error" && raw.type !== "done") watchdog.touch();
    onEvent(
      raw.type === "tool_call" && raw.name
        ? { ...raw, name: toolTransport.toInternalName(raw.name) }
        : raw,
    );
    if (raw.type === "error") {
      completion.terminal(new Error(raw.message || "AI stream failed"));
    } else if (raw.type === "done") {
      completion.terminal();
    }
  });
  socket.addEventListener("close", () => completion.disconnected());
  socket.addEventListener("error", () => completion.disconnected());

  const sendPayload = () => {
    if (signal?.aborted || !completion.opened()) return;
    try {
      socket.send(payload);
    } catch {
      completion.disconnected();
    }
  };
  // open 可能在监听前已就绪（readyState 已 OPEN），两种情况都要覆盖。
  if (socket.readyState === 1) sendPayload();
  else socket.addEventListener("open", sendPayload, { once: true });

  const cancel = () => {
    // IPC close does not guarantee a local close event. Release the caller
    // immediately and explicitly cancel the provider request as a fallback.
    completion.cancel();
    cancelProvider();
    closeSocket();
  };
  signal?.addEventListener("abort", cancel, { once: true });
  if (signal?.aborted) cancel();

  try {
    await Promise.race([completion.completed, watchdog.expired]);
    if (signal?.aborted) throw createAbortError();
  } finally {
    socket.removeEventListener("open", sendPayload);
    completion.dispose();
    watchdog.dispose();
    signal?.removeEventListener("abort", cancel);
    closeSocket();
  }
}

function parseCompletion(call: AiLoopToolCall) {
  try {
    const parsed = JSON.parse(call.arguments) as unknown;
    if (!parsed || typeof parsed !== "object") return undefined;
    const summary = (parsed as Record<string, unknown>).summary;
    return typeof summary === "string" && summary.trim()
      ? summary.trim()
      : undefined;
  } catch {
    return undefined;
  }
}

const completionTool: AiLoopToolDefinition = {
  name: "task_complete",
  description:
    "Call this only after the requested task has been completed and verified. The summary is displayed verbatim as the user-facing delivery report. Do not reply with a one-line completion notice. For project analysis, code review, architecture review, investigation, or research, provide a detailed Markdown report covering: conclusion; scope and evidence inspected; key findings with relevant file paths, symbols, or call chains; risks or open questions; recommended next steps and priority; and validation performed or not performed. For implementation tasks, clearly state changed files, behavior changes, verification, and remaining limitations. Keep only sections that apply, but preserve concrete evidence and do not invent facts. Write the summary in the language the user wrote in.",
  inputSchema: {
    type: "object",
    properties: {
      summary: {
        type: "string",
        description:
          "Detailed, evidence-based Markdown delivery report shown directly to the user. Do not use only a short completion sentence.",
      },
    },
    required: ["summary"],
    additionalProperties: false,
  },
};

/**
 * Framework-neutral model/tool loop. It preserves the complete message history,
 * executes returned tools and feeds their results back to the model. Plain-text
 * conversational responses stop immediately; tool-driven tasks use task_complete.
 */
export type AiLoopNotice = "completion_rejected" | "continuation_nudged";

export interface AiLoopToolCallProgress {
  index: number;
  name: string;
  bytes: number;
}

export async function runAiLoop(
  options: RunAiLoopOptions,
): Promise<AiLoopResult> {
  const maxLoops = normalizeAiMaxLoops(options.maxLoops);
  const history: AiLoopMessage[] = options.messages.map((message) => ({
    ...message,
    attachments: sanitizeAiAttachments(message.attachments),
    toolCalls: message.toolCalls?.map((call) => ({ ...call })),
    toolResult: cloneToolResult(message.toolResult),
  }));
  // Plain conversational turns need no tool contract; task_complete is only useful
  // after at least one business tool is available for an actionable request.
  const toolsForRequest = () => {
    const tools = options.currentTools?.() ?? options.tools;
    return tools.length > 0 ? [...tools, completionTool] : [];
  };
  let lastAssistantText = "";
  let usedBusinessTools = false;
  // Set after a continuation nudge; a second text-only reply ends the loop instead of
  // re-sending near-identical requests until the safety limit.
  let awaitingContinuation = false;
  const notifyHistoryChange = () => {
    options.onHistoryChange?.(history.map((message) => ({
      ...message,
      attachments: sanitizeAiAttachments(message.attachments),
      toolCalls: message.toolCalls?.map((call) => ({ ...call })),
      toolResult: cloneToolResult(message.toolResult),
    })));
  };

  /**
   * Streams one model request; the system reminder is appended for that request only.
   * `textOnly` ignores tool calls and thinking blocks (the loop-limit wrap-up request).
   */
  const streamRequest = async (
    messages: AiLoopMessage[],
    tools: AiLoopToolDefinition[],
    textOnly = false,
  ) => {
    let content = "";
    let reasoning = "";
    const toolCalls: AiLoopToolCall[] = [];
    const thinkingBlocks: AiLoopThinkingBlock[] = [];
    const reminder = options.systemReminder?.();
    const requestMessages = reminder
      ? [...messages, { role: "system" as const, content: reminder }]
      : messages;

    await runWithAbort(options.signal, () =>
      (options.streamTurn ?? streamAiLoopTurn)(
        options.config,
        requestMessages,
        tools,
        (event) => {
          if (options.signal?.aborted) return;
          if (event.type === "chunk" && event.text) {
            content += event.text;
            options.onAssistantDelta?.(event.text);
          } else if (event.type === "reasoning" && event.text) {
            reasoning += event.text;
            options.onReasoningDelta?.(event.text);
          } else if (event.type === "thinking_block" && !textOnly) {
            const block = thinkingBlockFromEvent(event);
            if (block) thinkingBlocks.push(block);
          } else if (event.type === "tool_call" && event.id && event.name && !textOnly) {
            const call = {
              id: event.id,
              name: event.name,
              arguments: event.arguments ?? "{}",
            };
            toolCalls.push(call);
            options.onToolCall?.(call);
          } else if (event.type === "tool_call_progress" && !textOnly && event.name && typeof event.index === "number") {
            options.onToolCallProgress?.({ index: event.index, name: event.name, bytes: event.bytes ?? 0 });
          } else if (event.type === "truncated") {
            content += "\n\n[The model output was truncated.]";
            options.onAssistantDelta?.("\n\n[The model output was truncated.]");
          } else if (event.type === "usage") {
            options.onUsage?.({
              inputTokens: event.inputTokens ?? 0,
              outputTokens: event.outputTokens ?? 0,
              cachedInputTokens: event.cachedInputTokens ?? 0,
              cacheWriteTokens: event.cacheWriteTokens ?? 0,
              reasoningTokens: event.reasoningTokens ?? 0,
              requestCount: event.requestCount ?? 1,
              attemptedRequestCount: 0,
              models: event.model ? [event.model] : undefined,
            });
          }
        },
        options.signal,
      ),
    );
    return { content, reasoning, toolCalls, thinkingBlocks };
  };

  for (let loopCount = 1; loopCount <= maxLoops; loopCount += 1) {
    if (options.signal?.aborted) throw createAbortError();
    if (loopCount > 1) {
      const compacted = options.compactHistory?.(history);
      if (compacted) {
        history.splice(0, history.length, ...compacted);
        notifyHistoryChange();
      }
    }
    const tools = toolsForRequest();
    const { content, reasoning, toolCalls, thinkingBlocks } = await streamRequest(history, tools);

    if (reasoning) lastAssistantText = content || lastAssistantText;
    const assistantMessage: AiLoopMessage = {
      role: "assistant",
      content,
      toolCalls,
      ...(toolCalls.length && thinkingBlocks.length ? { thinkingBlocks } : {}),
    };
    history.push(assistantMessage);
    options.onTurnComplete?.(assistantMessage);
    if (!toolCalls.length) notifyHistoryChange();
    lastAssistantText = content || lastAssistantText;

    const completionCalls = toolCalls.filter(
      (call) => call.name === completionTool.name,
    );
    const businessCalls = toolCalls.filter(
      (call) => call.name !== completionTool.name,
    );
    const completionSummary =
      completionCalls.length === 1
        ? parseCompletion(completionCalls[0])
        : undefined;

    const invalidCompletion =
      completionCalls.length > 1 ||
      (completionCalls.length === 1 && !completionSummary);

    const pushToolResult = (call: AiLoopToolCall, result: AiLoopToolExecution) => {
      history.push({
        role: "tool",
        toolCallId: call.id,
        content: result.content,
        attachments: sanitizeAiAttachments(result.attachments),
        toolResult: {
          status: result.status,
          source: result.source ? { ...result.source } : undefined,
        },
      });
    };
    const finishedResults = new Map<string, AiLoopToolExecution>();
    // Set once the user redirects the task; calls that have not started yet are skipped.
    let redirected = false;
    let toolResults: AiLoopToolExecution[];
    try {
      toolResults = await executeToolBatches(
        businessCalls,
        (call) => options.canExecuteToolInParallel?.(call) === true,
        async (call) => {
          if (redirected) {
            const skipped: AiLoopToolExecution = { content: REDIRECTED_TOOL_RESULT, status: "cancelled" };
            finishedResults.set(call.id, skipped);
            return skipped;
          }
          usedBusinessTools = true;
          let result: AiLoopToolExecution;
          try {
            result = await runWithAbort(options.signal, () =>
              options.executeTool(
                call,
                options.signal ?? new AbortController().signal,
              ),
            );
          } catch (error) {
            if (options.signal?.aborted) throw createAbortError();
            result = {
              content: `Tool failed: ${asError(error).message}`,
              status: "failed" as const,
            };
          }
          if (result.redirect) redirected = true;
          finishedResults.set(call.id, result);
          return result;
        },
      );
    } catch (error) {
      // Stopped mid-batch: keep the results of tools that already ran, so a later
      // "continue" does not repeat a write, and mark the rest as interrupted.
      const results = interruptedToolBatchResults(businessCalls, finishedResults, INTERRUPTED_TOOL_RESULT);
      for (const [index, call] of businessCalls.entries()) pushToolResult(call, results[index]);
      notifyHistoryChange();
      throw error;
    }
    for (const [index, call] of businessCalls.entries()) {
      const result = toolResults[index];
      options.onToolResult?.(call, result);
      pushToolResult(call, result);
    }

    // Completion is accepted only on its own: the model must see the results of
    // tools it ran in the same turn before summarizing.
    const completionRejection = invalidCompletion
      ? INVALID_COMPLETION_RESULT
      : completionSummary && businessCalls.length > 0
        ? PREMATURE_COMPLETION_RESULT
        : undefined;
    if (completionRejection) {
      for (const call of completionCalls) {
        history.push({ role: "tool", toolCallId: call.id, content: completionRejection });
      }
      options.onLoopNotice?.("completion_rejected");
      notifyHistoryChange();
      continue;
    }
    notifyHistoryChange();

    if (completionSummary) {
      history.push({
        role: "tool",
        toolCallId: completionCalls[0].id,
        content: "task_complete accepted.",
      });
      if (!usedBusinessTools) {
        return {
          history,
          completed: true,
          outcome: "response",
          summary: content.trim() || completionSummary,
          loopCount,
          usedTools: false,
        };
      }
      return {
        history,
        completed: true,
        outcome: "task_completed",
        summary: completionSummary,
        loopCount,
      };
    }

    const turnCompletion = aiLoopTurnCompletion(content, businessCalls.length);
    // Without tools the model cannot act on a continuation nudge, and after one ignored
    // nudge another one only repeats the same answer.
    const cannotContinue = turnCompletion.requiresContinuation
      && (tools.length === 0 || awaitingContinuation);
    if (turnCompletion.completed || cannotContinue) {
      return {
        history,
        completed: true,
        outcome: "response",
        summary: turnCompletion.summary,
        loopCount,
        usedTools: usedBusinessTools,
      };
    }
    awaitingContinuation = turnCompletion.requiresContinuation;
    if (turnCompletion.requiresContinuation) {
      options.onLoopNotice?.("continuation_nudged");
      history.push({
        role: "user",
        content:
          "Continue the promised work now. Use the appropriate tools instead of only describing the next step.",
      });
      notifyHistoryChange();
    }
  }

  // Out of steps: ask once more for a text-only progress report.
  if (options.signal?.aborted) throw createAbortError();
  let summary = lastAssistantText || LOOP_LIMIT_FALLBACK_SUMMARY;
  const wrapUpRequest: AiLoopMessage = { role: "user", content: LOOP_LIMIT_WRAP_UP_PROMPT };
  try {
    // The tool definitions are still sent because Anthropic rejects a request whose history
    // holds tool_use/tool_result blocks without them; textOnly drops any tool call, which is
    // what keeps tools disabled.
    const wrapUp = await streamRequest([...history, wrapUpRequest], toolsForRequest(), true);
    const wrapUpSummary = wrapUp.content.trim();
    // Stored only on a usable reply, so a failed wrap-up leaves no unanswered request.
    if (wrapUpSummary) {
      const assistantMessage: AiLoopMessage = { role: "assistant", content: wrapUp.content };
      history.push(wrapUpRequest, assistantMessage);
      options.onTurnComplete?.(assistantMessage);
      notifyHistoryChange();
      summary = wrapUpSummary;
    }
  } catch {
    // A failed wrap-up keeps the previous fallback summary; a stop still ends the run.
    if (options.signal?.aborted) throw createAbortError();
  }

  return {
    history,
    completed: false,
    outcome: "loop_limit",
    summary,
    loopCount: maxLoops,
  };
}
