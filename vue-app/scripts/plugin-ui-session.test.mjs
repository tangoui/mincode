import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import ts from "typescript";
import { computed, ref } from "vue";
import {
  classifyPluginUISessionFailure,
  findPluginUIViewFailure,
} from "../src/domain/plugin-ui/session-error.ts";
import { isPluginHostDownloadResult, parsePluginHostOpenURLResult, parsePluginHostSaveTextResult } from "../src/domain/plugin-ui/host-result.mjs";

test("host open-url results accept only bounded absolute HTTP(S) URLs", () => {
  assert.equal(parsePluginHostOpenURLResult({ type: "open-url", url: "https://example.com/docs?q=1" }), "https://example.com/docs?q=1");
  for (const url of [
    "//example.com/path",
    "javascript:alert(1)",
    "https://",
    "https://user:pass@example.com/",
    "https://user%40example.com@example.com/",
    "https://example.com/path with spaces",
    `https://example.com/${"x".repeat(2048)}`,
  ]) {
    assert.throws(() => parsePluginHostOpenURLResult({ type: "open-url", url }));
  }
});

test("open-url results may mark a download for the system browser", () => {
  assert.equal(isPluginHostDownloadResult({ type: "open-url", url: "https://example.com/a.zip", download: true }), true);
  for (const value of [{ type: "open-url", url: "https://example.com/" }, { download: "true" }, null, "download"]) {
    assert.equal(isPluginHostDownloadResult(value), false);
  }
});

test("host result handling keeps save-text-file and declares open-url in the frame", async () => {
  const [frame, pluginService] = await Promise.all([
    readFile(new URL("../src/components/plugins/PluginViewFrame.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/services/plugin-service.ts", import.meta.url), "utf8"),
  ]);
  assert.deepEqual(parsePluginHostSaveTextResult({
    type: "save-text-file",
    file: { filename: "result.txt", content: "safe" },
    savedResult: { saved: true },
    cancelledResult: { saved: false },
  }), {
    filename: "result.txt",
    content: "safe",
    savedResult: { saved: true },
    cancelledResult: { saved: false },
  });
  assert.match(frame, /action\.resultHandling === "open-url"/);
  // The host picks the browser: built-in where the platform has one, system for downloads or elsewhere.
  assert.match(frame, /openHostLink\(url, \{ download: isPluginHostDownloadResult\(result\) \}\)/);
  assert.doesNotMatch(frame, /openSystemBrowser/);
  assert.doesNotMatch(frame, /window\.open/);
  assert.match(pluginService, /resultHandling\?: "save-text-file" \| "open-url"/);
  assert.match(pluginService, /action\?\.resultHandling === "open-url"/);
  assert.match(pluginService, /resultHandling === "save-text-file" && actionRisk === "read"/);
});

