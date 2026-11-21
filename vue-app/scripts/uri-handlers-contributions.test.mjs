import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import {
  URI_HANDLER_LIMITS,
  URI_HANDLER_RESERVED_HOSTS,
  collectUriHandlers,
  createPluginLinkGate,
  isPluginLinkCandidate,
  parseResolvedPluginLink,
  parseUriHandlerPath,
  pluginLinkPluginId,
  sameResolvedPluginLink,
  uriHandlerPreviewSummary,
  validateUriHandlerEntry,
} from "../src/domain/plugins/uri-handlers.ts";

const contract = JSON.parse(readFileSync(new URL("../src/domain/plugins/contribution-points.json", import.meta.url), "utf8"));
const spec = contract.points?.uriHandlers ?? contract.uriHandlers ?? Object.values(contract).find((value) => value?.uriHandlers)?.uriHandlers;

function contribution(pluginId, entries, extra = {}) {
  return {
    pluginId,
    pluginName: `${pluginId} name`,
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: `${pluginId}:${entries.length}`, entries, issues: [] },
    ...extra,
  };
}

const viewEntry = (overrides = {}) => ({ id: "open-issue", path: "/issue/{id}", target: { type: "view", view: "issues" }, ...overrides });

function resolved(overrides = {}) {
  return {
    pluginId: "acme.tracker",
    pluginName: "Tracker",
    pluginVersion: "1.2.0",
    handlerId: "open-issue",
    title: "Open issue",
    target: { type: "view", view: "issues", viewTitle: "Issues" },
    path: "/issue/42",
    pathParams: { id: "42" },
    params: { tab: "comments", pinned: true },
    ...overrides,
  };
}

test("limits and reserved hosts mirror the contribution contract", () => {
  assert.ok(spec, "uriHandlers spec present");
  assert.equal(spec.status, "enabled");
  assert.equal(URI_HANDLER_LIMITS.maxEntries, spec.maxEntries);
  for (const [key, value] of Object.entries(URI_HANDLER_LIMITS)) {
    if (key === "maxEntries") continue;
    assert.equal(value, spec.limits[key], `limit ${key}`);
  }
  assert.deepEqual([...URI_HANDLER_RESERVED_HOSTS].sort(), [...spec.lists.reservedHosts].sort());
});

test("handler paths and entries are re-validated", () => {
  assert.deepEqual(parseUriHandlerPath("/issue/{id}"), [{ literal: "issue" }, { param: "id" }]);
  assert.equal(parseUriHandlerPath("issue"), undefined);
  assert.equal(parseUriHandlerPath("/a/{id}/{id}"), undefined);
  assert.equal(parseUriHandlerPath("/A"), undefined);
  assert.equal(parseUriHandlerPath(`/${Array.from({ length: 9 }, () => "a").join("/")}`), undefined);
  assert.equal(validateUriHandlerEntry(viewEntry()).ok, true);
  assert.equal(validateUriHandlerEntry(viewEntry({ target: { type: "url", url: "https://x" } })).ok, false);
  assert.equal(validateUriHandlerEntry(viewEntry({ target: { type: "command", command: "show" } })).ok, false, "command paths have no parameters");
  assert.equal(validateUriHandlerEntry(viewEntry({ path: "/status", target: { type: "command", command: "show" } })).ok, true);
  assert.equal(validateUriHandlerEntry(viewEntry({ title: "bad\u2066title" })).ok, false);
});

test("collect skips inactive, non-process and reserved-host plugins", () => {
  const { handlers, issues } = collectUriHandlers([
    contribution("acme.tracker", [viewEntry(), viewEntry()]),
    contribution("settings", [viewEntry()]),
    contribution("off", [viewEntry()], { activeInWorkspace: false }),
  ]);
  assert.deepEqual(handlers.map((handler) => `${handler.pluginId}:${handler.id}`), ["acme.tracker:open-issue"]);
  assert.ok(issues.some((issue) => issue.pluginId === "acme.tracker" && /duplicate/.test(issue.message)));
  assert.ok(issues.some((issue) => issue.pluginId === "settings" && /reserved/.test(issue.message)));
});

