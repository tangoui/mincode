import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import { readFile } from "node:fs/promises";
import test from "node:test";

// Browser-local AI chat: request building and SSE parsing (domain/ai/browser-stream.ts, ported from
// backend/ai/stream.go), the fetch stream (services/browser-runtime/ai.ts) and the browser
// branch of streamAiLoopTurn. globalThis.fetch is replaced per test with real Response/ReadableStream.

// ai-agent-loop imports desktop-bridge, which imports the Vite-only dev-performance helper.
const dataModule = (source) => `data:text/javascript,${encodeURIComponent(source)}`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/services/dev-performance") {
      return { url: dataModule("export const measureDevAsync = (_name, run) => run();"), shortCircuit: true };
    }
    return nextResolve(specifier, context);
  },
});

// Browser-local mode: no window.tango.
globalThis.window = {};

const {
  buildBrowserAiRequest,
  createSseParser,
  normalizeBaseURL,
  parseModelList,
} = await import("../src/domain/ai/browser-stream.ts");
const { streamBrowserAiTurn } = await import("../src/services/browser-runtime/ai.ts");
const { browserLocalFeatures, getBrowserHandler } = await import("../src/services/browser-runtime/registry.ts");
const { canStreamAi, streamAiLoopTurn } = await import("../src/services/ai-agent-loop.ts");
const { AI_STREAM_IDLE_TIMEOUT_MESSAGE, isAiStreamIdleTimeoutError } = await import("../src/domain/ai/stream-idle-watchdog.ts");

const realFetch = globalThis.fetch;
test.afterEach(() => {
  globalThis.fetch = realFetch;
});

const encoder = new TextEncoder();

function config(overrides = {}) {
  return { baseUrl: "https://api.example.test/v1", apiKey: "sk-test", model: "gpt-4o", apiFormat: "openai", ...overrides };
}

/** A streaming body that sends `chunks`, then closes, errors (`fail`) or stalls forever (`stall`). */
function streamBody(chunks, { fail, stall = false } = {}) {
  let index = 0;
  let cancelled = false;
  const body = new ReadableStream({
    // One chunk per read; the failure or stall happens only after every chunk was delivered.
    pull(controller) {
      if (index < chunks.length) {
        controller.enqueue(encoder.encode(chunks[index++]));
        return undefined;
      }
      if (fail) controller.error(fail);
      else if (stall) return new Promise(() => {});
      else controller.close();
      return undefined;
    },
    cancel() {
      cancelled = true;
    },
  });
  return { body, wasCancelled: () => cancelled };
}

function sseResponse(chunks, options = {}) {
  return new Response(streamBody(chunks, options).body, {
    status: 200,
    headers: { "content-type": "text/event-stream" },
  });
}

function mockFetch(respond) {
  const calls = [];
  globalThis.fetch = async (url, init) => {
    calls.push({ url, init, body: init?.body ? JSON.parse(init.body) : undefined });
    return respond(url, init, calls.length);
  };
  return calls;
}

const frame = (payload) => `data: ${JSON.stringify(payload)}\n\n`;

async function collect(run) {
  const events = [];
  let outcome;
  try {
    await run((event) => events.push(event));
    outcome = { ok: true };
  } catch (error) {
    outcome = { ok: false, error };
  }
  return { events, outcome };
}

// ── request building ─────────────────────────────────────────────────────────

test("normalizeBaseURL keeps a trailing version segment and otherwise appends /v1 (Go normalizeBaseURL)", () => {
  assert.equal(normalizeBaseURL("https://api.openai.com"), "https://api.openai.com/v1");
  assert.equal(normalizeBaseURL("https://api.openai.com/v1/"), "https://api.openai.com/v1");
  assert.equal(normalizeBaseURL("https://open.bigmodel.cn/api/paas/v4"), "https://open.bigmodel.cn/api/paas/v4");
  assert.equal(normalizeBaseURL("https://proxy.test/openai"), "https://proxy.test/openai/v1");
  assert.equal(normalizeBaseURL("https://proxy.test/v"), "https://proxy.test/v/v1", "a bare v is not a version");
  assert.equal(normalizeBaseURL("https://proxy.test/v1beta"), "https://proxy.test/v1beta/v1");
});

