import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import {
  SEARCH_PROVIDER_LIMITS,
  SEARCH_PROVIDER_RESERVED_PREFIXES,
  collectSearchProviders,
  normalizeSearchFileTarget,
  normalizeSearchResultURL,
  parseProviderQuery,
  parseSearchResultResponse,
  providerQueryReady,
  searchFileScopeIsCurrent,
  searchProviderPreviewSummary,
  searchResultURLHost,
  validateSearchProviderEntry,
} from "../src/domain/plugins/search-providers.ts";

const contract = JSON.parse(readFileSync(new URL("../src/domain/plugins/contribution-points.json", import.meta.url), "utf8"));
const spec = contract.points?.searchProviders ?? contract.searchProviders ?? Object.values(contract).find((value) => value?.searchProviders)?.searchProviders;

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

const entry = (overrides = {}) => ({ id: "issues", prefix: "gh", title: "GitHub issues", action: "search", ...overrides });

test("limits and reserved prefixes mirror the contribution contract", () => {
  assert.ok(spec, "searchProviders spec present");
  assert.equal(spec.status, "enabled");
  assert.equal(SEARCH_PROVIDER_LIMITS.maxEntries, spec.maxEntries);
  for (const [key, value] of Object.entries(SEARCH_PROVIDER_LIMITS)) {
    if (key === "maxEntries") continue;
    assert.equal(value, spec.limits[key], `limit ${key}`);
  }
  assert.deepEqual([...SEARCH_PROVIDER_RESERVED_PREFIXES].sort(), [...spec.lists.reservedPrefixes].sort());
});

test("entries are re-validated: prefix pattern, reserved prefixes, title and action", () => {
  assert.equal(validateSearchProviderEntry(entry()).ok, true);
  assert.equal(validateSearchProviderEntry(entry({ prefix: "g" })).ok, false);
  assert.equal(validateSearchProviderEntry(entry({ prefix: "Gh" })).ok, false);
  assert.equal(validateSearchProviderEntry(entry({ prefix: "a".repeat(17) })).ok, false);
  assert.equal(validateSearchProviderEntry(entry({ prefix: "git" })).ok, false);
  assert.equal(validateSearchProviderEntry(entry({ prefix: "https" })).ok, false);
  assert.equal(validateSearchProviderEntry(entry({ title: "bad\u202etitle" })).ok, false);
  assert.equal(validateSearchProviderEntry(entry({ action: "" })).ok, false);
  assert.equal(validateSearchProviderEntry(entry({ minQueryLength: 9 })).ok, false);
});

test("collect keeps active process plugins and gives a prefix to the lowest plugin id", () => {
  const { providers, issues } = collectSearchProviders([
    contribution("zeta", [entry({ id: "z" })]),
    contribution("alpha", [entry({ id: "a" }), entry({ id: "a", prefix: "docs" })]),
    contribution("inactive", [entry({ prefix: "inact" })], { activeInWorkspace: false }),
    contribution("decl", [entry({ prefix: "decl" })], { runtime: "declarative" }),
  ]);
  assert.deepEqual(providers.map((provider) => `${provider.pluginId}:${provider.prefix}`), ["alpha:gh"]);
  assert.equal(providers[0].action, undefined, "the action never reaches the palette");
  assert.ok(issues.some((issue) => issue.pluginId === "alpha" && /duplicate/.test(issue.message)));
  assert.ok(issues.some((issue) => issue.pluginId === "zeta" && /already used by plugin "alpha"/.test(issue.message)));
});

test("parseProviderQuery matches only registered prefixes and caps the query", () => {
  const { providers } = collectSearchProviders([contribution("alpha", [entry({ minQueryLength: 2 })])]);
  assert.equal(parseProviderQuery("gx:foo", providers), undefined);
  assert.equal(parseProviderQuery("> gh:foo", providers), undefined);
  const parsed = parseProviderQuery("  gh:  bug 12  ", providers);
  assert.equal(parsed?.provider.prefix, "gh");
  assert.equal(parsed?.query, "bug 12");
  assert.equal(providerQueryReady(parsed), true);
  assert.equal(providerQueryReady(parseProviderQuery("gh:b", providers)), false);
  assert.equal([...parseProviderQuery(`gh:${"é".repeat(400)}`, providers).query].length, SEARCH_PROVIDER_LIMITS.queryMaxRunes);
});

