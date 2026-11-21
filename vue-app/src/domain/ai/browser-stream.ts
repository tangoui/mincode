/**
 * Browser-local AI chat: provider request bodies and SSE parsing for the three API formats.
 *
 * Pure functions ported from backend/ai/stream.go (streamOpenAI, streamOpenAIResponses,
 * streamAnthropic, forEachSSEDataLine) and ai.go (normalizeBaseURL), so a browser request carries
 * the same fields and produces the same `AiLoopStreamEvent`s as the desktop. Only plain chat: the
 * browser never sends tool definitions, and tool history is folded into text (plainChatHistory).
 * Provider error texts keep Go's wording so both runtimes report failures identically.
 */
import type { AiAttachment } from "@/domain/ai/attachments";
import type { AiApiFormat } from "@/domain/ai/api-format";
import { normalizeAiReasoningEffort } from "@/domain/ai/model-selection";
import { plainChatHistory } from "@/domain/ai/plugin-provider-profile";
import type { AiLoopConfig, AiLoopMessage, AiLoopStreamEvent } from "@/services/ai-agent-loop";

export interface BrowserAiRequest {
  url: string;
  headers: Record<string, string>;
  body: string;
}

type Json = Record<string, unknown>;
type ChatMessage = Pick<AiLoopMessage, "role" | "content" | "attachments" | "toolCalls">;

const ANTHROPIC_API_VERSION = "2023-06-01";
/** Go `aiSSEBufMax`: one SSE event (or line) may not exceed 4 MiB. */
const SSE_BUFFER_MAX = 4 * 1024 * 1024;
/** Go `aiErrorTextLimit`. */
const ERROR_TEXT_LIMIT = 4 * 1024;

/**
 * An Error that keeps the failure it replaces. The tsconfig lib predates ES2022's `{ cause }`
 * constructor option, so the cause is attached by hand (as in services/browser-runtime/http.ts).
 */
export function errorWithCause(message: string, cause: unknown): Error {
  return Object.assign(new Error(message), { cause });
}

/** Go normalizeBaseURL: keep a trailing `vN` segment, otherwise append `/v1`. */
export function normalizeBaseURL(url: string): string {
  const trimmed = url.replace(/\/+$/, "");
  const lastSegment = trimmed.slice(trimmed.lastIndexOf("/") + 1);
  return /^v\d+$/.test(lastSegment) ? trimmed : `${trimmed}/v1`;
}

/** Go validateAiBaseURL (core.ValidateRequestURL): only http and https. */
export function validateAiBaseUrl(raw: string) {
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch (error) {
    throw errorWithCause(`AI base_url 校验失败: 无效 URL: ${error instanceof Error ? error.message : String(error)}`, error);
  }
  const scheme = parsed.protocol.replace(/:$/, "").toLowerCase();
  if (scheme !== "http" && scheme !== "https") {
    throw new Error(`AI base_url 校验失败: 不支持的协议: ${scheme}(仅允许 http/https)`);
  }
}

/** Go aiModelsEndpoint. */
export function aiModelsEndpoint(baseUrl: string): string {
  const trimmed = baseUrl.trim();
  if (!trimmed) throw new Error("请先填写 AI 服务地址");
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    throw new Error("AI 服务地址无效");
  }
  if (!parsed.host) throw new Error("AI 服务地址无效");
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") throw new Error("AI 服务地址仅支持 HTTP 或 HTTPS");
  if (parsed.search || parsed.hash) throw new Error("AI 服务地址不能包含查询参数或片段");
  return `${normalizeBaseURL(trimmed)}/models`;
}

/**
 * Go anthropicAuthHeaders plus `anthropic-dangerous-direct-browser-access`, which Anthropic requires
 * before it answers a CORS request from a web page.
 */