test("old frame requests cannot release a new session's slots or duplicate IDs", async () => {
  const source = await readFile(new URL("../src/components/plugins/PluginViewFrame.vue", import.meta.url), "utf8");
  const state = source.match(/const activeRequestIds =[^]*?const inFlight =[^\n]+/)[0];
  const close = source.match(/function closeTransport\(\) \{([^]*?)\n\}/)[1];
  const invoke = source.match(/async function handleInvokeRequest\(data: unknown\) \{([^]*?)\n\}/)[1];
  const reset = close.match(/activeRequestIds\.value =[^\n]+/)[0];
  const capture = invoke.match(/const requestIds =[^\n]+/)[0];
  const reserve = invoke.match(/requestIds\.add\(request.id\);/)[0];
  const release = invoke.match(/requestIds\.delete\(request.id\);/)[0];
  const code = ts.transpileModule(`${state}
    return {
      begin(id: string) { const request = { id }; ${capture} ${reserve}
        return () => { ${release} };
      },
      reset() { ${reset} },
      count() { return inFlight.value; },
      has(id: string) { return activeRequestIds.value.has(id); }
    };`, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;
  const tracker = new Function("ref", "computed", code)(ref, computed);
  const finishOld = tracker.begin("request-1");
  assert.equal(tracker.count(), 1);
  tracker.reset();
  assert.equal(tracker.count(), 0);
  const finishNew = tracker.begin("request-1");
  finishOld();
  assert.equal(tracker.count(), 1);
  assert.equal(tracker.has("request-1"), true);
  finishNew();
  assert.equal(tracker.count(), 0);
});

test("finds stale plugin view tabs without conflating missing plugins and views", () => {
  const plugins = [{ id: "draw" }, { id: "installed-without-views" }];
  const views = [{ pluginId: "draw", id: "canvas" }];
  assert.equal(findPluginUIViewFailure(plugins, views, "draw", "canvas"), undefined);
  assert.equal(findPluginUIViewFailure(plugins, views, "draw", "old-view"), "missing-view");
  assert.equal(findPluginUIViewFailure(plugins, views, "excalidraw", "canvas"), "missing-plugin");
  assert.equal(findPluginUIViewFailure(plugins, views, "installed-without-views", "canvas"), "missing-view");
});

test("classifies actionable plugin UI availability failures", () => {
  const cases = [
    [new Error('plugin "example" is not available (state: disabled)'), "disabled"],
    [new Error('plugin "example" is not available (state: stopped)'), "not-running"],
    [new Error('plugin "example" is not available (state: error)'), "not-running"],
    [new Error('plugin "example" not found'), "missing-plugin"],
    [new Error('plugin view "dashboard" not found'), "missing-view"],
    [new Error("plugin UI host is unavailable"), "host-unavailable"],
    [new Error("plugin UI parent origin must be the local tango origin"), "origin-rejected"],
    [new Error("MinCode application origin is unavailable"), "origin-rejected"],
    [new Error("Invalid plugin UI session response"), "invalid-response"],
    [new Error("Invalid plugin UI asset URL"), "invalid-response"],
  ];
  for (const [error, expected] of cases) {
    assert.equal(classifyPluginUISessionFailure(error), expected, error.message);
  }
});

test("reads nested IPC errors without exposing arbitrary details", () => {
  const nested = {
    message: "native invocation failed",
    cause: { error: 'plugin "example" is not available (state: starting)' },
  };
  assert.equal(classifyPluginUISessionFailure(nested), "not-running");
  assert.equal(classifyPluginUISessionFailure({ token: "secret", path: "/private/path" }), "unavailable");
  assert.equal(classifyPluginUISessionFailure(null), "unavailable");
});


test("shared plugin transport answers host liveness probes without invoking actions", async () => {
  const { readFile } = await import("node:fs/promises");
  const vm = await import("node:vm");
  // transport.js imports the self-contained theme-tokens.js helper; inline it
  // so the transport runs as a classic script inside the vm sandbox.
  const themeImport = /^import \{ applyThemeTokens \} from '\.\/theme-tokens\.js';$/m;
  const themeTokens = (await readFile(new URL("../src/plugin-ui/theme-tokens.js", import.meta.url), "utf8"))
    .replaceAll("export function", "function");
  const transport = await readFile(new URL("../src/plugin-ui/transport.js", import.meta.url), "utf8");
  assert.match(transport, themeImport);
  const source = themeTokens + transport.replace(themeImport, "").replace("export function", "function");
  const listeners = new Map();
  const parent = { postMessage() {} };
  const context = vm.createContext({
    URLSearchParams,
    location: { hash: "#mincodeSession=s&nonce=n&generation=1" },
    parent,
    setTimeout,
    clearTimeout,
    addEventListener: (type, listener) => listeners.set(type, listener),
    removeEventListener: (type) => listeners.delete(type),
  });
  vm.runInContext(source, context);
  context.createTransport({ onReady() {}, onError() {}, applyContext() {} });
  const sent = [];
  const port = { start() {}, close() {}, postMessage(message) { sent.push(message); } };
  listeners.get("message")({
    source: parent,
    data: { type: "mincode.host.connect", protocolVersion: 1, sessionId: "s", nonce: "n", generation: 1, requestTimeoutMs: 30000 },
    ports: [port],
  });

  port.onmessage({ data: { type: "mincode.host.probe", id: "frame-1" } });
  assert.equal(sent.length, 1);
  assert.equal(sent[0].type, "mincode.plugin.probe");
  assert.equal(sent[0].id, "frame-1");
});

test("an invoke message is serialized once and measured byte for byte", async () => {
  const {
    PLUGIN_UI_MAX_MESSAGE_BYTES,
    measurePluginUIMessage,
    parsePluginUIHostRequest,
    parsePluginUITaskRequest,
    pluginUIMessageByteLength,
    serializePluginUIPayload,
    utf8ByteLength,
    utf8ByteLengthUpTo,
  } = await import("../src/domain/plugin-ui/protocol.ts");
  const rows = Array.from({ length: 50 }, (_, index) => ({ id: index, name: `行-${index} "q" \u{1F600}`, at: new Date(0) }));
  const messages = [
    { type: "mincode.plugin.invoke", id: "r1", action: "echo", payload: { rows } },
    { type: "mincode.plugin.invoke", id: "r2", action: "echo", payload: "text" },
    { type: "mincode.plugin.invoke", id: "r3", action: "echo", payload: null },
    { type: "mincode.plugin.invoke", id: "r4", action: "echo", payload: 0 },
    { payload: [1, 2, 3] },
    { type: "mincode.plugin.task.start", id: "t1", action: "job", payload: { a: 1 }, timeoutMs: 1000, skipped: undefined },
    { type: "mincode.plugin.invoke", id: "r5", action: "echo", payload: () => 1 },
  ];
  for (const message of messages) {
    const measured = measurePluginUIMessage(message);
    assert.equal(measured.bytes, utf8ByteLength(JSON.stringify(message)), JSON.stringify(message).slice(0, 60));
    assert.equal(measured.payload, serializePluginUIPayload(typeof message.payload === "function" ? undefined : message.payload));
    assert.equal(measured.payloadBytes, utf8ByteLength(measured.payload));
  }
  // Without a payload member (or for non-plain values) the whole message is measured instead.
  for (const message of [{ type: "mincode.plugin.invoke", id: "r6", action: "echo" }, { type: "x", payload: undefined }, [1, { payload: 2 }], "text"]) {
    const measured = measurePluginUIMessage(message);
    assert.equal(measured.bytes, utf8ByteLength(JSON.stringify(message)));
    assert.equal(measured.payload, undefined);
  }
  const cyclic = { type: "mincode.plugin.invoke", id: "r7", action: "echo", payload: {} };
  cyclic.payload.self = cyclic.payload;
  assert.equal(measurePluginUIMessage(cyclic).bytes, Number.POSITIVE_INFINITY);
  assert.equal(measurePluginUIMessage({ type: "mincode.plugin.invoke", id: "r8", action: "echo", payload: 1n }).bytes, Number.POSITIVE_INFINITY);

  // The 256 KiB gate stays exact at the boundary, multi-byte characters included.
  const overhead = utf8ByteLength(JSON.stringify({ type: "mincode.plugin.invoke", id: "r9", action: "echo", payload: "" }));
  const fits = "é".repeat(Math.floor((PLUGIN_UI_MAX_MESSAGE_BYTES - overhead) / 2));
  const atLimit = { type: "mincode.plugin.invoke", id: "r9", action: "echo", payload: fits };
  const atLimitBytes = utf8ByteLength(JSON.stringify(atLimit));
  assert.ok(atLimitBytes <= PLUGIN_UI_MAX_MESSAGE_BYTES && atLimitBytes > PLUGIN_UI_MAX_MESSAGE_BYTES - 2);
  assert.equal(measurePluginUIMessage(atLimit).bytes, atLimitBytes);
  assert.ok(measurePluginUIMessage({ ...atLimit, payload: `${fits}éé` }).bytes > PLUGIN_UI_MAX_MESSAGE_BYTES);
  assert.ok(measurePluginUIMessage({ ...atLimit, payload: { text: "x".repeat(PLUGIN_UI_MAX_MESSAGE_BYTES * 2) } }).bytes > PLUGIN_UI_MAX_MESSAGE_BYTES);

  // Bounded counting: exact up to the limit, otherwise above it.
  for (const value of ["", "abc", "é".repeat(10), "\u{1F600}".repeat(10), "x".repeat(100)]) {
    for (const limit of [0, 3, 20, 40, 41, 99, 100, 1000]) {
      const exact = utf8ByteLength(value);
      const bounded = utf8ByteLengthUpTo(value, limit);
      if (exact <= limit) assert.equal(bounded, exact, `${value.length}/${limit}`);
      else assert.ok(bounded > limit, `${value.length}/${limit}`);
    }
  }
  assert.equal(pluginUIMessageByteLength({ a: "é" }, 10), utf8ByteLength(JSON.stringify({ a: "é" })));
  assert.equal(pluginUIMessageByteLength(undefined), 0);

  // Host-request parsing no longer re-measures invoke messages and accepts the caller's size.
  const confirm = { type: "mincode.plugin.confirm", id: "c1", intent: "write", subject: "s" };
  assert.equal(parsePluginUIHostRequest(messages[0]), undefined);
  assert.equal(parsePluginUIHostRequest(confirm, PLUGIN_UI_MAX_MESSAGE_BYTES + 1), undefined);
  assert.deepEqual(parsePluginUIHostRequest(confirm, 10), confirm);
  const login = { type: "mincode.plugin.ai-config.login", id: "l1", kind: "mcp", sourceId: "github" };
  assert.deepEqual(parsePluginUIHostRequest(login, 10), login);
  assert.equal(parsePluginUIHostRequest(login, PLUGIN_UI_MAX_MESSAGE_BYTES + 1), undefined);
  assert.equal(parsePluginUIHostRequest({ ...login, id: "x".repeat(129) }), undefined);
  const authStatus = { type: "mincode.plugin.ai-config.auth-status", id: "a1", sourceIds: ["github"] };
  assert.deepEqual(parsePluginUIHostRequest(authStatus, 10), authStatus);
  assert.equal(parsePluginUIHostRequest(authStatus, PLUGIN_UI_MAX_MESSAGE_BYTES + 1), undefined);
  assert.equal(parsePluginUIHostRequest({ ...authStatus, sourceIds: [] }), undefined);
  assert.equal(parsePluginUITaskRequest(messages[5], PLUGIN_UI_MAX_MESSAGE_BYTES + 1), undefined);
  assert.equal(parsePluginUITaskRequest(messages[5], measurePluginUIMessage(messages[5]).bytes)?.action, "job");
});

test("the frame measures each invoke message once and reuses its payload", async () => {
  const source = await readFile(new URL("../src/components/plugins/PluginViewFrame.vue", import.meta.url), "utf8");
  const invoke = source.match(/async function handleInvokeRequest\(data: unknown\) \{([^]*?)\n\}/)[1];
  assert.equal(invoke.match(/measurePluginUIMessage\(/g)?.length, 1);
  assert.doesNotMatch(invoke, /pluginUIMessageByteLength\(/);
  assert.match(invoke, /parsePluginUIHostRequest\(data, measured\.bytes\)/);
  assert.match(invoke, /parsePluginUITaskRequest\(data, measured\.bytes\)/);
  assert.match(invoke, /payload = measured\.payload;/);
  assert.match(invoke, /payloadBytes > action\.maxPayloadBytes/);
});

test("settings variant reports surface settings and is never pooled", async () => {
  const { parsePluginUISurface } = await import("../src/domain/plugin-ui/protocol.ts");
  assert.equal(parsePluginUISurface("settings"), "settings");
  assert.equal(parsePluginUISurface("sidebar"), undefined);
  const [frame, section] = await Promise.all([
    readFile(new URL("../src/components/plugins/PluginViewFrame.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/components/settings/PluginSettingsPageSection.vue", import.meta.url), "utf8"),
  ]);
  assert.match(frame, /variant\?: "page" \| "widget" \| "floating" \| "settings";/);
  assert.match(frame, /const isSettings = computed\(\(\) => props\.variant === "settings"\);/);
  assert.match(frame, /if \(isSettings\.value\) return \{ \.\.\.context, surface: "settings" \};/);
  // No file drop, no open requests (they belong to the tab), no AI settings writes, no window resize.
  assert.match(frame, /const dropElementId = computed\(\(\) => fileDrop\.value && !props\.document && !isWidget\.value && !isFloating\.value && !isSettings\.value/);
  assert.match(frame, /const acceptsOpenRequests = computed\(\(\) => !isWidget\.value && !isFloating\.value && !isSettings\.value && !props\.document\);/);
  assert.match(frame, /function aiConfigAllowed\([^)]*\) \{\s*return [^\n]*!isSettings\.value/);
  assert.match(frame, /if \(request\.type === "mincode\.plugin\.floating\.resize"\) \{\s*if \(!isFloating\.value\)/);
  // Never parked: the section never passes `warm` and never keeps a frame alive.
  assert.match(section, /variant="settings"/);
  assert.match(section, /drop-target-id=""/);
  const template = section.slice(section.indexOf("<template>"));
  assert.doesNotMatch(template, /:warm=|\swarm[\s=>]|KeepAlive|keep-alive/);
  assert.doesNotMatch(section, /v-html|innerHTML/);
  // "Open in tab" closes the Settings dialog first (the tab would open behind it), like the keybinding panel.
  const settings = await readFile(new URL("../src/views/SettingsWorkspaceView.vue", import.meta.url), "utf8");
  assert.match(settings, /<PluginSettingsPanel\s[^>]*@navigate="close\(\)"/);
  assert.match(settings, /function close\(\) \{\s*emit\("update:modelValue", false\);/);
  const panel = await readFile(new URL("../src/components/settings/PluginSettingsPanel.vue", import.meta.url), "utf8");
  assert.match(panel, /<PluginSettingsPageSection[^>]*@navigate="emit\('navigate'\)"/);
});
