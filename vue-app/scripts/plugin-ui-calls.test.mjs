import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import vm from "node:vm";
import {
  isPluginUIMethod,
  parsePluginUIBinaryRequest,
  parsePluginUICallRequest,
  PLUGIN_UI_MAX_BINARY_BYTES,
} from "../src/domain/plugin-ui/protocol.ts";

// A minimal WebSocket double: the test opens, closes and feeds it.
class FakeSocket {
  static instances = [];
  constructor(url) {
    this.url = url;
    this.binaryType = "blob";
    this.readyState = 0;
    this.sent = [];
    FakeSocket.instances.push(this);
  }
  send(data) { this.sent.push(new Uint8Array(data)); }
  close() { this.readyState = 3; this.onclose?.({}); }
  open() { this.readyState = 1; this.onopen?.({}); }
  receive(bytes) { this.onmessage?.({ data: bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) }); }
}
FakeSocket.OPEN = 1;

async function loadTransport(features, { socket = false, clone = false } = {}) {
  const themeImport = /^import \{ applyThemeTokens \} from '\.\/theme-tokens\.js';$/m;
  const themeTokens = (await readFile(new URL("../src/plugin-ui/theme-tokens.js", import.meta.url), "utf8"))
    .replaceAll("export function", "function");
  const transport = await readFile(new URL("../src/plugin-ui/transport.js", import.meta.url), "utf8");
  const source = themeTokens + transport.replace(themeImport, "").replace("export function", "function");
  const listeners = new Map();
  const parent = { postMessage() {} };
  const context = vm.createContext({
    URLSearchParams, ArrayBuffer, Uint8Array, TextEncoder, TextDecoder,
    ...(socket ? { WebSocket: FakeSocket } : {}),
    location: { hash: "#mincodeSession=s&nonce=n&generation=1" },
    parent, setTimeout, clearTimeout,
    addEventListener: (type, listener) => listeners.set(type, listener),
    removeEventListener: (type) => listeners.delete(type),
  });
  vm.runInContext(source, context);
  const errors = [];
  const api = context.createTransport({ onReady() {}, onError(message) { errors.push(message); }, applyContext() {} });
  const sent = [];
  // clone: copy like a real MessagePort does, so values it cannot clone (Vue reactive proxies) throw DataCloneError.
  const port = { start() {}, close() {}, postMessage(message, transfer) { sent.push({ message: clone ? structuredClone(message) : message, transfer }); } };
  listeners.get("message")({
    source: parent,
    data: { type: "mincode.host.connect", protocolVersion: 1, sessionId: "s", nonce: "n", generation: 1, requestTimeoutMs: 30000, features },
    ports: [port],
  });
  return { api, port, sent, errors };
}

test("transport.request sends plugin.call and resolves with the host response", async () => {
  const { api, port, sent } = await loadTransport({ tasks: 1, calls: 1 }, { clone: true });
  const pending = api.request("files/list", { prefix: "a" });
  assert.equal(sent.length, 1);
  assert.equal(sent[0].message.type, "mincode.plugin.call");
  assert.equal(sent[0].message.method, "files/list");
  assert.deepEqual(sent[0].message.params, { prefix: "a" });
  port.onmessage({ data: { type: "mincode.host.response", id: sent[0].message.id, ok: true, result: ["a.txt"] } });
  assert.deepEqual(await pending, ["a.txt"]);

  for (const method of ["plugin.invoke", "host.shell.exec", "../x", ""]) {
    await assert.rejects(api.request(method), (error) => error.code === "invalid-request", method);
  }
  assert.equal(sent.length, 1);
});

test("transport.request and binary need the host features", async () => {
  const { api, sent } = await loadTransport({ tasks: 1 });
  await assert.rejects(api.request("files/list"), (error) => error.code === "not-allowed");
  assert.equal(api.binary.supported(), false);
  await assert.rejects(api.binary.send("chan", new Uint8Array([1])), (error) => error.code === "not-allowed");
  assert.equal(sent.length, 0);
});

test("transport.binary transfers a copy and delivers host frames to listeners", async () => {
  const { api, port, sent, errors } = await loadTransport({ tasks: 1, binary: 1 });
  assert.equal(api.binary.supported(), true);
  const bytes = new Uint8Array([1, 2, 3]);
  const pending = api.binary.send("pty/1", bytes);
  assert.equal(sent[0].message.type, "mincode.plugin.binary");
  assert.equal(sent[0].message.channel, "pty/1");
  assert.equal(sent[0].transfer.length, 1);
  assert.equal(sent[0].transfer[0], sent[0].message.data);
  assert.notEqual(sent[0].message.data, bytes.buffer, "the caller's buffer is not transferred");
  port.onmessage({ data: { type: "mincode.host.response", id: sent[0].message.id, ok: true, result: true } });
  assert.equal(await pending, true);
  await assert.rejects(api.binary.send("bad channel", bytes), (error) => error.code === "invalid-request");

  const received = [];
  const unsubscribe = api.binary.onMessage((channel, data) => received.push([channel, [...data]]));
  port.onmessage({ data: { type: "mincode.host.binary", channel: "out", data: new Uint8Array([9, 8]).buffer } });
  unsubscribe();
  port.onmessage({ data: { type: "mincode.host.binary", channel: "out", data: new Uint8Array([7]).buffer } });
  assert.deepEqual(received, [["out", [9, 8]]]);
  assert.deepEqual(errors, []);
});