export function anthropicBrowserHeaders(config: Pick<AiLoopConfig, "apiKey" | "anthropicAuth">): Record<string, string> {
  const headers: Record<string, string> = {
    "anthropic-version": ANTHROPIC_API_VERSION,
    "anthropic-dangerous-direct-browser-access": "true",
  };
  if (!config.apiKey.trim()) return headers;
  if (config.anthropicAuth === "bearer") headers.Authorization = `Bearer ${config.apiKey}`;
  else headers["x-api-key"] = config.apiKey;
  return headers;
}

/** Go shouldRequestOpenAIStreamUsage. */
export function shouldRequestOpenAIStreamUsage(config: Pick<AiLoopConfig, "baseUrl" | "openaiStreamUsage">) {
  const mode = (config.openaiStreamUsage ?? "").trim().toLowerCase();
  return mode === "always" || (mode !== "never" && config.baseUrl.toLowerCase().includes("api.openai.com"));
}

function isOpenAIReasoningModel(model: string) {
  let name = model.trim().toLowerCase();
  name = name.slice(name.lastIndexOf("/") + 1);
  return ["o1", "o3", "o4", "gpt-5", "gpt-6"].some(
    (prefix) => name === prefix || name.startsWith(`${prefix}-`) || name.startsWith(`${prefix}.`),
  );
}

function reasoningEffort(config: AiLoopConfig) {
  return normalizeAiReasoningEffort(config.reasoningEffort) ?? "";
}

// ── messages ─────────────────────────────────────────────────────────────────

function runeLength(text: string) {
  let count = 0;
  for (const _ of text) count += 1;
  return count;
}

/** Go approximateMessageTokens. */
function approximateMessageTokens(message: ChatMessage) {
  let chars = runeLength(message.content);
  for (const attachment of message.attachments ?? []) {
    if (attachment.kind === "text") chars += runeLength(attachment.data);
  }
  for (const call of message.toolCalls ?? []) chars += runeLength(call.name) + runeLength(call.arguments);
  return Math.floor((chars + 3) / 4) + 8;
}

/**
 * Go limitChatMessages: keeps the leading system messages and the newest messages that fit the
 * input budget (context window minus output, or maxInputTokens); the last message always goes.
 */
export function limitChatMessages<T extends ChatMessage>(
  messages: readonly T[],
  config: Pick<AiLoopConfig, "contextWindow" | "maxInputTokens" | "maxOutputTokens">,
): T[] {
  let inputLimit = config.maxInputTokens ?? 0;
  const contextWindow = config.contextWindow ?? 0;
  if (contextWindow > 0) {
    const output = config.maxOutputTokens ?? 0;
    if (output >= contextWindow) throw new Error("最大输出 Token 必须小于上下文窗口");
    const contextInput = contextWindow - output;
    if (inputLimit === 0 || contextInput < inputLimit) inputLimit = contextInput;
  }
  if (inputLimit <= 0) return [...messages];

  let prefix = 0;
  while (prefix < messages.length - 1 && messages[prefix]!.role === "system") prefix += 1;
  let prefixCost = 0;
  for (let index = 0; index < prefix; index += 1) prefixCost += approximateMessageTokens(messages[index]!);
  const bodyBudget = prefixCost < inputLimit ? inputLimit - prefixCost : 0;

  let tail = messages.length - 1;
  while (tail > prefix && messages[tail]!.role === "system") tail -= 1;
  if (tail >= prefix && messages[tail]?.role === "system") tail = messages.length - 1;
  const body: T[] = [];
  let used = 0;
  for (let index = messages.length - 1; index >= prefix; index -= 1) {
    const cost = approximateMessageTokens(messages[index]!);
    if (index < tail && used + cost > bodyBudget) break;
    body.unshift(messages[index]!);
    used += cost;
  }
  const kept: T[] = [];
  for (let index = 0; index < prefix; index += 1) {
    const cost = approximateMessageTokens(messages[index]!);
    if (used + cost > inputLimit) continue;
    kept.push(messages[index]!);
    used += cost;
  }
  return [...kept, ...body];
}

