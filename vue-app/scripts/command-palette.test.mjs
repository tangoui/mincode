import assert from "node:assert/strict";
import test from "node:test";
import {
  filterCommandPaletteItems,
  groupCommandPaletteItems,
  parseCommandPaletteQuery,
  pluginServicePaletteItems,
} from "../src/domain/commands/command-palette.ts";
import { paletteServiceInput, servicesForPalette } from "../src/domain/plugins/services.ts";
import {
  parseProjectCodeSearchResponse,
  renderProjectCodeSearchResult,
} from "../src/services/project-search.ts";

const items = [
  { id: "settings", kind: "command", title: "Open Settings", keywords: ["preferences"], priority: 1, action: { type: "open-settings" } },
  { id: "projects", kind: "workspace", title: "本地项目", keywords: ["projects"], priority: 2, action: { type: "open-view", view: "projects" } },
  { id: "tab", kind: "tab", title: "Database", subtitle: "Workspace A", priority: 3, action: { type: "select-tab", tabId: "tab-1" } },
  { id: "file", kind: "file", title: "command-palette.ts", subtitle: "/src/domain/commands", priority: 4, action: { type: "open-file", root: "/repo", path: "/repo/command-palette.ts" } },
  { id: "db", kind: "database", title: "Production replica", subtitle: "PostgreSQL", keywords: ["analytics"], priority: 5, action: { type: "open-database", connectionId: "db-1" } },
];

test("parses command, navigation, resource, and all modes", () => {
  assert.deepEqual(parseCommandPaletteQuery("  > set"), { mode: "commands", query: "set", prefix: ">" });
  assert.equal(parseCommandPaletteQuery("@tab").mode, "navigation");
  assert.equal(parseCommandPaletteQuery("#db").mode, "resources");
  assert.equal(parseCommandPaletteQuery("项目").mode, "all");
});

test("matches case-insensitively and supports Chinese text", () => {
  assert.equal(filterCommandPaletteItems(items, "SETTINGS")[0]?.id, "settings");
  assert.equal(filterCommandPaletteItems(items, "项目")[0]?.id, "projects");
});

test("ranks title prefixes before subtitle and keyword matches", () => {
  const ranked = filterCommandPaletteItems(items, "data");
  assert.equal(ranked[0]?.id, "tab");
  assert.equal(filterCommandPaletteItems(items, "analytics")[0]?.id, "db");
});

test("filters each explicit mode to its supported kinds", () => {
  assert.deepEqual(filterCommandPaletteItems(items, ">").map((item) => item.kind), ["command", "workspace"]);
  assert.deepEqual(filterCommandPaletteItems(items, "@").map((item) => item.kind), ["tab", "file"]);
  assert.deepEqual(filterCommandPaletteItems(items, "#").map((item) => item.kind), ["database"]);
});

test("supports fuzzy subsequence matching", () => {
  assert.equal(filterCommandPaletteItems(items, "cmdplt")[0]?.id, "file");
  assert.equal(filterCommandPaletteItems(items, "prdrplc")[0]?.id, "db");
});

test("keeps stable priority order for empty queries and respects limits", () => {
  assert.deepEqual(filterCommandPaletteItems(items, "", 3).map((item) => item.id), ["settings", "projects", "tab"]);
  assert.equal(filterCommandPaletteItems(items, "", 0).length, 0);
});

test("ranks recent and frequent items ahead of unused peers", () => {
  const now = 2_000_000_000_000;
  const ranked = filterCommandPaletteItems([
    { ...items[0], id: "unused", priority: 0 },
    { ...items[0], id: "frequent", priority: 10, usageCount: 8, lastUsedAt: now - 10_000 },
    { ...items[0], id: "recent", priority: 20, usageCount: 1, lastUsedAt: now },
  ], "");
  assert.deepEqual(ranked.map((item) => item.id), ["frequent", "recent", "unused"]);
});

test("keeps textual match score ahead of usage ranking", () => {
  const ranked = filterCommandPaletteItems([
    { ...items[0], id: "prefix", title: "Settings panel" },
    { ...items[0], id: "used-subtitle", title: "Other", subtitle: "Settings", usageCount: 99, lastUsedAt: Date.now() },
  ], "settings");
  assert.equal(ranked[0]?.id, "prefix");
});