test("host parses call and binary requests from plugin views", () => {
  assert.deepEqual(parsePluginUICallRequest({ type: "mincode.plugin.call", id: "request-1", method: "db.query" }),
    { type: "mincode.plugin.call", id: "request-1", method: "db.query", params: null });
  for (const method of ["plugin.call", "Host.info", "rpc.x", "a b", 1]) {
    assert.equal(parsePluginUICallRequest({ type: "mincode.plugin.call", id: "request-1", method }), undefined, String(method));
    assert.equal(isPluginUIMethod(method), false);
  }
  assert.equal(parsePluginUICallRequest({ type: "mincode.plugin.call", id: "", method: "ok" }), undefined);

  const data = new ArrayBuffer(4);
  assert.deepEqual(parsePluginUIBinaryRequest({ type: "mincode.plugin.binary", id: "request-2", channel: "pty:1", data }),
    { type: "mincode.plugin.binary", id: "request-2", channel: "pty:1", data });
  assert.equal(parsePluginUIBinaryRequest({ type: "mincode.plugin.binary", id: "request-2", channel: "pty:1", data: [1, 2] }), undefined);
  assert.equal(parsePluginUIBinaryRequest({ type: "mincode.plugin.binary", id: "request-2", channel: "../x", data }), undefined);
  assert.equal(parsePluginUIBinaryRequest({
    type: "mincode.plugin.binary", id: "request-2", channel: "big", data: new ArrayBuffer(PLUGIN_UI_MAX_BINARY_BYTES + 1),
  }), undefined);
});