/** Go textWithFileAttachments. */
function textWithFileAttachments(message: ChatMessage) {
  let text = message.content.trim() ? message.content : "请分析附件。";
  for (const item of message.attachments ?? []) {
    if (item.kind === "text") text += `\n\n## 文本附件：${item.name}\n\`\`\`text\n${item.data}\n\`\`\``;
  }
  return text;
}

interface NormalizedImage {
  mimeType: string;
  data: string;
}

function sniffImageType(bytes: string) {
  if (bytes.startsWith("\x89PNG\r\n\x1a\n")) return "image/png";
  if (bytes.startsWith("\xff\xd8\xff")) return "image/jpeg";
  if (bytes.startsWith("GIF87a") || bytes.startsWith("GIF89a")) return "image/gif";
  if (bytes.startsWith("RIFF") && bytes.slice(8, 14) === "WEBPVP") return "image/webp";
  return "";
}

/** Go normalizedImageAttachment: accepts base64 (or a base64 data URL) whose bytes are PNG, JPEG, GIF or WebP. */
function normalizedImage(item: AiAttachment): NormalizedImage | undefined {
  const mimeType = item.mimeType.trim().toLowerCase();
  let data = item.data.trim();
  if (item.kind !== "image" || !mimeType.startsWith("image/") || !data) return undefined;
  if (data.toLowerCase().startsWith("data:")) {
    const comma = data.indexOf(",");
    if (comma < 0) return undefined;
    const parts = data.slice("data:".length, comma).split(";");
    if (parts.length < 2 || parts[parts.length - 1]!.trim().toLowerCase() !== "base64") return undefined;
    if (!parts[0]!.trim().toLowerCase().startsWith("image/")) return undefined;
    data = data.slice(comma + 1);
  }
  data = data.replace(/\s+/g, "");
  if (!data || !/^[A-Za-z0-9+/]*={0,2}$/.test(data)) return undefined;
  let decoded: string;
  try {
    decoded = atob(data.replace(/=+$/, "").padEnd(Math.ceil(data.replace(/=+$/, "").length / 4) * 4, "="));
  } catch {
    return undefined;
  }
  const detected = sniffImageType(decoded);
  if (!decoded || !detected) return undefined;
  return { mimeType: detected, data: btoa(decoded) };
}

function images(message: ChatMessage) {
  return (message.attachments ?? []).flatMap((item) => normalizedImage(item) ?? []);
}

function openaiMessage(message: ChatMessage): Json {
  const text = textWithFileAttachments(message);
  const pictures = images(message);
  if (!pictures.length) return { role: message.role, content: text };
  return {
    role: message.role,
    content: [
      { type: "text", text },
      ...pictures.map((image) => ({
        type: "image_url",
        image_url: { url: `data:${image.mimeType};base64,${image.data}`, detail: "auto" },
      })),
    ],
  };
}

function responsesInputItem(message: ChatMessage): Json {
  const text = textWithFileAttachments(message);
  if (message.role === "assistant") {
    return { type: "message", role: "assistant", content: [{ type: "output_text", text }] };
  }
  return {
    type: "message",
    role: message.role === "tool" ? "user" : message.role,
    content: [
      { type: "input_text", text },
      ...images(message).map((image) => ({ type: "input_image", image_url: `data:${image.mimeType};base64,${image.data}` })),
    ],
  };
}

function anthropicContent(message: ChatMessage): string | Json[] {
  const text = textWithFileAttachments(message);
  const pictures = images(message);
  if (!pictures.length) return text;
  return [
    { type: "text", text },
    ...pictures.map((image) => ({ type: "image", source: { type: "base64", media_type: image.mimeType, data: image.data } })),
  ];
}

// ── request bodies ───────────────────────────────────────────────────────────

const ANTHROPIC_THINKING_BUDGETS: Readonly<Record<string, number>> = {
  low: 1024, medium: 4096, high: 16384, xhigh: 24576, max: 32768, ultra: 65536,
};