test("groups results by kind while keeping the ranked order flat", () => {
  const ranked = [
    { id: "a", kind: "workspace", title: "A", action: { type: "open-settings" } },
    { id: "b", kind: "command", title: "B", action: { type: "open-settings" } },
    { id: "c", kind: "workspace", title: "C", action: { type: "open-settings" } },
    { id: "d", kind: "command", title: "D", action: { type: "open-settings" } },
  ];
  const groups = groupCommandPaletteItems(ranked);

  // The top-ranked item's kind leads, and each group keeps its ranked order.
  assert.deepEqual(groups.map((group) => group.kind), ["workspace", "command"]);
  assert.deepEqual(
    groups.flatMap((group) => group.entries.map((entry) => entry.item.id)),
    ["a", "c", "b", "d"],
  );
  // Flat indexes follow the rendered order so arrow keys track the visible list.
  assert.deepEqual(
    groups.flatMap((group) => group.entries.map((entry) => entry.index)),
    [0, 1, 2, 3],
  );
});

test("groups an empty result set into no sections", () => {
  assert.deepEqual(groupCommandPaletteItems([]), []);
});

test("splits project search results by their explicit group label", () => {
  const ranked = [
    { id: "1", kind: "file", title: "a.ts", group: "min", action: { type: "open-file", root: "/min", path: "a.ts" } },
    { id: "2", kind: "file", title: "b.ts", group: "docs", action: { type: "open-file", root: "/docs", path: "b.ts" } },
    { id: "3", kind: "file", title: "c.ts", group: "min", action: { type: "open-file", root: "/min", path: "c.ts" } },
  ];
  const groups = groupCommandPaletteItems(ranked);

  // Same kind throughout, so only the project label may split these.
  assert.deepEqual(groups.map((group) => group.label), ["min", "docs"]);
  assert.deepEqual(
    groups.flatMap((group) => group.entries.map((entry) => entry.item.id)),
    ["1", "3", "2"],
  );
  assert.deepEqual(
    groups.flatMap((group) => group.entries.map((entry) => entry.index)),
    [0, 1, 2],
  );
});

test("leaves kind groups unlabelled so the caller localizes them", () => {
  const [group] = groupCommandPaletteItems([
    { id: "x", kind: "command", title: "X", action: { type: "open-settings" } },
  ]);
  assert.equal(group.label, undefined);
  assert.equal(group.kind, "command");
  assert.equal(group.key, "command");
});

test("maps native code-search matches to file palette render models", () => {
  const response = parseProjectCodeSearchResponse({
    query: "renderResult",
    mode: "symbol",
    engine: "rg",
    filesConsidered: 4,
    results: [{
      relativePath: "src/services/project-search.ts",
      kind: "symbol",
      line: 111,
      column: 17,
      lineText: "export function renderProjectCodeSearchResult(",
      score: 90,
      reason: "declaration",
    }],
    truncated: false,
    durationMs: 12,
  });
  const item = renderProjectCodeSearchResult("/repo", response.results[0]);
  assert.deepEqual(item, {
    kind: "file",
    title: "project-search.ts",
    subtitle: "src/services/project-search.ts",
    search: {
      scope: { path: "src/services/project-search.ts" },
      detail: "export function renderProjectCodeSearchResult(",
      line: 111,
      column: 17,
    },
    action: {
      type: "open-file",
      root: "/repo",
      path: "src/services/project-search.ts",
      line: 111,
      column: 17,
    },
  });
});

function serviceEntry(overrides = {}) {
  return {
    pluginId: "demo.provider",
    pluginName: "Demo Provider",
    entryId: "reverse",
    service: "demo.text.reverse",
    title: "Reverse text",
    description: "Reverse the characters",
    input: { kind: "text", maxBytes: 8192 },
    output: { kind: "text" },
    surfaces: ["selection", "palette"],
    risk: "read",
    timeoutMs: 30000,
    isDefault: false,
    ...overrides,
  };
}

