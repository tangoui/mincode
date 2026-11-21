import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import vm from "node:vm";
import { parsePluginUIMessageEvent, parsePluginUIMessageRequest, PLUGIN_UI_MAX_MESSAGE_BYTES } from "../src/domain/plugin-ui/protocol.ts";

async function loadTransport(features) {
  const themeImport = /^import \{ applyThemeTokens \} from '\.\/theme-tokens\.js';$/m;
  const themeTokens = (await readFile(new URL("../src/plugin-ui/theme-tokens.js", import.meta.url), "utf8"))
    .replaceAll("export function", "function");
  const transport = await readFile(new URL("../src/plugin-ui/transport.js", import.meta.url), "utf8");
  const source = themeTokens + transport.replace(themeImport, "").replace("export function", "function");
  const listeners = new Map();
  const parent = { postMessage() {} };
  const context = vm.createContext({
    URLSearchParams, ArrayBuffer, Uint8Array, TextEncoder, JSON,
    location: { hash: "#mincodeSession=s&nonce=n&generation=1" },
    parent, setTimeout, clearTimeout,
    addEventListener: (type, listener) => listeners.set(type, listener),
    removeEventListener: (type) => listeners.delete(type),
  });
  vm.runInContext(source, context);
  const errors = [];
  const api = context.createTransport({ onReady() {}, onError(message) { errors.push(message); }, applyContext() {} });
  const sent = [];
  const port = { start() {}, close() {}, postMessage(message, transfer) { sent.push({ message, transfer }); } };
  listeners.get("message")({
    source: parent,
    data: { type: "mincode.host.connect", protocolVersion: 1, sessionId: "s", nonce: "n", generation: 1, requestTimeoutMs: 30000, features },
    ports: [port],
  });
  return { api, port, sent, errors };
}

test("transport.postMessage sends plain JSON and settles on the host acknowledgement", async () => {
  const { api, port, sent } = await loadTransport({ tasks: 1, messages: 1 });
  assert.equal(api.messagesSupported(), true);
  const pending = api.postMessage({ command: "refresh", path: "a.txt", skip: undefined });
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(sent.length, 1);
  assert.equal(sent[0].message.type, "mincode.plugin.message");
  assert.equal(JSON.stringify(sent[0].message.message), '{"command":"refresh","path":"a.txt"}');
  port.onmessage({ data: { type: "mincode.host.response", id: sent[0].message.id, ok: true, result: true } });
  assert.equal(await pending, undefined);
});

test("transport.postMessage queues bursts one at a time instead of hitting the request bound", async () => {
  const { api, port, sent } = await loadTransport({ tasks: 1, messages: 1 });
  const posts = Array.from({ length: 30 }, (_, seq) => api.postMessage({ seq }));
  const settle = () => new Promise(resolve => setTimeout(resolve, 0));
  for (let seq = 0; seq < 30; seq++) {
    await settle();
    assert.equal(sent.length, seq + 1, "exactly one message in flight");
    assert.equal(sent[seq].message.message.seq, seq);
    port.onmessage({ data: { type: "mincode.host.response", id: sent[seq].message.id, ok: true, result: true } });
  }
  await Promise.all(posts);
});

test("transport.postMessage retries a busy process queue", async () => {
  const { api, port, sent } = await loadTransport({ tasks: 1, messages: 1 });
  const pending = api.postMessage({ a: 1 });
  await new Promise(resolve => setTimeout(resolve, 0));
  port.onmessage({ data: { type: "mincode.host.response", id: sent[0].message.id, ok: false, error: { code: "busy", message: "busy" } } });
  await new Promise(resolve => setTimeout(resolve, 80));
  assert.equal(sent.length, 2, "retried after the busy reply");
  port.onmessage({ data: { type: "mincode.host.response", id: sent[1].message.id, ok: true, result: true } });
  assert.equal(await pending, undefined);
});