function anthropicThinkingBudget(effort: string, maxTokens: number) {
  let budget = ANTHROPIC_THINKING_BUDGETS[effort];
  if (budget === undefined) return undefined;
  if (maxTokens > 0 && budget >= maxTokens) budget = maxTokens - 1024;
  return budget < 1024 ? undefined : budget;
}

/** Go anthropicUsesAdaptiveThinking. */
function anthropicUsesAdaptiveThinking(model: string) {
  const name = model.toLowerCase();
  if (name.includes("claude-mythos") || name.includes("claude-fable")) return true;
  const match = /claude-(?:opus|sonnet|haiku)-(\d+)(?:[-.](\d{1,2}))?(?:\D|$)/.exec(name);
  if (!match) return false;
  const major = Number(match[1]);
  const minor = Number(match[2] ?? 0);
  return major > 4 || (major === 4 && minor >= 6);
}

/** Go anthropicAdaptiveEffort. */
function anthropicAdaptiveEffort(model: string, effort: string) {
  if (effort === "low" || effort === "medium" || effort === "high") return effort;
  if (effort === "xhigh") {
    const name = model.toLowerCase();
    return name.includes("4-6") || name.includes("4.6") || name.includes("mythos-preview") ? "high" : "xhigh";
  }
  return "max";
}

const EPHEMERAL_CACHE = { type: "ephemeral" };

function anthropicBody(config: AiLoopConfig, messages: ChatMessage[], cache: boolean): Json {
  const system = messages
    .filter((message) => message.role === "system" && message.content.trim())
    .map((message) => message.content)
    .join("\n\n");
  const effort = reasoningEffort(config);
  const thinkingEffort = effort in ANTHROPIC_THINKING_BUDGETS;
  const chatMessages: Json[] = messages
    .filter((message) => message.role !== "system")
    .map((message) => ({ role: message.role, content: anthropicContent(message) }));

  let maxTokens = config.maxOutputTokens ?? 0;
  if (maxTokens === 0 && thinkingEffort) maxTokens = ANTHROPIC_THINKING_BUDGETS[effort]! + 16384;
  if (maxTokens === 0) maxTokens = 16384;

  const body: Json = { model: config.model, max_tokens: maxTokens, messages: chatMessages, stream: true };
  if (system) body.system = system;
  if (thinkingEffort && anthropicUsesAdaptiveThinking(config.model)) {
    body.thinking = { type: "adaptive", display: "summarized" };
    body.output_config = { effort: anthropicAdaptiveEffort(config.model, effort) };
  } else if (effort) {
    const budget = anthropicThinkingBudget(effort, maxTokens);
    if (budget !== undefined) body.thinking = { type: "enabled", budget_tokens: budget };
  }
  if (cache) {
    // Go addAnthropicCacheBreakpoints: mark the system prompt and the newest message.
    if (system) body.system = [{ type: "text", text: system, cache_control: EPHEMERAL_CACHE }];
    const last = chatMessages[chatMessages.length - 1];
    if (last && typeof last.content === "string" && last.content) {
      last.content = [{ type: "text", text: last.content, cache_control: EPHEMERAL_CACHE }];
    } else if (last && Array.isArray(last.content) && last.content.length) {
      const blocks = last.content as Json[];
      blocks[blocks.length - 1] = { ...blocks[blocks.length - 1], cache_control: EPHEMERAL_CACHE };
    }
  }
  return body;
}

/**
 * Builds the provider request for one plain-chat turn. Throws (like Go) when the configured output
 * budget does not fit the context window. `cache: false` drops Anthropic cache breakpoints for
 * gateways that reject `cache_control`.
 */
