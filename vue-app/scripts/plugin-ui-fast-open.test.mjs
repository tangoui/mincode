// Fast plugin view opening: warm pool visibility, host skeleton and prefetch
// on the page side (transport.js) plus the host frame wiring.
import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import vm from "node:vm";

async function loadTransport(connect = {}) {
  const themeImport = /^import \{ applyThemeTokens \} from '\.\/theme-tokens\.js';$/m;
  const themeTokens = (await readFile(new URL("../src/plugin-ui/theme-tokens.js", import.meta.url), "utf8"))
    .replaceAll("export function", "function");
  const transport = await readFile(new URL("../src/plugin-ui/transport.js", import.meta.url), "utf8");
  const source = themeTokens + transport.replace(themeImport, "").replace("export function", "function");
  const listeners = new Map();
  const parent = { postMessage() {} };
  const context = vm.createContext({
    URLSearchParams, ArrayBuffer, Uint8Array, Promise,
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
  const doConnect = (extra = connect) => listeners.get("message")({
    source: parent,
    data: { type: "mincode.host.connect", protocolVersion: 1, sessionId: "s", nonce: "n", generation: 1, requestTimeoutMs: 30000, features: { tasks: 1 }, ...extra },
    ports: [port],
  });
  return { api, port, sent, errors, connect: doConnect };
}

test("transport.visible follows host visibility messages only when the host supports them", async () => {
  const legacy = await loadTransport({ features: { tasks: 1 } });
  legacy.connect();
  assert.equal(legacy.api.visible(), true);
  legacy.port.onmessage({ data: { type: "mincode.host.visibility", visible: false } });
  assert.equal(legacy.api.visible(), true, "hosts without features.visibility cannot hide the page");

  const { api, port, connect, errors } = await loadTransport();
  const seen = [];
  const stop = api.onVisibilityChange((visible) => seen.push(visible));
  connect({ features: { tasks: 1, visibility: 1 }, visible: false });
  assert.equal(api.visible(), false);
  port.onmessage({ data: { type: "mincode.host.visibility", visible: true } });
  port.onmessage({ data: { type: "mincode.host.visibility", visible: true } });
  port.onmessage({ data: { type: "mincode.host.visibility", visible: "no" } });
  port.onmessage({ data: { type: "mincode.host.visibility", visible: false } });
  assert.deepEqual(seen, [true, false]);
  stop();
  port.onmessage({ data: { type: "mincode.host.visibility", visible: true } });
  assert.deepEqual(seen, [true, false]);
  assert.equal(api.visible(), true);
  assert.deepEqual(errors, []);
});

test("transport.prefetched resolves undefined without host support or a matching declared action", async () => {
  for (const extra of [
    { features: { tasks: 1 } },
    { features: { tasks: 1 }, prefetch: { action: "state" } },
    { features: { tasks: 1, prefetch: 1 } },
    { features: { tasks: 1, prefetch: 1 }, prefetch: { action: "other" } },
  ]) {
    const { api, port, connect } = await loadTransport(extra);
    const early = api.prefetched("state");
    connect();
    port.onmessage({ data: { type: "mincode.host.prefetch", action: "state", ok: true, result: { tasks: [1] } } });
    assert.equal(await early, undefined, JSON.stringify(extra));
    assert.equal(await api.prefetched("state"), undefined);
  }
  const { api } = await loadTransport();
  assert.equal(await api.prefetched(""), undefined);
  assert.equal(await api.prefetched(42), undefined);
});

test("transport.prefetched waits for the push, delivers the result once, and fails soft", async () => {
  const declared = { features: { tasks: 1, prefetch: 1 }, prefetch: { action: "state" } };
  // Called before the handshake and before the push.
  let loaded = await loadTransport(declared);
  const early = loaded.api.prefetched("state");
  loaded.connect();
  loaded.port.onmessage({ data: { type: "mincode.host.prefetch", action: "state", ok: true, result: { tasks: ["a"] } } });
  assert.deepEqual(await early, { tasks: ["a"] });
  assert.equal(await loaded.api.prefetched("state"), undefined, "one-shot: later callers fetch fresh data");

  // Push before the call; another action never steals it; a second push is ignored.
  loaded = await loadTransport(declared);
  loaded.connect();
  loaded.port.onmessage({ data: { type: "mincode.host.prefetch", action: "other", ok: true, result: "x" } });
  loaded.port.onmessage({ data: { type: "mincode.host.prefetch", action: "state", ok: true, result: 1 } });
  loaded.port.onmessage({ data: { type: "mincode.host.prefetch", action: "state", ok: true, result: 2 } });
  assert.equal(await loaded.api.prefetched("other"), undefined);
  assert.equal(await loaded.api.prefetched("state"), 1);

  // A failed prefetch resolves undefined so the page calls the action itself.
  loaded = await loadTransport(declared);
  loaded.connect();
  const failed = loaded.api.prefetched("state");
  loaded.port.onmessage({ data: { type: "mincode.host.prefetch", action: "state", ok: false, error: { code: "invoke-failed", message: "boom" } } });
  assert.equal(await failed, undefined);

  // Closing the page releases waiters.
  loaded = await loadTransport(declared);
  loaded.connect();
  const pending = loaded.api.prefetched("state");
  loaded.api.close();
  assert.equal(await pending, undefined);
  assert.deepEqual(loaded.sent, []);
});

test("prefetch pushes are bounded like ordinary responses", async () => {
  const { pluginUIPrefetchMessage } = await import("../src/domain/plugin-ui/prefetch.ts");
  assert.deepEqual(pluginUIPrefetchMessage("state", { ok: true, result: { a: 1 } }, "too large"), { type: "mincode.host.prefetch", action: "state", ok: true, result: { a: 1 } });
  assert.deepEqual(
    pluginUIPrefetchMessage("state", { ok: false, error: { code: "invoke-failed", message: "x" } }, "too large"),
    { type: "mincode.host.prefetch", action: "state", ok: false, error: { code: "invoke-failed", message: "x" } },
  );
  assert.deepEqual(
    pluginUIPrefetchMessage("state", { ok: true, result: "x".repeat(300 * 1024) }, "too large"),
    { type: "mincode.host.prefetch", action: "state", ok: false, error: { code: "result-too-large", message: "too large" } },
  );
});

test("the host frame prefetches through the session's own invoke path", async () => {
  const [frame, service] = await Promise.all([
    readFile(new URL("../src/components/plugins/PluginViewFrame.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/services/plugin-service.ts", import.meta.url), "utf8"),
  ]);
  // Started right after the session exists, before the advisory lookup and the frame load finish.
  assert.match(frame, /session = await beginPluginUISession\(pluginId, viewId\);\s*\}\s*(?:\/\/[^\n]*\n\s*)+if \(mounted && currentAttempt === attempt\) beginPrefetch\(session\);/);
  assert.match(frame, /invokePluginUI\(session\.sessionId, session\.nonce, action\.id, spec\.payload\)/);
  assert.match(frame, /action\.risk !== "read" \|\| requiresPluginApproval\(action\.risk\) \|\| action\.resultHandling/);
  // Delivered only to the same, connected session; dropped when the transport closes.
  assert.match(frame, /currentSession !== state\.session\) return;/);
  assert.match(frame, /prefetchState = undefined;/);
  assert.match(frame, /prefetch: 1,/);
  // The service revalidates the declared prefetch against the session's actions.
  assert.match(service, /action\.risk !== "read" \|\| action\.resultHandling/);
});