test("plugin link candidates exclude reserved hosts and unsafe text", () => {
  assert.equal(pluginLinkPluginId("mincode://acme.tracker/issue/42"), "acme.tracker");
  assert.equal(pluginLinkPluginId("mincode://settings/general"), undefined);
  assert.equal(isPluginLinkCandidate("mincode://acme.tracker/issue/42?tab=x"), true);
  assert.equal(isPluginLinkCandidate("mincode://acme.tracker"), false);
  assert.equal(isPluginLinkCandidate("https://acme.tracker/issue"), false);
  assert.equal(isPluginLinkCandidate("mincode://acme.tracker/a\u202eb"), false);
  assert.equal(isPluginLinkCandidate(`mincode://acme.tracker/${"a".repeat(2100)}`), false);
});

test("resolve responses are re-validated", () => {
  assert.deepEqual(parseResolvedPluginLink(resolved()), resolved());
  assert.equal(parseResolvedPluginLink(resolved({ pluginId: "Bad Id" })), undefined);
  assert.equal(parseResolvedPluginLink(resolved({ target: { type: "url", url: "https://x" } })), undefined);
  assert.equal(parseResolvedPluginLink(resolved({ params: { nested: { a: 1 } } })), undefined);
  assert.equal(parseResolvedPluginLink(resolved({ pathParams: { id: "4\n2" } })), undefined);
  const command = parseResolvedPluginLink(resolved({ target: { type: "command", command: "show" }, pathParams: {}, payload: "{\"a\":1}" }));
  assert.equal(command?.target.type, "command");
  assert.equal(command?.target.commandTitle, "show");
  assert.equal(command?.payload, "{\"a\":1}");
});

test("a second resolve must match what the user confirmed", () => {
  const first = parseResolvedPluginLink(resolved());
  assert.equal(sameResolvedPluginLink(first, parseResolvedPluginLink(resolved({ title: "Renamed", params: { pinned: true, tab: "comments" } }))), true);
  assert.equal(sameResolvedPluginLink(first, parseResolvedPluginLink(resolved({ pluginVersion: "1.3.0" }))), false);
  assert.equal(sameResolvedPluginLink(first, parseResolvedPluginLink(resolved({ target: { type: "view", view: "other" } }))), false);
  assert.equal(sameResolvedPluginLink(first, parseResolvedPluginLink(resolved({ params: { tab: "comments", pinned: false } }))), false);
});

test("the link gate dedupes identical links within 5 s and bounds the queue", () => {
  let now = 1_000;
  const gate = createPluginLinkGate({ now: () => now });
  assert.equal(gate.offer("mincode://a/x"), "accepted");
  assert.equal(gate.offer("mincode://a/x"), "duplicate");
  now += 4_999;
  assert.equal(gate.offer("mincode://a/x"), "duplicate");
  now += 1;
  assert.equal(gate.offer("mincode://a/x"), "accepted");
  assert.equal(gate.offer("mincode://a/y"), "accepted");
  assert.equal(gate.offer("mincode://a/z"), "overflow");
  assert.equal(gate.waiting, URI_HANDLER_LIMITS.pendingLinksMax);
  assert.equal(gate.take(), "mincode://a/x");
  assert.equal(gate.offer("mincode://a/z"), "accepted");
  assert.deepEqual([gate.take(), gate.take(), gate.take(), gate.take()], ["mincode://a/x", "mincode://a/y", "mincode://a/z", undefined]);
});

test("install preview lists handler paths and targets", () => {
  assert.deepEqual(
    uriHandlerPreviewSummary({ uriHandlers: [viewEntry(), { path: "/status", target: { type: "command", command: "show" } }, { path: "bad" }] }),
    [
      { path: "/issue/{id}", target: "view issues" },
      { path: "/status", target: "command show" },
    ],
  );
});