export function buildBrowserAiRequest(
  config: AiLoopConfig,
  messages: readonly AiLoopMessage[],
  options: { cache?: boolean } = {},
): BrowserAiRequest {
  const plain: ChatMessage[] = plainChatHistory(limitChatMessages(messages, config));
  const base = normalizeBaseURL(config.baseUrl);
  const effort = reasoningEffort(config);
  const bearer: Record<string, string> = config.apiKey ? { Authorization: `Bearer ${config.apiKey}` } : {};

  if (config.apiFormat === "anthropic") {
    return {
      url: `${base}/messages`,
      headers: { "Content-Type": "application/json", ...anthropicBrowserHeaders(config) },
      body: JSON.stringify(anthropicBody(config, plain, options.cache ?? true)),
    };
  }
  if (config.apiFormat === "openai-responses") {
    const body: Json = { model: config.model, input: plain.map(responsesInputItem), stream: true };
    if (effort) body.reasoning = { effort };
    if (config.maxOutputTokens) body.max_output_tokens = config.maxOutputTokens;
    return { url: `${base}/responses`, headers: { "Content-Type": "application/json", ...bearer }, body: JSON.stringify(body) };
  }
  const body: Json = { model: config.model, messages: plain.map(openaiMessage), stream: true };
  if (shouldRequestOpenAIStreamUsage(config)) body.stream_options = { include_usage: true };
  if (effort) body.reasoning_effort = effort;
  if (config.maxOutputTokens) {
    const official = config.baseUrl.toLowerCase().includes("api.openai.com");
    const field = isOpenAIReasoningModel(config.model) || (official && effort) ? "max_completion_tokens" : "max_tokens";
    body[field] = config.maxOutputTokens;
  }
  return { url: `${base}/chat/completions`, headers: { "Content-Type": "application/json", ...bearer }, body: JSON.stringify(body) };
}

// ── SSE parsing ──────────────────────────────────────────────────────────────

/** Go truncateErrorText: trimmed, at most 4 KiB of UTF-8, with an ellipsis when cut. */
export function truncateErrorText(text: string) {
  const trimmed = text.trim();
  const bytes = new TextEncoder().encode(trimmed);
  if (bytes.length <= ERROR_TEXT_LIMIT) return trimmed;
  let cut = ERROR_TEXT_LIMIT;
  while (cut > 0 && (bytes[cut]! & 0xc0) === 0x80) cut -= 1;
  return `${new TextDecoder().decode(bytes.subarray(0, cut))}…`;
}

/** Go streamErrorMessage: an `error` field may be an object with message/type, or a string. */
function streamErrorMessage(raw: unknown) {
  if (raw === undefined || raw === null) return "";
  if (typeof raw === "string") return raw.trim();
  if (typeof raw === "object" && !Array.isArray(raw)) {
    const { message, type } = raw as Json;
    if (typeof message === "string" && message.trim()) return message.trim();
    if (typeof type === "string" && type) return type;
  }
  return truncateErrorText(JSON.stringify(raw));
}

function isValidJson(text: string) {
  try {
    JSON.parse(text);
    return true;
  } catch {
    return false;
  }
}

function record(value: unknown): Json {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Json : {};
}

function text(value: unknown) {
  return typeof value === "string" ? value : "";
}

function count(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : 0;
}

interface TokenUsage {
  inputTokens: number;
  outputTokens: number;
  cachedInputTokens: number;
  cacheWriteTokens: number;
  reasoningTokens: number;
}

const emptyUsage = (): TokenUsage => ({ inputTokens: 0, outputTokens: 0, cachedInputTokens: 0, cacheWriteTokens: 0, reasoningTokens: 0 });

export interface SseParserOptions {
  /** Reported on the `usage` event (Go passes config.Model). */
  model?: string;
  /** OpenAI chat only: usage was requested, so the stream ends after the usage chunk. */
  usageRequested?: boolean;
}

export interface SseParser {
  /** Feeds decoded text; returns the events it completes. Input after the terminal event is ignored. */
  push(chunk: string): AiLoopStreamEvent[];
  /** The body ended: flushes what is left and returns the closing events (a `done` or `error`). */
  end(): AiLoopStreamEvent[];
  /** A terminal `done` or `error` event has been produced. */
  readonly finished: boolean;
}

