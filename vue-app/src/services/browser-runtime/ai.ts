/**
 * 浏览器本地模式的 AI 纯对话：fetch 直连服务商，代替 backend/ai 的流式对话、
 * `ai_list_models` 与 `ai_test_connection`（这是「AI 经 Go 后端执行」规则的浏览器模式例外）。
 *
 * - 请求体与 SSE 解析在 `domain/ai/browser-stream.ts`（与 Go stream.go 字段一致），本模块只负责
 *   fetch、取消、空闲看门狗与错误说明；只发纯对话，从不携带工具定义。
 * - 取消（用户停止）：发出 `done` 并结束；服务商错误、断网、超时：先发 `error` 事件再拒绝 Promise，
 *   与桌面端事件契约一致。每个 await 都与中止信号赛跑，fetch 或响应体不理会中止也不会挂起。
 * - fetch 抛 TypeError（服务商不允许跨域或网络不可达）时给出 `ai.browser.corsError` 说明。
 * - API Key 由设置页存在 sessionStorage（见 services/browser-ai-keys.ts），这里只随请求发出。
 *
 * 只能经 `services/browser-runtime/index.ts` 静态导入；`streamAiLoopTurn` 通过动态 `import()` 使用它。
 */
import { normalizeAnthropicAuth } from "@/domain/ai/anthropic-auth";
import { normalizeAiApiFormat } from "@/domain/ai/api-format";
import {
  aiModelsEndpoint,
  anthropicBrowserHeaders,
  buildBrowserAiRequest,
  createSseParser,
  errorWithCause,
  parseModelList,
  shouldRequestOpenAIStreamUsage,
  truncateErrorText,
  validateAiBaseUrl,
  type BrowserAiRequest,
} from "@/domain/ai/browser-stream";
import {
  AI_STREAM_IDLE_TIMEOUT_MESSAGE,
  AiStreamIdleTimeoutError,
  createAiStreamIdleWatchdog,
} from "@/domain/ai/stream-idle-watchdog";
import { useVueI18n } from "@/i18n/messages";
import type { AiLoopConfig, AiLoopMessage, AiLoopStreamEvent } from "@/services/ai-agent-loop";
import { registerBrowserHandler, registerBrowserLocalFeature } from "./registry";

/** Go `aiErrorBodyReadLimit`. */
const ERROR_BODY_READ_LIMIT = 64 * 1024;
/** Go `maxModelListResponseSize`. */
const MODEL_LIST_MAX_BYTES = 1 << 20;
const MODEL_LIST_TIMEOUT_MS = 30_000;
const CONNECTION_TEST_TIMEOUT_MS = 30_000;

/** Reads at most `limit` bytes of a body; `truncated` reports that more was available. */
async function readLimited(response: Response, limit: number, guard: <T>(promise: Promise<T>) => Promise<T>) {
  if (!response.body) {
    const bytes = new Uint8Array(await guard(response.arrayBuffer()));
    return { bytes: bytes.subarray(0, limit), truncated: bytes.length > limit };
  }
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    while (total <= limit) {
      const { done, value } = await guard(reader.read());
      if (done) break;
      chunks.push(value);
      total += value.byteLength;
    }
  } finally {
    void reader.cancel().catch(() => undefined);
  }
  const bytes = new Uint8Array(Math.min(total, limit));
  let offset = 0;
  for (const chunk of chunks) {
    if (offset >= bytes.length) break;
    const part = chunk.subarray(0, bytes.length - offset);
    bytes.set(part, offset);
    offset += part.length;
  }
  return { bytes, truncated: total > limit };
}

/** Go aiPostStream's failure text: `HTTP <status>: <body>`. */
async function httpErrorMessage(response: Response, guard: <T>(promise: Promise<T>) => Promise<T>) {
  const { bytes } = await readLimited(response, ERROR_BODY_READ_LIMIT, guard);
  return `HTTP ${response.status}: ${truncateErrorText(new TextDecoder().decode(bytes))}`;
}

/** Wraps an abortable operation so every await rejects as soon as `signal` aborts. */
function abortGuard(signal: AbortSignal) {
  const stopped = new Promise<never>((_resolve, reject) => {
    if (signal.aborted) reject(signal.reason);
    else signal.addEventListener("abort", () => reject(signal.reason), { once: true });
  });
  void stopped.catch(() => undefined);
  return <T>(promise: Promise<T>) => Promise.race([promise, stopped]);
}