test("openai request: /chat/completions, bearer auth, Go body fields and no tools", () => {
  const request = buildBrowserAiRequest(
    config({ baseUrl: "https://api.openai.com", model: "gpt-5.1", reasoningEffort: "high", maxOutputTokens: 2048 }),
    [
      { role: "system", content: "be brief" },
      { role: "user", content: "hi", attachments: [{ id: "a", name: "notes.txt", mimeType: "text/plain", size: 3, kind: "text", data: "abc" }] },
    ],
  );
  assert.equal(request.url, "https://api.openai.com/v1/chat/completions");
  assert.deepEqual(request.headers, { "Content-Type": "application/json", Authorization: "Bearer sk-test" });
  const body = JSON.parse(request.body);
  assert.equal(body.model, "gpt-5.1");
  assert.equal(body.stream, true);
  assert.deepEqual(body.stream_options, { include_usage: true }, "auto mode asks the official endpoint for usage");
  assert.equal(body.reasoning_effort, "high");
  assert.equal(body.max_completion_tokens, 2048, "reasoning models take max_completion_tokens");
  assert.equal(body.max_tokens, undefined);
  assert.equal(body.tools, undefined);
  assert.equal(body.tool_choice, undefined);
  assert.deepEqual(body.messages, [
    { role: "system", content: "be brief" },
    { role: "user", content: "hi\n\n## 文本附件：notes.txt\n```text\nabc\n```" },
  ]);
});

test("openai request: proxies get max_tokens, no usage request in auto mode, and no auth header without a key", () => {
  const request = buildBrowserAiRequest(config({ apiKey: "", maxOutputTokens: 512 }), [{ role: "user", content: "hi" }]);
  const body = JSON.parse(request.body);
  assert.equal(body.max_tokens, 512);
  assert.equal(body.stream_options, undefined);
  assert.equal(body.reasoning_effort, undefined);
  assert.deepEqual(request.headers, { "Content-Type": "application/json" });
  const always = JSON.parse(buildBrowserAiRequest(config({ openaiStreamUsage: "always" }), [{ role: "user", content: "x" }]).body);
  assert.deepEqual(always.stream_options, { include_usage: true });
});

test("openai request: image attachments become image_url parts with a sniffed MIME type", () => {
  // 1x1 PNG declared as JPEG: Go re-detects the type from the bytes.
  const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==";
  const request = buildBrowserAiRequest(config(), [
    { role: "user", content: "", attachments: [
      { id: "i", name: "a.png", mimeType: "image/jpeg", size: 70, kind: "image", data: `data:image/png;base64,${png}` },
      { id: "x", name: "bad.png", mimeType: "image/png", size: 3, kind: "image", data: "bm90IGFuIGltYWdl" },
    ] },
  ]);
  const [message] = JSON.parse(request.body).messages;
  assert.deepEqual(message.content, [
    { type: "text", text: "请分析附件。" },
    { type: "image_url", image_url: { url: `data:image/png;base64,${png}`, detail: "auto" } },
  ]);
});

test("tool history is folded into plain text: the browser request never carries tool calls", () => {
  const request = buildBrowserAiRequest(config(), [
    { role: "user", content: "list" },
    { role: "assistant", content: "", toolCalls: [{ id: "c1", name: "list_dir", arguments: "{}" }] },
    { role: "tool", content: "a.txt", toolCallId: "c1" },
    { role: "assistant", content: "One file." },
    { role: "user", content: "thanks" },
  ]);
  const { messages } = JSON.parse(request.body);
  assert.deepEqual(messages.map((message) => message.role), ["user", "assistant", "user"]);
  assert.ok(messages.every((message) => !("tool_calls" in message) && !("tool_call_id" in message)));
  assert.match(messages[1].content, /list_dir[\s\S]*a\.txt[\s\S]*One file\./);
});

test("openai-responses request: /responses, input items, reasoning and max_output_tokens", () => {
  const request = buildBrowserAiRequest(
    config({ apiFormat: "openai-responses", reasoningEffort: "low", maxOutputTokens: 900 }),
    [
      { role: "system", content: "sys" },
      { role: "user", content: "hi" },
      { role: "assistant", content: "hello" },
      { role: "user", content: "continue" },
    ],
  );
  assert.equal(request.url, "https://api.example.test/v1/responses");
  assert.equal(request.headers.Authorization, "Bearer sk-test");
  const body = JSON.parse(request.body);
  assert.equal(body.stream, true);
  assert.deepEqual(body.reasoning, { effort: "low" });
  assert.equal(body.max_output_tokens, 900);
  assert.equal(body.tools, undefined);
  assert.deepEqual(body.input, [
    { type: "message", role: "system", content: [{ type: "input_text", text: "sys" }] },
    { type: "message", role: "user", content: [{ type: "input_text", text: "hi" }] },
    { type: "message", role: "assistant", content: [{ type: "output_text", text: "hello" }] },
    { type: "message", role: "user", content: [{ type: "input_text", text: "continue" }] },
  ]);
});