interface FormatHandler {
  /** Handles one SSE data payload; returns true when the provider's terminal event arrived. */
  data(payload: Json, emit: (event: AiLoopStreamEvent) => void): boolean;
  /** Closing events after the terminal event or the end of the body. */
  finish(emit: (event: AiLoopStreamEvent) => void): void;
}

function usageEvent(usage: TokenUsage, model: string | undefined): AiLoopStreamEvent | undefined {
  if (!usage.inputTokens && !usage.outputTokens && !usage.cachedInputTokens && !usage.cacheWriteTokens && !usage.reasoningTokens) {
    return undefined;
  }
  return { type: "usage", ...usage, requestCount: 1, model };
}

function openaiHandler(options: SseParserOptions): FormatHandler {
  let upstreamError = "";
  let truncated = false;
  let finished = false;
  let usage = emptyUsage();
  return {
    data(payload, emit) {
      const errorMessage = streamErrorMessage(payload.error);
      if (errorMessage) {
        upstreamError = errorMessage;
        return true;
      }
      const rawUsage = record(payload.usage);
      const hasUsage = count(rawUsage.prompt_tokens) > 0 || count(rawUsage.completion_tokens) > 0;
      if (hasUsage) {
        usage = {
          ...emptyUsage(),
          inputTokens: count(rawUsage.prompt_tokens),
          outputTokens: count(rawUsage.completion_tokens),
          cachedInputTokens: count(record(rawUsage.prompt_tokens_details).cached_tokens),
          reasoningTokens: count(record(rawUsage.completion_tokens_details).reasoning_tokens),
        };
      }
      const choices = Array.isArray(payload.choices) ? payload.choices : [];
      // With usage requested, the usage chunk arrives on its own after finish_reason.
      if (!choices.length) return finished && hasUsage;
      const choice = record(choices[0]);
      const delta = record(choice.delta);
      if (text(delta.content)) emit({ type: "chunk", text: text(delta.content) });
      if (text(delta.reasoning_content)) emit({ type: "reasoning", text: text(delta.reasoning_content) });
      else if (text(delta.reasoning)) emit({ type: "reasoning", text: text(delta.reasoning) });
      const finishReason = text(choice.finish_reason);
      if (finishReason) {
        truncated = finishReason === "length";
        finished = true;
      }
      return finished && (!options.usageRequested || hasUsage);
    },
    finish(emit) {
      if (upstreamError) {
        emit({ type: "error", message: `上游返回错误: ${upstreamError}` });
        return;
      }
      if (truncated) emit({ type: "truncated" });
      const event = usageEvent(usage, options.model);
      if (event) emit(event);
      emit({ type: "done" });
    },
  };
}

function responsesHandler(options: SseParserOptions): FormatHandler {
  let truncated = false;
  let emittedText = false;
  let upstreamFailed = false;
  let upstreamError = "";
  let usage = emptyUsage();
  return {
    data(payload, emit) {
      const type = text(payload.type);
      const response = record(payload.response);
      if (type === "response.output_text.delta" && text(payload.delta)) {
        emittedText = true;
        emit({ type: "chunk", text: text(payload.delta) });
      }
      if (type === "response.completed" || type === "response.incomplete") {
        if (type === "response.incomplete") truncated = true;
        const rawUsage = record(response.usage);
        usage = {
          ...emptyUsage(),
          inputTokens: count(rawUsage.input_tokens),
          outputTokens: count(rawUsage.output_tokens),
          cachedInputTokens: count(record(rawUsage.input_tokens_details).cached_tokens),
          reasoningTokens: count(record(rawUsage.output_tokens_details).reasoning_tokens),
        };
        for (const item of Array.isArray(response.output) ? response.output : []) {
          if (emittedText) break;
          for (const content of Array.isArray(record(item).content) ? record(item).content as unknown[] : []) {
            const part = record(content);
            if (part.type === "output_text" && text(part.text)) {
              emit({ type: "chunk", text: text(part.text) });
              emittedText = true;
            }
          }
        }
      } else if (type === "response.failed" || type === "error") {
        upstreamFailed = true;
        upstreamError = streamErrorMessage(payload.error)
          || text(payload.message).trim()
          || text(record(response.error).message);
      }
      if (type === "response.completed" && response.status === "incomplete") truncated = true;
      return type === "response.completed" || type === "response.incomplete" || type === "response.failed" || type === "error";
    },
    finish(emit) {
      if (upstreamFailed) {
        emit({ type: "error", message: `Responses API 返回错误: ${upstreamError.trim() || "上游返回失败事件但未提供错误信息"}` });
        return;
      }
      if (truncated) emit({ type: "truncated" });
      if (!emittedText && !truncated) {
        emit({ type: "error", message: "Responses API 未返回可识别的文本或工具调用" });
        return;
      }
      const event = usageEvent(usage, options.model);
      if (event) emit(event);
      emit({ type: "done" });
    },
  };
}