/** Errors whose text is already final (provider or Go-equivalent messages). */
class BrowserAiStreamError extends Error {}

/**
 * Streams one plain-chat turn from the browser. Resolves after `done` (also when `signal` stops the
 * turn); rejects after emitting `error` for provider, network and idle-timeout failures — the idle
 * timeout rejects with `AiStreamIdleTimeoutError`, like the desktop watchdog.
 */
export async function streamBrowserAiTurn(
  config: AiLoopConfig,
  messages: readonly AiLoopMessage[],
  onEvent: (event: AiLoopStreamEvent) => void,
  signal?: AbortSignal,
  idleTimeoutMs?: number,
): Promise<void> {
  let terminal: AiLoopStreamEvent | undefined;
  const emit = (event: AiLoopStreamEvent) => {
    if (terminal) return;
    if (event.type === "done" || event.type === "error") terminal = event;
    onEvent(event);
  };
  if (signal?.aborted) {
    emit({ type: "done" });
    return;
  }

  const { t } = useVueI18n();
  const controller = new AbortController();
  let timedOut = false;
  const watchdog = createAiStreamIdleWatchdog(() => {
    timedOut = true;
    controller.abort(new AiStreamIdleTimeoutError());
  }, idleTimeoutMs);
  void watchdog.expired.catch(() => undefined);
  const forwardAbort = () => controller.abort(signal?.reason);
  signal?.addEventListener("abort", forwardAbort, { once: true });
  const guard = abortGuard(controller.signal);

  let phase: "connect" | "read" = "connect";
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  const post = async (request: BrowserAiRequest) => guard(fetch(request.url, {
    method: "POST",
    headers: request.headers,
    body: request.body,
    signal: controller.signal,
  }));
  try {
    let response = await post(buildBrowserAiRequest(config, messages));
    if (!response.ok) {
      const message = await httpErrorMessage(response, guard);
      // Go: some Anthropic-compatible gateways reject cache_control; retry once without it.
      if (config.apiFormat !== "anthropic" || response.status !== 400 || !message.includes("cache_control")) {
        throw new BrowserAiStreamError(message);
      }
      response = await post(buildBrowserAiRequest(config, messages, { cache: false }));
      if (!response.ok) throw new BrowserAiStreamError(await httpErrorMessage(response, guard));
    }
    watchdog.touch();
    phase = "read";
    const parser = createSseParser(config.apiFormat, {
      model: config.model,
      usageRequested: config.apiFormat === "openai" && shouldRequestOpenAIStreamUsage(config),
    });
    const decoder = new TextDecoder();
    const deliver = (events: AiLoopStreamEvent[]) => events.forEach(emit);
    if (!response.body) {
      deliver(parser.push(await guard(response.text())));
    } else {
      reader = response.body.getReader();
      // Stop at the provider's terminal event instead of waiting for the server to close (Go does the same).
      while (!parser.finished) {
        const { done, value } = await guard(reader.read());
        if (done) break;
        watchdog.touch();
        deliver(parser.push(decoder.decode(value, { stream: true })));
      }
      if (!parser.finished) deliver(parser.push(decoder.decode()));
    }
    deliver(parser.end());
    if (terminal?.type === "error") throw new BrowserAiStreamError(terminal.message ?? "AI stream failed");
  } catch (error) {
    if (signal?.aborted) {
      emit({ type: "done" });
      return;
    }
    if (timedOut) {
      emit({ type: "error", message: AI_STREAM_IDLE_TIMEOUT_MESSAGE });
      throw new AiStreamIdleTimeoutError();
    }
    const message = error instanceof BrowserAiStreamError
      ? error.message
      : error instanceof TypeError
        ? t(phase === "connect" ? "ai.browser.corsError" : "ai.browser.streamInterrupted")
        : error instanceof Error && error.message ? error.message : String(error);
    emit({ type: "error", message });
    throw errorWithCause(message, error);
  } finally {
    watchdog.dispose();
    signal?.removeEventListener("abort", forwardAbort);
    if (reader) void reader.cancel().catch(() => undefined);
  }
}

