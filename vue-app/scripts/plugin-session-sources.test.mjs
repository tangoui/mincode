import assert from "node:assert/strict";
import test from "node:test";
import {
  collectSessionSources,
  parsePluginSessionSourceKey,
  parseSessionListResponse,
  parseSessionMessagesResponse,
  pluginSessionSourceKey,
  SESSION_SOURCE_LIMITS,
} from "../src/domain/plugins/session-sources.ts";
import { readFile } from "node:fs/promises";
import { registerHooks } from "node:module";

// --- Stubs: the desktop bridge records the host arguments; the locale is fixed.
const desktopStub = `
  export const isDesktopRuntime = () => true;
  export const nativeErrorMessage = (error) => String(error);
  export async function invokeBackend(command, args) {
    const state = globalThis.__pluginSessionSourcesTest;
    state.calls.push([command, JSON.parse(JSON.stringify(args))]);
    return state.response;
  }
`;
const i18nStub = `export const useVueI18n = () => ({ locale: { value: "zh" } });`;
const dataModule = (source) => `data:text/javascript,${encodeURIComponent(source)}`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/i18n/messages") return { url: dataModule(i18nStub), shortCircuit: true };
    const resolved = nextResolve(specifier, context);
    if (resolved.url.endsWith("/src/services/desktop-bridge.ts")) return { url: dataModule(desktopStub), shortCircuit: true };
    return resolved;
  },
});
const bridge = { calls: [], response: undefined };
globalThis.__pluginSessionSourcesTest = bridge;
const service = await import("../src/services/plugin-session-sources.ts");

const contract = JSON.parse(await readFile(new URL("../../plugins/api/contribution-points.json", import.meta.url), "utf8"));

function contribution(pluginId, entries, overrides = {}) {
  return {
    pluginId,
    pluginName: `${pluginId} name`,
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: "f", entries, issues: [] },
    ...overrides,
  };
}

test("session sources: limits mirror the contract", () => {
  const point = contract.points.sessionSources;
  for (const [name, value] of Object.entries(SESSION_SOURCE_LIMITS)) {
    if (name === "maxEntries") assert.equal(value, point.maxEntries);
    else assert.equal(value, point.limits[name], name);
  }
});

test("session sources: collect snapshot entries", () => {
  const sources = collectSessionSources([
    contribution("p.x", [
      { id: "team", title: "Team", titleLocalized: { zh: "团队" }, icon: "rocket", mark: "TE", action: "sessions.read" },
      { id: "no-action", title: "Broken" },
      { id: "third", title: "Third", action: "sessions.read" },
    ]),
    contribution("a.y", [{ id: "chat", title: "会话", icon: "cpu", mark: "abcd", action: "read" }], { activeInWorkspace: false }),
    contribution("c.z", [{ id: "decl", title: "Decl", action: "read" }], { runtime: "declarative" }),
    contribution("b.dup", [{ id: "x", title: "X", action: "r" }, { id: "x", title: "X2", action: "r" }]),
  ]);
  assert.deepEqual(sources.map((source) => source.key), ["plugin:a.y/chat", "plugin:b.dup/x", "plugin:p.x/team"]);
  const team = sources.find((source) => source.key === "plugin:p.x/team");
  assert.deepEqual(team, {
    key: "plugin:p.x/team",
    pluginId: "p.x",
    pluginName: "p.x name",
    id: "team",
    title: "Team",
    titleLocalized: { zh: "团队" },
    icon: "chat",
    mark: "TE",
    activeInWorkspace: true,
  });
  const chat = sources.find((source) => source.key === "plugin:a.y/chat");
  assert.equal(chat.icon, "cpu");
  assert.equal(chat.mark, "PL", "an invalid mark falls back to the default");
  assert.equal(chat.activeInWorkspace, false);
  assert.equal(sources.find((source) => source.pluginId === "b.dup").title, "X");
});

test("session sources: key round trip", () => {
  const key = pluginSessionSourceKey("p.x", "team");
  assert.equal(key, "plugin:p.x/team");
  assert.deepEqual(parsePluginSessionSourceKey(key), { pluginId: "p.x", id: "team" });
  for (const bad of ["custom:a", "claude", "plugin:", "plugin:p.x", "plugin:p.x/", "plugin:/team", "plugin:P.X/team", "plugin:p.x/Team", "plugin:p.x/team/more"]) {
    assert.equal(parsePluginSessionSourceKey(bad), undefined, bad);
  }
});