test("anthropic request: /messages, version and direct-browser headers, x-api-key by default", () => {
  const request = buildBrowserAiRequest(
    config({ apiFormat: "anthropic", baseUrl: "https://api.anthropic.com", model: "claude-3-7-sonnet-latest" }),
    [{ role: "system", content: "a" }, { role: "system", content: "b" }, { role: "user", content: "hi" }],
  );
  assert.equal(request.url, "https://api.anthropic.com/v1/messages");
  assert.deepEqual(request.headers, {
    "Content-Type": "application/json",
    "anthropic-version": "2023-06-01",
    "anthropic-dangerous-direct-browser-access": "true",
    "x-api-key": "sk-test",
  });
  const body = JSON.parse(request.body);
  assert.equal(body.max_tokens, 16384);
  assert.equal(body.stream, true);
  assert.equal(body.tools, undefined);
  assert.deepEqual(body.system, [{ type: "text", text: "a\n\nb", cache_control: { type: "ephemeral" } }]);
  assert.deepEqual(body.messages, [
    { role: "user", content: [{ type: "text", text: "hi", cache_control: { type: "ephemeral" } }] },
  ]);
  assert.equal(body.thinking, undefined);

  const uncached = JSON.parse(buildBrowserAiRequest(
    config({ apiFormat: "anthropic" }),
    [{ role: "system", content: "a" }, { role: "user", content: "hi" }],
    { cache: false },
  ).body);
  assert.equal(uncached.system, "a");
  assert.deepEqual(uncached.messages, [{ role: "user", content: "hi" }]);
});

test("anthropic request: bearer auth and thinking settings follow Go", () => {
  const bearer = buildBrowserAiRequest(
    config({ apiFormat: "anthropic", anthropicAuth: "bearer", model: "claude-sonnet-4-5", reasoningEffort: "medium" }),
    [{ role: "user", content: "hi" }],
  );
  assert.equal(bearer.headers.Authorization, "Bearer sk-test");
  assert.equal(bearer.headers["x-api-key"], undefined);
  const budget = JSON.parse(bearer.body);
  assert.deepEqual(budget.thinking, { type: "enabled", budget_tokens: 4096 });
  assert.equal(budget.max_tokens, 4096 + 16384);

  const adaptive = JSON.parse(buildBrowserAiRequest(
    config({ apiFormat: "anthropic", model: "claude-opus-4-6", reasoningEffort: "xhigh" }),
    [{ role: "user", content: "hi" }],
  ).body);
  assert.deepEqual(adaptive.thinking, { type: "adaptive", display: "summarized" });
  assert.deepEqual(adaptive.output_config, { effort: "high" });

  const noKey = buildBrowserAiRequest(config({ apiFormat: "anthropic", apiKey: "  " }), [{ role: "user", content: "hi" }]);
  assert.equal(noKey.headers["x-api-key"], undefined);
  assert.equal(noKey.headers.Authorization, undefined);
});

test("the context window budget trims old messages like Go and rejects an output budget that does not fit", () => {
  const long = "x".repeat(400); // ~108 tokens each
  const request = buildBrowserAiRequest(
    config({ contextWindow: 300, maxOutputTokens: 100 }),
    [
      { role: "system", content: "sys" },
      { role: "user", content: long },
      { role: "assistant", content: long },
      { role: "user", content: "latest" },
    ],
  );
  assert.deepEqual(JSON.parse(request.body).messages.map((message) => message.content), ["sys", long, "latest"]);
  assert.throws(
    () => buildBrowserAiRequest(config({ contextWindow: 100, maxOutputTokens: 100 }), [{ role: "user", content: "x" }]),
    /最大输出 Token 必须小于上下文窗口/,
  );
});

// ── SSE parsing ──────────────────────────────────────────────────────────────

function parseAll(format, chunks, options) {
  const parser = createSseParser(format, options);
  const events = chunks.flatMap((chunk) => parser.push(chunk));
  return [...events, ...parser.end()];
}