test("transport.postMessage rejects values that are not JSON or too large", async () => {
  const { api, sent } = await loadTransport({ tasks: 1, messages: 1 });
  const cyclic = {};
  cyclic.self = cyclic;
  for (const value of [undefined, null, () => 1, cyclic]) {
    await assert.rejects(api.postMessage(value), (error) => error.code === "invalid-request");
  }
  await assert.rejects(api.postMessage("x".repeat(PLUGIN_UI_MAX_MESSAGE_BYTES)), (error) => error.code === "payload-too-large");
  assert.equal(sent.length, 0);
});

test("transport messages need the host feature", async () => {
  const { api, sent } = await loadTransport({ tasks: 1 });
  assert.equal(api.messagesSupported(), false);
  await assert.rejects(api.postMessage({ a: 1 }), (error) => error.code === "not-allowed");
  assert.equal(sent.length, 0);
});

test("transport.onMessage delivers host messages in order until unsubscribed", async () => {
  const { api, port, errors } = await loadTransport({ tasks: 1, messages: 1 });
  const received = [];
  const unsubscribe = api.onMessage((message) => received.push(message));
  api.onMessage(() => { throw new Error("listener failed"); });
  for (const seq of [1, 2]) port.onmessage({ data: { type: "mincode.host.message", message: { seq } } });
  unsubscribe();
  port.onmessage({ data: { type: "mincode.host.message", message: { seq: 3 } } });
  assert.deepEqual(received.map((message) => message.seq), [1, 2]);
  assert.equal(errors.length, 3, "a failing listener is reported, not fatal");
});

test("host parses message requests from plugin views", () => {
  assert.deepEqual(parsePluginUIMessageRequest({ type: "mincode.plugin.message", id: "request-1", message: { a: 1 } }),
    { type: "mincode.plugin.message", id: "request-1", message: { a: 1 } });
  assert.deepEqual(parsePluginUIMessageRequest({ type: "mincode.plugin.message", id: "request-1", message: 0 }),
    { type: "mincode.plugin.message", id: "request-1", message: 0 });
  for (const raw of [
    { type: "mincode.plugin.message", id: "request-1" },
    { type: "mincode.plugin.message", id: "request-1", message: null },
    { type: "mincode.plugin.message", id: "", message: 1 },
    { type: "mincode.plugin.message", id: "request-1", message: "x".repeat(PLUGIN_UI_MAX_MESSAGE_BYTES) },
    { type: "mincode.plugin.call", id: "request-1", message: 1 },
  ]) assert.equal(parsePluginUIMessageRequest(raw), undefined, JSON.stringify(raw).slice(0, 60));
});

test("host validates plugin:ui-message events", () => {
  assert.deepEqual(parsePluginUIMessageEvent({ pluginId: "p", generation: 2, viewId: "main", message: { a: 1 } }),
    { pluginId: "p", generation: 2, viewId: "main", message: { a: 1 } });
  assert.deepEqual(parsePluginUIMessageEvent({ pluginId: "p", generation: 2, viewId: "", message: "x" }),
    { pluginId: "p", generation: 2, viewId: "", message: "x" });
  for (const raw of [{ pluginId: "p", generation: 0, message: 1 }, { pluginId: "p", generation: 2 }, { generation: 2, message: 1 }, null]) {
    assert.equal(parsePluginUIMessageEvent(raw), undefined);
  }
});

test("the plugin frame serializes view messages and routes process messages by plugin, generation and view", async () => {
  const frame = await readFile(new URL("../src/components/plugins/PluginViewFrame.vue", import.meta.url), "utf8");
  const handler = frame.slice(frame.indexOf("async function handleProcessRequest"), frame.indexOf("function handleProcessBinary"));
  assert.match(handler, /postPluginUIMessage\(/);
  assert.match(handler, /messageQueue/);
  assert.doesNotMatch(handler, /confirmAction/);
  const push = frame.slice(frame.indexOf("function handleProcessMessage"), frame.indexOf("function handleTaskChanged"));
  assert.match(push, /event\.pluginId !== props\.pluginId/);
  assert.match(push, /event\.generation !== session\.generation/);
  assert.match(push, /event\.viewId && event\.viewId !== session\.viewId/);
  assert.match(push, /"mincode\.host\.message"/);
  assert.match(frame, /listenAppEvent<unknown>\("plugin:ui-message", handleProcessMessage\)/);
  assert.match(frame, /messages: 1/);
});