/** The connection fields the settings page sends (`AiModelFetchConfig` / `AiConnectionTestConfig`). */
function requestConfig(value: unknown): AiLoopConfig {
  const raw = (value && typeof value === "object" ? value : {}) as Record<string, unknown>;
  const text = (field: string) => (typeof raw[field] === "string" ? raw[field] as string : "");
  const number = (field: string) => (typeof raw[field] === "number" ? raw[field] as number : undefined);
  return {
    baseUrl: text("baseUrl"),
    apiKey: text("apiKey"),
    model: text("model"),
    apiFormat: normalizeAiApiFormat(raw.apiFormat),
    anthropicAuth: normalizeAnthropicAuth(raw.anthropicAuth),
    reasoningEffort: text("reasoningEffort") || undefined,
    contextWindow: number("contextWindow"),
    maxInputTokens: number("maxInputTokens"),
    maxOutputTokens: number("maxOutputTokens"),
    openaiStreamUsage: raw.openaiStreamUsage === "always" || raw.openaiStreamUsage === "never" ? raw.openaiStreamUsage : "auto",
  };
}

/** Runs `operation` with a timeout and the caller's signal; returns whether the timeout fired. */
async function withTimeout<T>(timeoutMs: number, signal: AbortSignal | undefined, operation: (signal: AbortSignal) => Promise<T>) {
  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);
  const forwardAbort = () => controller.abort(signal?.reason);
  signal?.addEventListener("abort", forwardAbort, { once: true });
  try {
    const value = await operation(controller.signal);
    return { value, timedOut };
  } catch (error) {
    if (timedOut) return { value: undefined, timedOut };
    throw error;
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener("abort", forwardAbort);
  }
}

/** `ai_list_models` (Go aiListModelsHandler): GET `<base>/models`, sorted unique ids. */
async function listBrowserAiModels(args: Record<string, unknown>, signal?: AbortSignal): Promise<string[]> {
  const { t } = useVueI18n();
  const config = requestConfig(args.config);
  const endpoint = aiModelsEndpoint(config.baseUrl);
  validateAiBaseUrl(config.baseUrl);
  const headers: Record<string, string> = { Accept: "application/json" };
  if (config.apiFormat === "anthropic") Object.assign(headers, anthropicBrowserHeaders(config));
  else if (config.apiKey.trim()) headers.Authorization = `Bearer ${config.apiKey}`;

  const { value, timedOut } = await withTimeout(MODEL_LIST_TIMEOUT_MS, signal, async (requestSignal) => {
    const guard = abortGuard(requestSignal);
    let response: Response;
    try {
      response = await guard(fetch(endpoint, { method: "GET", headers, signal: requestSignal }));
    } catch (error) {
      if (signal?.aborted || requestSignal.aborted) throw error;
      throw error instanceof TypeError ? errorWithCause(t("ai.browser.corsError"), error) : error;
    }
    return { status: response.status, ...(await readLimited(response, MODEL_LIST_MAX_BYTES, guard)) };
  });
  if (timedOut || !value) throw new Error(t("ai.browser.timeout"));
  if (value.status < 200 || value.status >= 300) throw new Error(`获取模型列表失败（HTTP ${value.status}）`);
  if (value.truncated) throw new Error("模型列表响应过大");
  return parseModelList(new TextDecoder().decode(value.bytes));
}

/** `ai_test_connection` (Go aiTestConnectionHandler): one short streamed request within 30 seconds. */
async function testBrowserAiConnection(args: Record<string, unknown>, signal?: AbortSignal): Promise<boolean> {
  const { t } = useVueI18n();
  const config = requestConfig(args.config);
  validateAiBaseUrl(config.baseUrl);
  if (!config.model.trim()) throw new Error("请先选择或填写 AI 模型");
  let result: { timedOut: boolean };
  try {
    result = await withTimeout(CONNECTION_TEST_TIMEOUT_MS, signal, (requestSignal) =>
      streamBrowserAiTurn(config, [{ role: "user", content: "Reply with OK." }], () => undefined, requestSignal));
  } catch (error) {
    throw errorWithCause(`AI 连接测试失败: ${error instanceof Error ? error.message : String(error)}`, error);
  }
  // A stopped stream resolves; only a completed one proves the connection.
  if (result.timedOut) throw new Error(`AI 连接测试失败: ${t("ai.browser.timeout")}`);
  signal?.throwIfAborted();
  return true;
}

registerBrowserHandler("ai_list_models", listBrowserAiModels);
registerBrowserHandler("ai_test_connection", testBrowserAiConnection);
registerBrowserLocalFeature({
  id: "ai",
  titleKey: "browserLocal.feature.ai.title",
  limitKey: "browserLocal.feature.ai.limit",
});