test("openai SSE: events split across chunks, CRLF, reasoning, usage after finish and [DONE]", () => {
  const stream = [
    frame({ choices: [{ delta: { reasoning_content: "think" } }] }),
    frame({ choices: [{ delta: { content: "Hel" } }] }),
    frame({ choices: [{ delta: { content: "lo" } }] }).replace(/\n/g, "\r\n"),
    frame({ choices: [{ delta: {}, finish_reason: "stop" }] }),
    frame({ choices: [], usage: { prompt_tokens: 11, completion_tokens: 7, prompt_tokens_details: { cached_tokens: 3 }, completion_tokens_details: { reasoning_tokens: 2 } } }),
    "data: [DONE]\n\n",
  ].join("");
  // Split at awkward places: inside "data:", inside JSON and between \r and \n.
  const chunks = [];
  for (let index = 0; index < stream.length; index += 7) chunks.push(stream.slice(index, index + 7));
  const events = parseAll("openai", chunks, { model: "gpt-4o", usageRequested: true });
  assert.deepEqual(events, [
    { type: "reasoning", text: "think" },
    { type: "chunk", text: "Hel" },
    { type: "chunk", text: "lo" },
    { type: "usage", inputTokens: 11, outputTokens: 7, cachedInputTokens: 3, cacheWriteTokens: 0, reasoningTokens: 2, requestCount: 1, model: "gpt-4o" },
    { type: "done" },
  ]);
});

test("openai SSE: adjacent deltas in one push are merged and `reasoning` is accepted", () => {
  const parser = createSseParser("openai");
  const events = parser.push([
    frame({ choices: [{ delta: { reasoning: "a" } }] }),
    frame({ choices: [{ delta: { reasoning: "b" } }] }),
    frame({ choices: [{ delta: { content: "x" } }] }),
    frame({ choices: [{ delta: { content: "y" } }] }),
  ].join(""));
  assert.deepEqual(events, [{ type: "reasoning", text: "ab" }, { type: "chunk", text: "xy" }]);
});

test("openai SSE: stops at finish_reason when usage was not requested, ignores trailing data, and reports truncation", () => {
  const parser = createSseParser("openai", { model: "m" });
  const events = parser.push([
    frame({ choices: [{ delta: { content: "cut" } }] }),
    frame({ choices: [{ delta: {}, finish_reason: "length" }] }),
    frame({ choices: [{ delta: { content: "ignored" } }] }),
  ].join(""));
  assert.deepEqual(events, [{ type: "chunk", text: "cut" }, { type: "truncated" }, { type: "done" }]);
  assert.deepEqual(parser.push(frame({ choices: [{ delta: { content: "late" } }] })), []);
  assert.deepEqual(parser.end(), []);
});

test("openai SSE: a provider error frame becomes a terminal error event", () => {
  const events = parseAll("openai", [
    frame({ choices: [{ delta: { content: "partial" } }] }),
    frame({ error: { message: "rate limited", type: "rate_limit" } }),
  ]);
  assert.deepEqual(events, [{ type: "chunk", text: "partial" }, { type: "error", message: "上游返回错误: rate limited" }]);
  assert.deepEqual(parseAll("openai", [frame({ error: "boom" })]), [{ type: "error", message: "上游返回错误: boom" }]);
});

test("openai SSE: a stream that ends without [DONE] still finishes, data without a trailing blank line included", () => {
  assert.deepEqual(parseAll("openai", [`data: ${JSON.stringify({ choices: [{ delta: { content: "end" } }] })}`]), [
    { type: "chunk", text: "end" },
    { type: "done" },
  ]);
  // Proxies that omit the blank line between events: complete JSON is dispatched before the next data line.
  assert.deepEqual(parseAll("openai", [
    `data: ${JSON.stringify({ choices: [{ delta: { content: "a" } }] })}\n`,
    `data: ${JSON.stringify({ choices: [{ delta: { content: "b" } }] })}\n`,
  ]), [{ type: "chunk", text: "a" }, { type: "chunk", text: "b" }, { type: "done" }]);
});