test("the plugin frame routes calls and binary frames without action confirmation", async () => {
  const frame = await readFile(new URL("../src/components/plugins/PluginViewFrame.vue", import.meta.url), "utf8");
  const handler = frame.slice(frame.indexOf("async function handleProcessRequest"), frame.indexOf("function handleProcessBinary"));
  assert.match(handler, /callPluginUIMethod\(/);
  assert.match(handler, /sendPluginUIBinary\(/);
  assert.match(handler, /!session\.binary/);
  assert.match(handler, /PLUGIN_UI_MAX_IN_FLIGHT_REQUESTS/);
  assert.doesNotMatch(handler, /confirmAction/);
  assert.match(frame, /listenAppEvent<unknown>\("plugin:ui-binary", handleProcessBinary\)/);
  assert.match(frame, /frame\.generation !== session\.generation/);
});

// Pages keep action payloads in Vue reactive state; a Proxy cannot be structured-cloned by postMessage
// ("[object Array] could not be cloned"). Payloads are JSON by contract, so the transport sends a plain copy.
test("transport sends reactive (Proxy) payloads and params as plain JSON copies", async () => {
  const { api, port, sent } = await loadTransport({ tasks: 1, calls: 1 }, { clone: true });
  const reactive = (value) => new Proxy(value, {});
  const hosts = reactive(["a.example.com", "b.example.com"]);
  const pending = api.call("setDecrypt", reactive({ hosts, nested: reactive({ list: reactive([1, 2]) }) }));
  assert.deepEqual(sent[0].message.payload, { hosts: ["a.example.com", "b.example.com"], nested: { list: [1, 2] } });
  port.onmessage({ data: { type: "mincode.host.response", id: sent[0].message.id, ok: true, result: 1 } });
  assert.equal(await pending, 1);

  const method = api.request("files/list", reactive({ prefixes: reactive(["a"]) }));
  assert.deepEqual(sent[1].message.params, { prefixes: ["a"] });
  port.onmessage({ data: { type: "mincode.host.response", id: sent[1].message.id, ok: true, result: [] } });
  await method;

  const noPayload = api.call("status");
  assert.equal(sent[2].message.payload, undefined);
  port.onmessage({ data: { type: "mincode.host.response", id: sent[2].message.id, ok: true, result: 0 } });
  await noPayload;

  const cyclic = {};
  cyclic.self = cyclic;
  for (const bad of [cyclic, 1n, () => 1]) {
    await assert.rejects(api.call("run", bad), (error) => error.code === "invalid-request");
    await assert.rejects(api.request("files/list", bad), (error) => error.code === "invalid-request");
  }
  assert.equal(sent.length, 3, "unserializable arguments are rejected before posting");
});

test("transport retries busy calls quickly and close() rejects a waiting retry", async () => {
  const { api, port, sent } = await loadTransport({ tasks: 1 });
  const started = Date.now();
  const pending = api.call("run", {});
  port.onmessage({ data: { type: "mincode.host.response", id: sent[0].message.id, ok: false, error: { code: "busy", message: "busy" } } });
  while (sent.length < 2) await new Promise((resolve) => setTimeout(resolve, 5));
  assert.ok(Date.now() - started < 400, "first busy retry is sent within the short backoff");
  port.onmessage({ data: { type: "mincode.host.response", id: sent[1].message.id, ok: true, result: 7 } });
  assert.equal(await pending, 7);

  const waiting = api.call("run", {});
  port.onmessage({ data: { type: "mincode.host.response", id: sent[2].message.id, ok: false, error: { code: "busy", message: "busy" } } });
  api.close();
  await assert.rejects(waiting, (error) => error.code === "busy" && error.uncertain !== true);
  await new Promise((resolve) => setTimeout(resolve, 120));
  assert.equal(sent.length, 3, "no retry is sent after close()");
});

test("transport delivers pushed task snapshots only with features.taskEvents", async () => {
  const pushed = await loadTransport({ tasks: 1, taskEvents: 1 });
  assert.equal(pushed.api.tasks.pushSupported(), true);
  const seen = [];
  const stop = pushed.api.tasks.onChanged((snapshot) => seen.push(snapshot));
  const snapshot = { taskId: "t1", state: "succeeded", cancelRequested: false, result: 1 };
  pushed.port.onmessage({ data: { type: "mincode.host.task.changed", taskId: "t1", snapshot } });
  pushed.port.onmessage({ data: { type: "mincode.host.task.changed", taskId: "t2", snapshot } });
  pushed.port.onmessage({ data: { type: "mincode.host.task.changed", taskId: "t1", snapshot: null } });
  assert.deepEqual(JSON.parse(JSON.stringify(seen)), [snapshot]);
  stop();
  pushed.port.onmessage({ data: { type: "mincode.host.task.changed", taskId: "t1", snapshot } });
  assert.equal(seen.length, 1);

  const legacy = await loadTransport({ tasks: 1 });
  assert.equal(legacy.api.tasks.pushSupported(), false);
  const ignored = [];
  legacy.api.tasks.onChanged((value) => ignored.push(value));
  legacy.port.onmessage({ data: { type: "mincode.host.task.changed", taskId: "t1", snapshot } });
  assert.equal(ignored.length, 0);
});

test("direct channel tokens must point at the loopback asset server", async () => {
  const { parsePluginUIDirectToken } = await import("../src/domain/plugin-ui/direct-token.ts");
  const token = "ab".repeat(32);
  const url = `ws://127.0.0.1:4000/__mincode/direct?token=${token}`;
  assert.deepEqual(parsePluginUIDirectToken({ token, url }), { token, url });
  for (const bad of [
    { token, url: `ws://example.com/__mincode/direct?token=${token}` },
    { token, url: `wss://127.0.0.1:4000/__mincode/direct?token=${token}` },
    { token, url: `ws://127.0.0.1:4000/other?token=${token}` },
    { token, url: `ws://127.0.0.1:4000/__mincode/direct?token=${"cd".repeat(32)}` },
    { token: "short", url: "ws://127.0.0.1:4000/__mincode/direct?token=short" },
    null,
  ]) {
    assert.throws(() => parsePluginUIDirectToken(bad), /direct channel/i, JSON.stringify(bad));
  }
});

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
const directURL = `ws://127.0.0.1:4000/__mincode/direct?token=${"ab".repeat(32)}`;
const frame = (channel, bytes) => new Uint8Array([channel.length, ...new TextEncoder().encode(channel), ...bytes]);

test("transport prefers the direct socket and falls back to the bridge", async () => {
  FakeSocket.instances = [];
  const { api, port, sent } = await loadTransport({ binary: 1, binaryDirect: 1 }, { socket: true });
  const tokenRequest = sent.find((entry) => entry.message.type === "mincode.plugin.direct.token");
  assert.ok(tokenRequest, "the transport asks the host for a direct token");
  port.onmessage({ data: { type: "mincode.host.response", id: tokenRequest.message.id, ok: true, result: { url: directURL } } });
  await settle();
  const socket = FakeSocket.instances.at(-1);
  assert.equal(socket.url, directURL);
  assert.equal(socket.binaryType, "arraybuffer");
  assert.equal(api.binary.mode(), "bridge");
  socket.open();
  assert.equal(api.binary.mode(), "direct");

  const received = [];
  api.binary.onMessage((channel, data) => received.push([channel, [...data]]));
  socket.receive(frame("screen", [1, 2, 3]));
  assert.deepEqual(received, [["screen", [1, 2, 3]]]);

  const bridgeBefore = sent.filter((entry) => entry.message.type === "mincode.plugin.binary").length;
  await api.binary.send("input", new Uint8Array([9, 8]));
  assert.deepEqual([...socket.sent.at(-1)], [...frame("input", [9, 8])]);
  assert.equal(sent.filter((entry) => entry.message.type === "mincode.plugin.binary").length, bridgeBefore);

  socket.close();
  assert.equal(api.binary.mode(), "bridge");
  const pending = api.binary.send("input", new Uint8Array([7]));
  const bridged = sent.filter((entry) => entry.message.type === "mincode.plugin.binary").at(-1);
  assert.equal(bridged.message.channel, "input");
  port.onmessage({ data: { type: "mincode.host.response", id: bridged.message.id, ok: true, result: true } });
  await pending;
  api.close();
});

test("transport ignores the direct channel without the feature", async () => {
  FakeSocket.instances = [];
  const { api, sent } = await loadTransport({ binary: 1 }, { socket: true });
  await settle();
  assert.equal(sent.some((entry) => entry.message.type === "mincode.plugin.direct.token"), false);
  assert.equal(FakeSocket.instances.length, 0);
  assert.equal(api.binary.mode(), "bridge");
});

test("transport drops malformed direct frames and closes the socket on page close", async () => {
  FakeSocket.instances = [];
  const { api, port, sent } = await loadTransport({ binary: 1, binaryDirect: 1 }, { socket: true });
  const tokenRequest = sent.find((entry) => entry.message.type === "mincode.plugin.direct.token");
  port.onmessage({ data: { type: "mincode.host.response", id: tokenRequest.message.id, ok: true, result: { url: directURL } } });
  await settle();
  const socket = FakeSocket.instances.at(-1);
  socket.open();
  const received = [];
  api.binary.onMessage((channel) => received.push(channel));
  socket.receive(new Uint8Array([0]));
  socket.receive(new Uint8Array([9, 1, 2]));
  socket.receive(frame("../x", [1]));
  assert.deepEqual(received, []);
  api.close();
  assert.equal(socket.readyState, 3);
});

test("transport offers immersive mode and keyboard capture when the host does", async () => {
  const { api, port, sent } = await loadTransport({ immersive: 1, keyboardCapture: 1 });
  assert.equal(api.immersive.supported(), true);
  assert.equal(api.keyboard.supported(), true);
  const changes = [];
  api.immersive.onChanged((active) => changes.push(active));
  const released = [];
  api.keyboard.onReleased(() => released.push(true));

  const entering = api.immersive.enter();
  const enter = sent.at(-1).message;
  assert.equal(enter.type, "mincode.plugin.immersive.enter");
  port.onmessage({ data: { type: "mincode.host.response", id: enter.id, ok: true, result: true } });
  assert.equal(await entering, true);
  port.onmessage({ data: { type: "mincode.host.immersive", active: true } });
  port.onmessage({ data: { type: "mincode.host.immersive", active: "yes" } });
  assert.deepEqual(changes, [true]);
  assert.equal(api.immersive.active(), true);

  const capturing = api.keyboard.capture();
  const capture = sent.at(-1).message;
  assert.equal(capture.type, "mincode.plugin.keyboard.capture");
  port.onmessage({ data: { type: "mincode.host.response", id: capture.id, ok: true, result: true } });
  await capturing;
  assert.equal(api.keyboard.captured(), true);
  port.onmessage({ data: { type: "mincode.host.keyboard", captured: false } });
  assert.deepEqual(released, [true]);
  assert.equal(api.keyboard.captured(), false);

  for (const [call, type] of [[() => api.immersive.exit(), "mincode.plugin.immersive.exit"], [() => api.keyboard.release(), "mincode.plugin.keyboard.release"]]) {
    const pending = call();
    const message = sent.at(-1).message;
    assert.equal(message.type, type);
    port.onmessage({ data: { type: "mincode.host.response", id: message.id, ok: true, result: true } });
    await pending;
  }
});

test("transport refuses immersive mode and keyboard capture on older hosts", async () => {
  const { api, sent } = await loadTransport({ tasks: 1 });
  assert.equal(api.immersive.supported(), false);
  assert.equal(api.keyboard.supported(), false);
  await assert.rejects(api.immersive.enter(), (error) => error.code === "not-allowed");
  await assert.rejects(api.keyboard.capture(), (error) => error.code === "not-allowed");
  assert.equal(sent.length, 0);
});