test("the host frame keeps parked frames hidden, inert and revalidated before reuse", async () => {
  const [frame, workspace, css, dts] = await Promise.all([
    readFile(new URL("../src/components/plugins/PluginViewFrame.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/views/PluginWorkspaceView.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/styles/workspaces/plugins/workspace.css", import.meta.url), "utf8"),
    readFile(new URL("../src/plugin-ui/transport.d.ts", import.meta.url), "utf8"),
  ]);
  // Visibility is advertised and pushed; parked or inactive pages are hidden.
  assert.match(frame, /visibility: 1,/);
  assert.match(frame, /visible: pageVisible\.value,/);
  assert.match(frame, /const pageVisible = computed\(\(\) => props\.active && !props\.warm && !reusePending\.value\)/);
  assert.match(frame, /type: "mincode\.host\.visibility", visible/);
  // A parked frame receives no open requests; drops already require an active frame.
  assert.match(frame, /acceptsOpenRequests\.value && !props\.warm/);
  assert.match(frame, /props\.active && status\.value === "ready"/);
  // Leaving the pool revalidates the plugin and view and fails closed otherwise.
  assert.match(frame, /async function revalidateReusedSession\(\) \{[\s\S]*?listPluginViews\(/);
  assert.match(frame, /!view\.activeInWorkspace \|\| view\.state !== "running"[\s\S]*?failClosed/);
  // Revalidation keeps the page shown but inert: no skeleton flash.
  assert.ok(frame.includes(':inert="reusePending || undefined"'));
  assert.ok(frame.includes(`v-if="status === 'loading' && !isFloating"`));
  // Parked pages cannot raise host UI or cause side effects.
  assert.ok(frame.includes('props.warm && (isTask || action.risk !== "read" || action.resultHandling)'));
  assert.ok(frame.includes("props.warm && WARM_BLOCKED_HOST_REQUESTS.has(request.type)"));
  // Parked panes are hidden and inert like inactive tabs.
  assert.match(workspace, /:inert="slot\.id !== activePluginTabId"/);
  assert.match(workspace, /:aria-hidden="slot\.id !== activePluginTabId"/);
  assert.match(workspace, /:warm="slot\.warm"/);
  assert.match(css, /\.plugin-view-workspace__pane \{[^}]*visibility: hidden;[^}]*pointer-events: none;/);
  assert.match(dts, /visible\(\): boolean;/);
  assert.match(dts, /onVisibilityChange\(listener: \(visible: boolean\) => void\): \(\) => void;/);
});

test("transport accepts the settings surface and an unknown surface falls back to workspace", async () => {
  const settings = await loadTransport();
  settings.connect({ context: { locale: "en", surface: "settings" } });
  assert.equal(settings.api.surface(), "settings");
  settings.port.onmessage({ data: { type: "mincode.host.context", context: { locale: "en", surface: "settings" } } });
  assert.equal(settings.api.surface(), "settings");

  const unknown = await loadTransport();
  unknown.connect({ context: { locale: "en", surface: "sidebar" } });
  assert.equal(unknown.api.surface(), "workspace");
  const dts = await readFile(new URL("../src/plugin-ui/transport.d.ts", import.meta.url), "utf8");
  assert.match(dts, /surface\?: 'workspace' \| 'document' \| 'widget' \| 'floating' \| 'settings';/);
  assert.match(dts, /surface\(\): 'workspace' \| 'document' \| 'widget' \| 'floating' \| 'settings';/);
});