test("responses SSE: text deltas, completed usage and done; completed output fills in missing text", () => {
  assert.deepEqual(parseAll("openai-responses", [
    frame({ type: "response.output_text.delta", delta: "Hi" }),
    frame({ type: "response.output_text.delta", delta: "!" }),
    frame({ type: "response.completed", response: { status: "completed", usage: { input_tokens: 5, output_tokens: 2, input_tokens_details: { cached_tokens: 1 }, output_tokens_details: { reasoning_tokens: 1 } } } }),
  ], { model: "gpt-5" }), [
    { type: "chunk", text: "Hi" },
    { type: "chunk", text: "!" },
    { type: "usage", inputTokens: 5, outputTokens: 2, cachedInputTokens: 1, cacheWriteTokens: 0, reasoningTokens: 1, requestCount: 1, model: "gpt-5" },
    { type: "done" },
  ]);
  assert.deepEqual(parseAll("openai-responses", [
    frame({ type: "response.completed", response: { output: [{ type: "message", content: [{ type: "output_text", text: "only here" }] }] } }),
  ]), [{ type: "chunk", text: "only here" }, { type: "done" }]);
});

test("responses SSE: failure events, incomplete responses and empty output follow Go", () => {
  assert.deepEqual(parseAll("openai-responses", [
    frame({ type: "response.failed", response: { error: { message: "quota" } } }),
  ]), [{ type: "error", message: "Responses API 返回错误: quota" }]);
  assert.deepEqual(parseAll("openai-responses", [frame({ type: "error", message: "bad key" })]), [
    { type: "error", message: "Responses API 返回错误: bad key" },
  ]);
  assert.deepEqual(parseAll("openai-responses", [
    frame({ type: "response.output_text.delta", delta: "par" }),
    frame({ type: "response.incomplete", response: { status: "incomplete" } }),
  ]), [{ type: "chunk", text: "par" }, { type: "truncated" }, { type: "done" }]);
  assert.deepEqual(parseAll("openai-responses", [frame({ type: "response.completed", response: {} })]), [
    { type: "error", message: "Responses API 未返回可识别的文本或工具调用" },
  ]);
});

test("anthropic SSE: text, thinking, signed thinking blocks, usage, truncation and message_stop", () => {
  const events = parseAll("anthropic", [
    `event: message_start\n${frame({ type: "message_start", message: { usage: { input_tokens: 10, cache_read_input_tokens: 4, cache_creation_input_tokens: 2 } } })}`,
    frame({ type: "content_block_start", index: 0, content_block: { type: "thinking", thinking: "" } }),
    frame({ type: "content_block_delta", index: 0, delta: { type: "thinking_delta", thinking: "hmm" } }),
    frame({ type: "content_block_delta", index: 0, delta: { type: "signature_delta", signature: "sig" } }),
    frame({ type: "content_block_stop", index: 0 }),
    frame({ type: "content_block_start", index: 1, content_block: { type: "text", text: "" } }),
    frame({ type: "content_block_delta", index: 1, delta: { type: "text_delta", text: "Answer" } }),
    frame({ type: "message_delta", delta: { stop_reason: "max_tokens" }, usage: { output_tokens: 9 } }),
    frame({ type: "message_stop" }),
    frame({ type: "content_block_delta", index: 1, delta: { type: "text_delta", text: "after stop" } }),
  ], { model: "claude-x" });
  assert.deepEqual(events, [
    { type: "reasoning", text: "hmm" },
    { type: "thinking_block", blockType: "thinking", thinking: "hmm", signature: "sig", data: "" },
    { type: "chunk", text: "Answer" },
    { type: "truncated" },
    { type: "usage", inputTokens: 16, outputTokens: 9, cachedInputTokens: 4, cacheWriteTokens: 2, reasoningTokens: 0, requestCount: 1, model: "claude-x" },
    { type: "done" },
  ]);
});

test("anthropic SSE: an error event becomes a terminal error", () => {
  assert.deepEqual(parseAll("anthropic", [
    frame({ type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "a" } }),
    `event: error\n${frame({ type: "error", error: { type: "overloaded_error", message: "Overloaded" } })}`,
  ]), [{ type: "chunk", text: "a" }, { type: "error", message: "Anthropic 返回错误: Overloaded" }]);
});

test("parseModelList accepts data/models arrays of ids, names or strings, sorted and de-duplicated", () => {
  assert.deepEqual(parseModelList(JSON.stringify({ data: [{ id: "b" }, { id: "a" }, { name: "c" }, "a", { id: " " }] })), ["a", "b", "c"]);
  assert.deepEqual(parseModelList(JSON.stringify({ models: ["z"] })), ["z"]);
  assert.deepEqual(parseModelList(JSON.stringify({})), []);
  assert.throws(() => parseModelList("<html>"), /解析模型列表失败/);
});

// ── fetch stream ─────────────────────────────────────────────────────────────

