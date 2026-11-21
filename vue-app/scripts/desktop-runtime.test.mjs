// Desktop runtime glue (services/desktop-runtime.ts, desktop-events.ts, desktop-stream.ts) against a
// stub window.tango: event order across by-reference payloads, page-emitted events, cancelable
// invokes and the object-stream wrapper. The Go side is covered by internal/host tests.
import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import test from "node:test";

// desktop-bridge.ts (imported by desktop-stream.ts) uses the Vite-only dev-performance helper.
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/services/dev-performance") {
      const source = "export const measureDevAsync = (_name, run) => run();";
      return { url: `data:text/javascript,${encodeURIComponent(source)}`, shortCircuit: true };
    }
    return nextResolve(specifier, context);
  },
});

const runtime = {
  rawHandler: null,
  calls: [],
  emitted: [],
  streams: [],
  onRaw(fn) {
    this.rawHandler = fn;
    return () => {};
  },
  emit(name, data) {
    this.emitted.push({ name, data });
  },
  async call(method, request) {
    this.calls.push({ method, request });
    return { echoed: request.command };
  },
  stream(method, request) {
    const stream = new FakeStream(method, request);
    this.streams.push(stream);
    return stream;
  },
  socket(method) {
    return new FakeSocket(method);
  },
};

class FakeStream {
  constructor(method, request) {
    this.method = method;
    this.request = request;
    this.canceled = false;
    this.wake = null;
    this.result = undefined;
  }
  cancel() {
    this.canceled = true;
    this.wake?.();
  }
  resolve(value) {
    this.result = { value };
    this.wake?.();
  }
  async *[Symbol.asyncIterator]() {
    if (!this.result && !this.canceled) await new Promise((resolve) => (this.wake = resolve));
    if (this.canceled) throw Object.assign(new Error("stream canceled"), { code: "canceled" });
    yield this.result.value;
  }
}

class FakeSocket extends EventTarget {
  constructor(method) {
    super();
    this.url = `tango:${method}`;
    this.readyState = 0;
    this.bufferedAmount = 0;
    this.binaryType = "blob";
    this.sent = [];
  }
  send(data) {
    this.sent.push(data);
  }
  close() {
    this.readyState = 3;
    this.dispatchEvent(Object.assign(new Event("close"), { code: 1000, reason: "", wasClean: true }));
  }
}


globalThis.window = globalThis;
globalThis.tango = runtime;
const fetched = [];
globalThis.fetch = async (url, init) => {
  fetched.push({ url, init });
  const id = decodeURIComponent(String(url).split("id=")[1]);
  await new Promise((resolve) => setTimeout(resolve, 20));
  if (id === "missing") return { ok: false, status: 404 };
  return { ok: true, json: async () => ({ name: "seq", data: `ref:${id}` }) };
};

const { onEvent, emitEvent, handleRawMessage } = await import("../src/services/desktop-events.ts");
const { invokeCommand, isDesktopRuntime } = await import("../src/services/desktop-runtime.ts");
const { JSONStream, Stream } = await import("../src/services/desktop-stream.ts");

const settle = (ms = 120) => new Promise((resolve) => setTimeout(resolve, ms));

test("the event module subscribes to raw messages at load", () => {
  assert.equal(isDesktopRuntime(), true);
  assert.equal(runtime.rawHandler, handleRawMessage);
});

test("events keep Go's order across by-reference payloads; failures do not stall delivery", async (t) => {
  // Listener errors and failed fetches are reported (reportError), not swallowed.
  const uncaught = [];
  const previous = globalThis.reportError;
  globalThis.reportError = (error) => uncaught.push(String(error?.message ?? error));
  t.after(() => (globalThis.reportError = previous));
  const got = [];
  const offSeq = onEvent("seq", (data) => got.push(data));
  const offBoom = onEvent("boom", () => {
    got.push("boom");
    throw new Error("listener failed on purpose");
  });
  handleRawMessage('wev:{"name":"seq","data":"a"}');
  assert.deepEqual(got, ["a"], "inline events dispatch synchronously when nothing is pending");
  handleRawMessage("wref:r1");
  handleRawMessage('wev:{"name":"seq","data":"b"}');
  assert.deepEqual(got, ["a"], "later events wait for the outstanding reference");
  handleRawMessage("wref:missing");
  handleRawMessage('wev:{"name":"boom","data":null}');
  handleRawMessage('wev:{"name":"seq","data":"d"}');
  handleRawMessage("unrelated message");
  await settle();
  handleRawMessage('wev:{"name":"seq","data":"e"}');
  assert.deepEqual(got, ["a", "ref:r1", "b", "boom", "d", "e"]);
  assert.deepEqual(uncaught, ["event-ref 404", "listener failed on purpose"]);
  assert.equal(fetched[0].url, "/_mincode/event-ref?id=r1");
  assert.equal(fetched[0].init.cache, "no-store");
  offSeq();
  offBoom();
  handleRawMessage('wev:{"name":"seq","data":"after-off"}');
  assert.equal(got.at(-1), "e");
});

test("emitEvent broadcasts through Go", () => {
  emitEvent("x:changed", { v: 1 });
  emitEvent("x:none", undefined);
  assert.deepEqual(runtime.emitted.slice(-2), [
    { name: "mincode.emit", data: { name: "x:changed", data: { v: 1 } } },
    { name: "mincode.emit", data: { name: "x:none", data: null } },
  ]);
});

test("invokeCommand without a signal is a plain call", async () => {
  const value = await invokeCommand("git_status", { path: "/p" });
  assert.deepEqual(value, { echoed: "git_status" });
  assert.deepEqual(runtime.calls.at(-1), { method: "mincode.invoke", request: { command: "git_status", args: { path: "/p" } } });
});

test("invokeCommand with a signal runs as a cancelable stream", async () => {
  const done = new AbortController();
  const pending = invokeCommand("ai_list_models", {}, done.signal);
  const first = runtime.streams.at(-1);
  assert.equal(first.method, "mincode.invokeStream");
  assert.deepEqual(first.request, { command: "ai_list_models", args: {} });
  first.resolve(["m1"]);
  assert.deepEqual(await pending, ["m1"]);

  const abort = new AbortController();
  const canceled = invokeCommand("ai_chat", {}, abort.signal);
  const second = runtime.streams.at(-1);
  const reason = new Error("user stopped");
  abort.abort(reason);
  await assert.rejects(canceled, (error) => error === reason);
  assert.equal(second.canceled, true, "aborting cancels the stream (and the Go command's ctx)");

  await assert.rejects(invokeCommand("x", {}, abort.signal), (error) => error === reason);
});

test("Stream is a byte socket; JSONStream sends JSON text and parses every frame", async () => {
  const bytes = Stream("shell-wire");
  assert.equal(bytes.binaryType, "arraybuffer");

  const socket = JSONStream("ai-chat");
  const inner = socket["socket"];
  const seen = [];
  socket.addEventListener("message", (event) => seen.push(event.data));
  let closed = null;
  socket.addEventListener("close", (event) => (closed = event.code));
  socket.send({ prompt: "hi" });
  assert.deepEqual(inner.sent, ['{"prompt":"hi"}']);
  inner.dispatchEvent(new MessageEvent("message", { data: new TextEncoder().encode('{"a":1}').buffer }));
  inner.dispatchEvent(new MessageEvent("message", { data: '"plain"' }));
  assert.deepEqual(seen, [{ a: 1 }, "plain"]);
  socket.close();
  assert.equal(closed, 1000);
  assert.equal(socket.readyState, 3);
});