test("URLs are http(s) only, without credentials or whitespace", () => {
  assert.equal(normalizeSearchResultURL("https://example.com/a?b=1"), "https://example.com/a?b=1");
  assert.equal(normalizeSearchResultURL("javascript:alert(1)"), undefined);
  assert.equal(normalizeSearchResultURL("file:///etc/passwd"), undefined);
  assert.equal(normalizeSearchResultURL("https://user:pw@example.com/"), undefined);
  assert.equal(normalizeSearchResultURL("https://example.com/a b"), undefined);
  assert.equal(normalizeSearchResultURL("https://example.com\\@evil.com"), undefined);
  assert.equal(normalizeSearchResultURL(`https://example.com/${"a".repeat(2100)}`), undefined);
  assert.equal(searchResultURLHost("https://example.com:8443/x"), "example.com:8443");
});

test("responses are re-validated; invalid and duplicate items are dropped and counted", () => {
  const response = parseSearchResultResponse({
    dropped: 2,
    items: [
      { id: "1", title: "Open view", target: { type: "view", view: "main", args: { id: 7 } } },
      { id: "2", title: "Run", detail: "read", target: { type: "command", command: "lookup" } },
      { id: "3", title: "Docs", target: { type: "url", url: "https://example.com/docs" } },
      { id: "3", title: "Duplicate", target: { type: "url", url: "https://example.com/docs" } },
      { id: "4", title: "Bad url", target: { type: "url", url: "javascript:void(0)" } },
      { id: "5", title: "Mixed", target: { type: "view", view: "main", url: "https://x.test" } },
      { id: "6", title: "line\nbreak", target: { type: "command", command: "lookup" } },
      { id: "7", title: "Url args", target: { type: "url", url: "https://x.test", args: { a: 1 } } },
      { id: "8", title: "Nested args", target: { type: "view", view: "main", args: { a: { b: 1 } } } },
    ],
  });
  assert.deepEqual(response.items.map((item) => item.id), ["1", "2", "3"]);
  assert.equal(response.dropped, 2 + 6);
  assert.equal(response.truncated, false);
  const many = parseSearchResultResponse({
    items: Array.from({ length: 60 }, (_, index) => ({ id: `i${index}`, title: `T${index}`, target: { type: "view", view: "v" } })),
  });
  assert.equal(many.items.length, SEARCH_PROVIDER_LIMITS.itemsMax);
  assert.equal(many.truncated, true);
  assert.deepEqual(parseSearchResultResponse(null), { items: [], dropped: 0, truncated: false });
});

test("install preview lists valid prefixes", () => {
  assert.deepEqual(searchProviderPreviewSummary({ searchProviders: [entry(), { prefix: "X" }, entry({ prefix: "docs", title: "" })] }), [
    { prefix: "gh", title: "GitHub issues" },
    { prefix: "docs", title: "docs" },
  ]);
  assert.deepEqual(searchProviderPreviewSummary(undefined), []);
});

const fileScope = { workspaceId: "ws-a", root: "/projects/a", revision: "7" };
const providerRuntime = { instance: "3", generation: "1" };
const fileItem = (id, target) => ({ id, title: `File ${id}`, target: { type: "file", ...target } });