test("registers the ai browser feature and the model-list and connection-test handlers at import time", () => {
  assert.ok(browserLocalFeatures.some((feature) => feature.id === "ai"
    && feature.titleKey === "browserLocal.feature.ai.title"
    && feature.limitKey === "browserLocal.feature.ai.limit"));
  assert.equal(typeof getBrowserHandler("ai_list_models"), "function");
  assert.equal(typeof getBrowserHandler("ai_test_connection"), "function");
  assert.equal(canStreamAi(), true, "browser mode can stream once the AI module is registered");
});

test("streamBrowserAiTurn posts the built request and forwards parsed events until done", async () => {
  const calls = mockFetch(() => sseResponse([
    frame({ choices: [{ delta: { content: "Hello" } }] }),
    frame({ choices: [{ delta: {}, finish_reason: "stop" }] }),
    "data: [DONE]\n\n",
  ]));
  const { events, outcome } = await collect((onEvent) => streamBrowserAiTurn(config(), [{ role: "user", content: "hi" }], onEvent));
  assert.deepEqual(outcome, { ok: true });
  assert.deepEqual(events, [{ type: "chunk", text: "Hello" }, { type: "done" }]);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, "https://api.example.test/v1/chat/completions");
  assert.equal(calls[0].init.method, "POST");
  assert.equal(calls[0].init.headers.Authorization, "Bearer sk-test");
  assert.ok(calls[0].init.signal instanceof AbortSignal);
});

test("a fetch TypeError (CORS refusal or unreachable host) becomes an error event with the cross-origin explanation", async () => {
  globalThis.fetch = async () => {
    throw new TypeError("Failed to fetch");
  };
  const { events, outcome } = await collect((onEvent) => streamBrowserAiTurn(config(), [{ role: "user", content: "hi" }], onEvent));
  assert.equal(events.length, 1);
  assert.equal(events[0].type, "error");
  assert.match(events[0].message, /跨域/);
  assert.equal(outcome.ok, false);
  assert.equal(outcome.error.message, events[0].message);
});

test("an HTTP error status becomes `HTTP <status>: <body>` like Go", async () => {
  mockFetch(() => new Response(JSON.stringify({ error: { message: "invalid key" } }), { status: 401 }));
  const { events, outcome } = await collect((onEvent) => streamBrowserAiTurn(config(), [{ role: "user", content: "hi" }], onEvent));
  assert.deepEqual(events, [{ type: "error", message: 'HTTP 401: {"error":{"message":"invalid key"}}' }]);
  assert.equal(outcome.ok, false);
});

test("a provider error event rejects after the error event is emitted", async () => {
  mockFetch(() => sseResponse([frame({ error: { message: "overloaded" } })]));
  const { events, outcome } = await collect((onEvent) => streamBrowserAiTurn(config(), [{ role: "user", content: "hi" }], onEvent));
  assert.deepEqual(events, [{ type: "error", message: "上游返回错误: overloaded" }]);
  assert.equal(outcome.error.message, "上游返回错误: overloaded");
});

test("a network drop mid-stream emits an error event and rejects instead of hanging", async () => {
  mockFetch(() => sseResponse([frame({ choices: [{ delta: { content: "par" } }] })], { fail: new TypeError("network error") }));
  const { events, outcome } = await collect((onEvent) => streamBrowserAiTurn(config(), [{ role: "user", content: "hi" }], onEvent));
  assert.equal(events[0].type, "chunk");
  assert.equal(events.at(-1).type, "error");
  assert.match(events.at(-1).message, /中断/);
  assert.equal(events.filter((event) => event.type === "error" || event.type === "done").length, 1);
  assert.equal(outcome.ok, false);
});

test("stopping mid-stream emits done and resolves even when the body ignores the abort", async () => {
  const stalled = streamBody([frame({ choices: [{ delta: { content: "partial" } }] })], { stall: true });
  mockFetch(() => new Response(stalled.body, { status: 200 }));
  const controller = new AbortController();
  const events = [];
  const run = streamBrowserAiTurn(config(), [{ role: "user", content: "hi" }], (event) => {
    events.push(event);
    if (event.type === "chunk") controller.abort();
  }, controller.signal);
  await run;
  assert.deepEqual(events, [{ type: "chunk", text: "partial" }, { type: "done" }]);
  assert.equal(stalled.wasCancelled(), true, "the body reader is released");
});