test("plugin services: lists palette services with the localized prefix and a plugin-service action", () => {
  const entries = [
    serviceEntry(),
    serviceEntry({ pluginId: "other.provider", pluginName: "Other", entryId: "rev" }),
    serviceEntry({ service: "demo.text.upper", entryId: "upper", title: "Upper case", surfaces: ["selection"] }),
    serviceEntry({ service: "demo.image.resize", entryId: "resize", title: "Resize image", description: "", input: { kind: "files", accepts: ["image/*"], maxBytes: 1024, maxFiles: 4 }, surfaces: ["palette"] }),
  ];
  const items = pluginServicePaletteItems(servicesForPalette(entries), "Service: ");
  assert.deepEqual(items.map((item) => item.title), ["Service: Resize image", "Service: Reverse text"]);
  assert.ok(items.every((item) => item.kind === "plugin-service"));
  assert.deepEqual(items[1].action, { type: "plugin-service", service: "demo.text.reverse" });
  assert.equal(items[1].id, "plugin-service:demo.text.reverse");
  // Both providers of a merged service can be found by plugin name.
  assert.ok(items[1].keywords.includes("Other"));
  assert.ok(items[1].keywords.includes("demo.text.reverse"));
  // Services are commands: the ">" mode lists them, the "@" mode does not.
  assert.deepEqual(filterCommandPaletteItems(items, ">reverse").map((item) => item.id), ["plugin-service:demo.text.reverse"]);
  assert.deepEqual(filterCommandPaletteItems(items, "@reverse"), []);
  assert.equal(groupCommandPaletteItems(items)[0].kind, "plugin-service");
});

test("plugin services: palette items show the title in the UI language and stay findable by the untranslated one", () => {
  const entries = [serviceEntry({ titleLocalized: { zh: "反转文字" } }), serviceEntry({ service: "demo.text.upper", entryId: "upper", title: "Upper case" })];
  const items = pluginServicePaletteItems(servicesForPalette(entries, ["zh"]), "服务：");
  assert.deepEqual(items.map((item) => item.title).sort(), ["服务：Upper case", "服务：反转文字"].sort());
  const reverse = items.find((item) => item.action.service === "demo.text.reverse");
  assert.ok(reverse.keywords.includes("反转文字"));
  assert.ok(reverse.keywords.includes("Reverse text"));
  assert.equal(filterCommandPaletteItems(items, ">reverse text")[0]?.id, "plugin-service:demo.text.reverse");
  assert.equal(pluginServicePaletteItems(servicesForPalette(entries, "en"), "Service: ").find((item) => item.action.service === "demo.text.reverse").title, "Service: Reverse text");
});

test("plugin services: palette input follows the preferred provider's input shape", () => {
  const group = (entries) => servicesForPalette(entries)[0];
  assert.deepEqual(paletteServiceInput(group([serviceEntry({ input: { kind: "none" } })])), { kind: "none" });
  assert.deepEqual(paletteServiceInput(group([serviceEntry({ input: { kind: "text", maxBytes: 100 } }), serviceEntry({ pluginId: "b.provider", input: { kind: "text", maxBytes: 300 } })])), { kind: "text", maxBytes: 300 });
  const files = paletteServiceInput(group([serviceEntry({ input: { kind: "files", accepts: [".csv", "image/png"], maxBytes: 10, maxFiles: 3 } })]));
  assert.deepEqual(files, { kind: "files", accepts: [".csv", "image/png"], extensions: [".csv", ".png"], maxFiles: 3 });
  const single = paletteServiceInput(group([serviceEntry({ input: { kind: "file", accepts: ["image/*"], maxBytes: 10 } })]));
  assert.equal(single.kind, "file");
  assert.equal(single.maxFiles, 1);
  assert.ok(single.extensions.includes(".png") && single.extensions.includes(".jpeg") && !single.extensions.includes(".pdf"));
  // The default provider decides the shape when providers disagree.
  assert.equal(paletteServiceInput(group([serviceEntry(), serviceEntry({ pluginId: "b.provider", isDefault: true, input: { kind: "none" } })])).kind, "none");
});