test("parses utf8 and line column bounds for file targets", () => {
  assert.equal(SEARCH_PROVIDER_LIMITS.pathMaxLength, 1024);
  assert.equal(SEARCH_PROVIDER_LIMITS.lineMax, 10_000_000);
  assert.equal(SEARCH_PROVIDER_LIMITS.columnMax, 10_000_000);
  // 341 three-byte runes + "a" = 1024 bytes; 342 runes = 1026 bytes.
  const fits = `${"界".repeat(341)}a`;
  const tooLong = "界".repeat(342);
  assert.deepEqual(normalizeSearchFileTarget({ type: "file", path: "src/main.go" }), { type: "file", path: "src/main.go" });
  assert.deepEqual(normalizeSearchFileTarget({ type: "file", path: "src/main.go", line: 12, column: 3 }), { type: "file", path: "src/main.go", line: 12, column: 3 });
  assert.deepEqual(normalizeSearchFileTarget({ type: "file", path: "a.go", line: 10_000_000, column: 10_000_000 }), { type: "file", path: "a.go", line: 10_000_000, column: 10_000_000 });
  assert.equal(normalizeSearchFileTarget({ type: "file", path: fits })?.path, fits);
  for (const bad of [
    { path: tooLong },
    { path: "" },
    { path: "../x" },
    { path: ".." },
    { path: "./x" },
    { path: "a//b" },
    { path: "a/" },
    { path: "/etc/passwd" },
    { path: "a\\b" },
    { path: "C:/x" },
    { path: "a/../b" },
    { path: "a\u202eb" },
    { path: "a\u0000b" },
    { path: "a\ud800b" },
    { path: "~/x" },
    { path: "a.go", line: 0 },
    { path: "a.go", line: -1 },
    { path: "a.go", line: 1.5 },
    { path: "a.go", line: 10_000_001 },
    { path: "a.go", line: "3" },
    { path: "a.go", column: 2 },
    { path: "a.go", line: 1, column: 0 },
    { path: "a.go", line: 1, column: 10_000_001 },
    { path: "a.go", url: "https://x.test" },
    { path: "a.go", args: { a: 1 } },
    { path: "a.go", extra: true },
  ]) {
    assert.equal(normalizeSearchFileTarget({ type: "file", ...bad }), undefined, JSON.stringify(bad));
  }
});

test("file items need host fileScope and providerRuntime or are dropped and counted", () => {
  const items = [
    { id: "v", title: "View", target: { type: "view", view: "main" } },
    { id: "u", title: "Docs", target: { type: "url", url: "https://example.com/docs" } },
    fileItem("f", { path: "src/main.go", line: 4, column: 2 }),
  ];
  const legacy = parseSearchResultResponse({ items });
  assert.deepEqual(legacy.items.map((item) => item.id), ["v", "u"]);
  assert.equal(legacy.dropped, 1);
  assert.equal(legacy.fileScope, undefined);
  for (const partial of [
    { fileScope },
    { providerRuntime },
    { fileScope: { ...fileScope, revision: "x" }, providerRuntime },
    { fileScope: { ...fileScope, root: "" }, providerRuntime },
    { fileScope, providerRuntime: { instance: "0", generation: "1" } },
    { fileScope, providerRuntime: { instance: "01", generation: "1" } },
  ]) {
    const response = parseSearchResultResponse({ items, ...partial });
    assert.deepEqual(response.items.map((item) => item.id), ["v", "u"], JSON.stringify(partial));
    assert.equal(response.dropped, 1);
    assert.equal(response.fileScope, undefined);
    assert.equal(response.providerRuntime, undefined);
  }
  const negotiated = parseSearchResultResponse({ items: [...items, fileItem("bad", { path: "../x" })], fileScope, providerRuntime });
  assert.deepEqual(negotiated.items.map((item) => item.id), ["v", "u", "f"]);
  assert.deepEqual(negotiated.items[2].target, { type: "file", path: "src/main.go", line: 4, column: 2 });
  assert.equal(negotiated.dropped, 1);
  assert.deepEqual(negotiated.fileScope, fileScope);
  assert.deepEqual(negotiated.providerRuntime, providerRuntime);
});

test("file scope matches only the same ready workspace, root and revision", () => {
  assert.equal(searchFileScopeIsCurrent(fileScope, { ...fileScope, ready: true }), true);
  assert.equal(searchFileScopeIsCurrent(fileScope, undefined), false);
  assert.equal(searchFileScopeIsCurrent(fileScope, { ...fileScope, ready: false }), false);
  assert.equal(searchFileScopeIsCurrent(fileScope, { ...fileScope, revision: "8", ready: true }), false);
  assert.equal(searchFileScopeIsCurrent(fileScope, { ...fileScope, root: "/projects/b", ready: true }), false);
  assert.equal(searchFileScopeIsCurrent(fileScope, { ...fileScope, workspaceId: "ws-b", ready: true }), false);
});

test("the palette loads the file target service lazily", () => {
  const source = readFileSync(new URL("../src/composables/use-command-palette.ts", import.meta.url), "utf8");
  assert.match(source, /import\(\s*"@\/services\/plugin-search-file-targets"\s*\)/);
  assert.doesNotMatch(source, /from "@\/services\/plugin-search-file-targets"/);
});