test("stopping while the request is still connecting emits done even if fetch never settles", async () => {
  globalThis.fetch = () => new Promise(() => {});
  const controller = new AbortController();
  const events = [];
  const run = streamBrowserAiTurn(config(), [{ role: "user", content: "hi" }], (event) => events.push(event), controller.signal);
  setTimeout(() => controller.abort(), 5);
  await run;
  assert.deepEqual(events, [{ type: "done" }]);
});

test("the idle watchdog ends a silent stream with the idle-timeout error", async () => {
  mockFetch(() => new Response(streamBody([frame({ choices: [{ delta: { content: "a" } }] })], { stall: true }).body));
  const { events, outcome } = await collect((onEvent) => streamBrowserAiTurn(config(), [{ role: "user", content: "hi" }], onEvent, undefined, 30));
  assert.deepEqual(events, [{ type: "chunk", text: "a" }, { type: "error", message: AI_STREAM_IDLE_TIMEOUT_MESSAGE }]);
  assert.equal(isAiStreamIdleTimeoutError(outcome.error), true);
});

test("a too-large output budget is reported as an error event before any request", async () => {
  const calls = mockFetch(() => sseResponse([]));
  const { events, outcome } = await collect((onEvent) => streamBrowserAiTurn(
    config({ contextWindow: 10, maxOutputTokens: 10 }), [{ role: "user", content: "hi" }], onEvent,
  ));
  assert.deepEqual(events, [{ type: "error", message: "最大输出 Token 必须小于上下文窗口" }]);
  assert.equal(outcome.ok, false);
  assert.equal(calls.length, 0);
});

test("anthropic retries once without cache_control when a gateway rejects it", async () => {
  const calls = mockFetch((_url, _init, attempt) => attempt === 1
    ? new Response("cache_control: Extra inputs are not permitted", { status: 400 })
    : sseResponse([frame({ type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "ok" } }), frame({ type: "message_stop" })]));
  const { events, outcome } = await collect((onEvent) => streamBrowserAiTurn(
    config({ apiFormat: "anthropic" }), [{ role: "system", content: "s" }, { role: "user", content: "hi" }], onEvent,
  ));
  assert.deepEqual(outcome, { ok: true });
  assert.deepEqual(events, [{ type: "chunk", text: "ok" }, { type: "done" }]);
  assert.equal(calls.length, 2);
  assert.ok(Array.isArray(calls[0].body.system));
  assert.equal(calls[1].body.system, "s");
});

// ── streamAiLoopTurn browser branch ──────────────────────────────────────────

test("browser mode: streamAiLoopTurn streams through fetch and never sends tools", async () => {
  const calls = mockFetch(() => sseResponse([frame({ choices: [{ delta: { content: "ok" }, finish_reason: "stop" }] })]));
  const tools = [{ name: "http_send_request", description: "send", inputSchema: { type: "object" } }];
  const events = [];
  await streamAiLoopTurn(
    config({ noToolModels: [] }),
    [{ role: "user", content: "hi" }, { role: "assistant", content: "", toolCalls: [{ id: "c", name: "http_send_request", arguments: "{}" }] }, { role: "tool", content: "r", toolCallId: "c" }, { role: "user", content: "go" }],
    tools,
    (event) => events.push(event),
  );
  assert.deepEqual(events, [{ type: "chunk", text: "ok" }, { type: "done" }]);
  assert.equal(calls.length, 1);
  const body = calls[0].body;
  assert.equal(body.tools, undefined);
  assert.equal(body.tool_choice, undefined);
  assert.ok(!JSON.stringify(body).includes("tool_calls"));
  assert.equal(body.noToolModels, undefined);
});

test("browser mode: stopping streamAiLoopTurn rejects with AbortError like the desktop stream", async () => {
  mockFetch(() => new Response(streamBody([frame({ choices: [{ delta: { content: "x" } }] })], { stall: true }).body));
  const controller = new AbortController();
  await assert.rejects(
    streamAiLoopTurn(config(), [{ role: "user", content: "hi" }], [], () => controller.abort(), controller.signal),
    (error) => error.name === "AbortError",
  );
});

// ── handlers ─────────────────────────────────────────────────────────────────

test("ai_list_models fetches /models with the format's auth headers and parses the list", async () => {
  const calls = mockFetch(() => new Response(JSON.stringify({ data: [{ id: "claude-b" }, { id: "claude-a" }] })));
  const models = await getBrowserHandler("ai_list_models")({
    config: { baseUrl: "https://api.anthropic.com", apiKey: "k", apiFormat: "anthropic", anthropicAuth: "x-api-key" },
  });
  assert.deepEqual(models, ["claude-a", "claude-b"]);
  assert.equal(calls[0].url, "https://api.anthropic.com/v1/models");
  assert.equal(calls[0].init.method, "GET");
  assert.deepEqual(calls[0].init.headers, {
    Accept: "application/json",
    "anthropic-version": "2023-06-01",
    "anthropic-dangerous-direct-browser-access": "true",
    "x-api-key": "k",
  });
});