function anthropicHandler(options: SseParserOptions): FormatHandler {
  const thinkingBlocks = new Map<number, { blockType: "thinking" | "redacted_thinking"; thinking: string; signature: string; data: string }>();
  let truncated = false;
  let upstreamError = "";
  const usage = emptyUsage();
  return {
    data(payload, emit) {
      const index = typeof payload.index === "number" ? payload.index : 0;
      const delta = record(payload.delta);
      switch (payload.type) {
        case "message_stop":
          return true;
        case "error":
          upstreamError = streamErrorMessage(payload.error) || "上游返回错误事件但未提供错误信息";
          return true;
        case "message_start": {
          const rawUsage = record(record(payload.message).usage);
          usage.cachedInputTokens = count(rawUsage.cache_read_input_tokens);
          usage.cacheWriteTokens = count(rawUsage.cache_creation_input_tokens);
          // Anthropic reports plain, cache-read and cache-write input separately; show the total.
          usage.inputTokens = count(rawUsage.input_tokens) + usage.cachedInputTokens + usage.cacheWriteTokens;
          break;
        }
        case "content_block_delta":
          if (delta.type === "text_delta" && text(delta.text)) emit({ type: "chunk", text: text(delta.text) });
          else if (delta.type === "thinking_delta" && text(delta.thinking)) {
            emit({ type: "reasoning", text: text(delta.thinking) });
            const block = thinkingBlocks.get(index);
            if (block) block.thinking += text(delta.thinking);
          } else if (delta.type === "signature_delta") {
            const block = thinkingBlocks.get(index);
            if (block) block.signature += text(delta.signature);
          }
          break;
        case "content_block_start": {
          const block = record(payload.content_block);
          if (block.type === "thinking") {
            thinkingBlocks.set(index, { blockType: "thinking", thinking: text(block.thinking), signature: text(block.signature), data: "" });
            if (text(block.thinking)) emit({ type: "reasoning", text: text(block.thinking) });
          } else if (block.type === "redacted_thinking") {
            thinkingBlocks.set(index, { blockType: "redacted_thinking", thinking: "", signature: "", data: text(block.data) });
          }
          break;
        }
        case "content_block_stop": {
          const block = thinkingBlocks.get(index);
          thinkingBlocks.delete(index);
          // Only signed (or encrypted) blocks can be replayed to Anthropic.
          if (block && (block.signature || block.data)) emit({ type: "thinking_block", ...block });
          break;
        }
        case "message_delta":
          usage.outputTokens = count(record(payload.usage).output_tokens);
          if (delta.stop_reason === "max_tokens") truncated = true;
          break;
      }
      return false;
    },
    finish(emit) {
      if (upstreamError) {
        emit({ type: "error", message: `Anthropic 返回错误: ${upstreamError}` });
        return;
      }
      if (truncated) emit({ type: "truncated" });
      const event = usageEvent(usage, options.model);
      if (event) emit(event);
      emit({ type: "done" });
    },
  };
}