test("session sources: list response", () => {
  const response = parseSessionListResponse({
    sessions: [
      { id: "s1", title: "First", detail: "gpt", model: "gpt-4o", lastAt: 1_700_000_000_000, messageCount: 3 },
      { id: "s1", title: "Duplicate" },
      { id: "s2", lastAt: -1 },
      { id: "s3", title: "a\nb" },
      { id: "s4" },
      { id: " s5", title: "Padded id" },
      { id: "s6", messageCount: 1.5 },
      "nope",
      // Go structs without omitempty send null for absent optional fields.
      { id: "s7", title: null, detail: null, model: null, lastAt: null, messageCount: null },
    ],
    dropped: 2,
    truncated: false,
  });
  assert.deepEqual(response.sessions, [
    { id: "s1", title: "First", detail: "gpt", model: "gpt-4o", lastAt: 1_700_000_000_000, messageCount: 3 },
    { id: "s4", title: "" },
    { id: "s7", title: "" },
  ]);
  assert.equal(response.dropped, 8);
  assert.equal(response.truncated, false);
  const many = parseSessionListResponse({ sessions: Array.from({ length: 205 }, (_, index) => ({ id: `s${index}` })) });
  assert.equal(many.sessions.length, 200);
  assert.equal(many.truncated, true);
  assert.deepEqual(parseSessionListResponse(null), { sessions: [], dropped: 0, truncated: false });
});

test("session sources: messages response", () => {
  const response = parseSessionMessagesResponse({
    messages: [
      { role: "user", text: "<b>hi</b> [x](file:///etc/passwd)", timestamp: 5 },
      { role: "admin", text: "nope" },
      { role: "assistant", text: "line1\nline2\tTab", model: "m", truncated: true },
      { role: "tool", text: 7 },
      { role: "system", text: "ok", timestamp: 253402300800000 },
      { role: "tool", text: "null fields", timestamp: null, model: null, truncated: null },
    ],
    dropped: 1,
    truncated: true,
  });
  assert.deepEqual(response.messages, [
    { role: "user", text: "<b>hi</b> [x](file:///etc/passwd)", timestamp: 5 },
    { role: "assistant", text: "line1\nline2\tTab", model: "m", truncated: true },
    { role: "tool", text: "null fields" },
  ]);
  assert.equal(response.dropped, 4);
  assert.equal(response.truncated, true);
  const many = parseSessionMessagesResponse({ messages: Array.from({ length: 205 }, (_, index) => ({ role: "user", text: `m${index}` })) });
  assert.equal(many.messages.length, 200);
  assert.equal(many.messages[0].text, "m5", "the last 200 messages are kept");
  assert.equal(many.truncated, true);
});

function pluginSource(pluginId, id) {
  return {
    key: pluginSessionSourceKey(pluginId, id),
    pluginId,
    pluginName: `${pluginId} name`,
    id,
    title: id,
    icon: "chat",
    mark: "PL",
    activeInWorkspace: true,
  };
}

test("refreshAll skips collapsed plugin sources", () => {
  const a = pluginSource("p.a", "one");
  const b = pluginSource("p.b", "two");
  assert.deepEqual(service.pluginSourcesToRefresh([a, b], { [a.key]: true }), [a]);
  assert.deepEqual(service.pluginSourcesToRefresh([a, b], { [a.key]: false, [b.key]: true }), [b]);
  assert.deepEqual(service.pluginSourcesToRefresh([a, b], {}), []);
});

test("session sources: messages request carries only the session id", async () => {
  bridge.calls = [];
  bridge.response = { messages: [{ role: "user", text: "hi" }], dropped: 0, truncated: false };
  const result = await service.loadPluginSessionMessages(pluginSource("p.a", "one"), "s-1");
  assert.equal(result.ok, true);
  assert.deepEqual(result.response, { messages: [{ role: "user", text: "hi" }], truncated: false });
  assert.equal(bridge.calls.length, 1);
  const [command, args] = bridge.calls[0];
  assert.equal(command, "plugin_host_session_messages");
  assert.deepEqual(Object.keys(args).sort(), ["locale", "pluginId", "requestId", "sessionId", "sourceId"]);
  assert.equal(args.pluginId, "p.a");
  assert.equal(args.sourceId, "one");
  assert.equal(args.sessionId, "s-1");
  assert.equal(args.locale, "zh");
  assert.equal(typeof args.requestId, "string");
});

test("session sources: list request sends the project root only for the workspace scope", async () => {
  bridge.calls = [];
  bridge.response = { sessions: [{ id: "s1", title: "One" }], dropped: 0, truncated: true };
  const source = pluginSource("p.a", "one");
  const workspace = await service.loadPluginSessions(source, "workspace", "/work/app", undefined);
  assert.deepEqual(workspace, { ok: true, response: { sessions: [{ id: "s1", title: "One" }], truncated: true } });
  await service.loadPluginSessions(source, "all", "/work/app", undefined);
  const [[command, first], [, second]] = bridge.calls;
  assert.equal(command, "plugin_host_session_list");
  assert.deepEqual(Object.keys(first).sort(), ["locale", "pluginId", "projectRoot", "requestId", "scope", "sourceId"]);
  assert.equal(first.scope, "workspace");
  assert.equal(first.projectRoot, "/work/app");
  assert.deepEqual(Object.keys(second).sort(), ["locale", "pluginId", "requestId", "scope", "sourceId"]);
  assert.equal(second.scope, "all");
});