test("ai_list_models reports CORS refusals, HTTP errors and invalid base URLs", async () => {
  const handler = getBrowserHandler("ai_list_models");
  globalThis.fetch = async () => {
    throw new TypeError("Failed to fetch");
  };
  await assert.rejects(handler({ config: config() }), /跨域/);
  mockFetch(() => new Response("nope", { status: 503 }));
  await assert.rejects(handler({ config: config() }), /获取模型列表失败（HTTP 503）/);
  await assert.rejects(handler({ config: config({ baseUrl: "" }) }), /请先填写 AI 服务地址/);
  await assert.rejects(handler({ config: config({ baseUrl: "https://x.test/v1?key=1" }) }), /不能包含查询参数或片段/);
  await assert.rejects(handler({ config: config({ baseUrl: "ftp://x.test" }) }), /仅支持 HTTP 或 HTTPS/);
});

test("ai_test_connection streams a one-line prompt and reports failures with Go's prefix", async () => {
  const handler = getBrowserHandler("ai_test_connection");
  const calls = mockFetch(() => sseResponse([frame({ choices: [{ delta: { content: "OK" }, finish_reason: "stop" }] })]));
  assert.equal(await handler({ config: config() }), true);
  assert.deepEqual(calls[0].body.messages, [{ role: "user", content: "Reply with OK." }]);
  await assert.rejects(handler({ config: config({ model: " " }) }), /请先选择或填写 AI 模型/);
  mockFetch(() => new Response("denied", { status: 403 }));
  await assert.rejects(handler({ config: config() }), /AI 连接测试失败: HTTP 403: denied/);
});

// ── drawer: browser mode is plain chat ───────────────────────────────────────

test("the assistant offers no native tools, MCP servers or approval controls in browser-local mode", async () => {
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  assert.match(drawer, /const browserLocalAi = isBrowserLocalMode\(\);/);
  assert.match(drawer, /const nativeTools = browserLocalAi \|\| isolatedExternal\s*\?\s*\[\]/);
  assert.match(drawer, /const mcpServerIds = isolatedExternal \|\| browserLocalAi\s*\?\s*\[\]/);
  assert.match(drawer, /const agentTools = computed\(\(\) => \(browserLocalAi \? \[\] : capabilityTools\.value\)\);/);
  assert.match(drawer, /v-if="assistant\.mode === 'agentic' && !browserLocalAi"/);
  // Both model entry points use the AI availability check instead of the desktop-only gate.
  assert.equal((drawer.match(/if \(!canStreamAi\(\)\)/g) ?? []).length, 2);
});

test("browser-local mode uses a plain-chat system prompt and empty-state hint; desktop text is unchanged", async () => {
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  const zh = await readFile(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8");
  const en = await readFile(new URL("../src/i18n/locales/en.ts", import.meta.url), "utf8");
  const prompt = drawer.slice(drawer.indexOf("const baseSystemPrompt = "), drawer.indexOf("const externalSource = "));
  const [browserPrompt, desktopPrompts] = [prompt.slice(0, prompt.indexOf(": runProjectRoot")), prompt.slice(prompt.indexOf(": runProjectRoot"))];
  assert.match(browserPrompt, /^const baseSystemPrompt = browserLocalAi\s*\?\s*"You are an assistant in a plain chat without tools/);
  assert.doesNotMatch(browserPrompt, /task_complete|MCP|workspace/i);
  // The desktop prompts still describe workspace/MCP tools and task_complete.
  assert.match(desktopPrompts, /Use workspace or MCP tools only when/);
  assert.equal((desktopPrompts.match(/call task_complete exactly once/g) ?? []).length, 2);
  assert.match(drawer, /browserLocalAi \? t\('ai\.agentic\.browserHint'\) : t\('ai\.agentic\.hint'\)/);
  assert.match(zh, /"ai\.agentic\.browserHint":\s*"浏览器本地模式下 AI 助手只能纯聊天/);
  assert.match(en, /"ai\.agentic\.browserHint":\s*"In browser-local mode the assistant is a plain chat/);
});