/**
 * Incremental SSE parser for one provider response (Go forEachSSEDataLine plus the per-format
 * event handling). Multi-line `data:` fields join with `\n`; a complete JSON payload is dispatched
 * before the next `data:` line for proxies that omit the blank line between events; `[DONE]` or the
 * provider's own terminal event ends the stream. Adjacent text deltas from one `push` are merged.
 */
export function createSseParser(format: AiApiFormat, options: SseParserOptions = {}): SseParser {
  const handler = format === "anthropic"
    ? anthropicHandler(options)
    : format === "openai-responses"
      ? responsesHandler(options)
      : openaiHandler(options);
  let buffer = "";
  let pending: string[] = [];
  let pendingSize = 0;
  let stopped = false;
  let finished = false;
  let events: AiLoopStreamEvent[] = [];

  const emit = (event: AiLoopStreamEvent) => {
    const previous = events[events.length - 1];
    if ((event.type === "chunk" || event.type === "reasoning") && previous?.type === event.type) {
      events[events.length - 1] = { type: event.type, text: `${previous.text ?? ""}${event.text ?? ""}` };
      return;
    }
    events.push(event);
  };
  const fail = (message: string) => {
    stopped = true;
    finished = true;
    emit({ type: "error", message });
  };
  const close = () => {
    if (finished) return;
    finished = true;
    stopped = true;
    handler.finish(emit);
  };
  /** Dispatches the pending event; returns true when it was the provider's terminal event. */
  const dispatch = () => {
    if (!pending.length) return false;
    const data = pending.join("\n");
    pending = [];
    pendingSize = 0;
    let payload: unknown;
    try {
      payload = JSON.parse(data);
    } catch {
      return false;
    }
    return handler.data(record(payload), emit);
  };
  const line = (raw: string) => {
    const current = raw.endsWith("\r") ? raw.slice(0, -1) : raw;
    if (current === "") {
      if (dispatch()) close();
      return;
    }
    if (!current.startsWith("data:")) return;
    let data = current.slice("data:".length);
    if (data.startsWith(" ")) data = data.slice(1);
    if (data === "[DONE]") {
      dispatch();
      close();
      return;
    }
    if (pending.length && isValidJson(pending.join("\n")) && dispatch()) {
      close();
      return;
    }
    pendingSize += data.length;
    if (pendingSize > SSE_BUFFER_MAX) {
      fail(`AI 响应行超出上限(${SSE_BUFFER_MAX}B)`);
      return;
    }
    pending.push(data);
  };
  const take = () => {
    const result = events;
    events = [];
    return result;
  };

  return {
    push(chunk) {
      if (stopped) return [];
      buffer += chunk;
      let newline = buffer.indexOf("\n");
      while (newline >= 0 && !stopped) {
        line(buffer.slice(0, newline));
        buffer = buffer.slice(newline + 1);
        newline = buffer.indexOf("\n");
      }
      if (!stopped && buffer.length > SSE_BUFFER_MAX) fail(`AI 响应行超出上限(${SSE_BUFFER_MAX}B)`);
      return take();
    },
    end() {
      if (!stopped) {
        if (buffer) line(buffer);
        buffer = "";
        if (!stopped) dispatch();
        close();
      }
      return take();
    },
    get finished() {
      return finished;
    },
  };
}

/** Go parseModelList: `data` or `models`, entries as strings or objects with id/name; sorted, unique. */
export function parseModelList(raw: string): string[] {
  let payload: unknown;
  try {
    payload = JSON.parse(raw);
  } catch (error) {
    throw errorWithCause(`解析模型列表失败: ${error instanceof Error ? error.message : String(error)}`, error);
  }
  const root = record(payload);
  const candidates = root.data ?? root.models;
  if (candidates === undefined || candidates === null) return [];
  if (!Array.isArray(candidates)) throw new Error("解析模型列表失败: 模型列表不是数组");
  const names = new Set<string>();
  for (const entry of candidates) {
    const name = typeof entry === "string"
      ? entry.trim()
      : text(record(entry).id).trim() || text(record(entry).name).trim();
    if (name) names.add(name);
  }
  return [...names].sort();
}
