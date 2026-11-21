import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  AI_CATALOG_LIMITS,
  AI_CATALOG_KINDS,
  aiCatalogPreviewSummary,
  collectAiCatalogs,
  parseAiCatalogInstallResponse,
  parseAiCatalogSearchResponse,
  pluginCatalogEntryId,
  validateAiCatalogEntry,
} from "../src/domain/plugins/ai-catalogs.ts";
import { settingsIntegrations } from "./settings-integrations-module.mjs";

const { aiSkillSource, describeMcpServersForReview, loadMcpRegistryEntry, loadSkillRegistryEntry, searchMcpRegistry, searchSkillRegistry } = settingsIntegrations;

const contractUrl = new URL("../../plugins/api/contribution-points.json", import.meta.url);

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

const team = { id: "team", kind: "mcp", title: "Team connectors", searchAction: "catalog-search", installAction: "catalog-install" };
const rules = { id: "rules", kind: "skill", title: "Team skills", titleLocalized: { zh: "团队技能" }, searchAction: "catalog-search", installAction: "catalog-install" };

test("aiCatalogs: limits and kinds mirror the contract", async () => {
  const contract = JSON.parse(await readFile(contractUrl, "utf8"));
  const point = contract.points?.aiCatalogs ?? contract.aiCatalogs;
  assert.ok(point, "contract entry exists");
  assert.equal(point.status, "enabled");
  assert.deepEqual([...AI_CATALOG_KINDS].sort(), [...point.lists.kinds].sort());
  assert.equal(AI_CATALOG_LIMITS.maxEntries, point.maxEntries);
  for (const [key, value] of Object.entries(AI_CATALOG_LIMITS)) {
    if (key === "maxEntries") continue;
    assert.equal(value, point.limits[key], `limit ${key}`);
  }
  for (const key of Object.keys(point.limits)) assert.ok(key in AI_CATALOG_LIMITS, `limit ${key} is mirrored`);
});

test("aiCatalogs: entries are re-validated per plugin", () => {
  assert.equal(validateAiCatalogEntry(team).ok, true);
  assert.equal(validateAiCatalogEntry({ ...team, searchAction: "" }).ok, false);
  assert.equal(validateAiCatalogEntry({ ...team, installAction: 3 }).ok, false);
  assert.equal(validateAiCatalogEntry({ ...team, descriptionLocalized: { en: "bad\u202etext" } }).ok, false);
  assert.equal(validateAiCatalogEntry({ ...team, kind: "prompt" }).ok, false);
  assert.equal(validateAiCatalogEntry({ ...team, id: "Team" }).ok, false);
  assert.equal(validateAiCatalogEntry({ ...team, title: "a\u202eb" }).ok, false);
  assert.equal(validateAiCatalogEntry({ ...team, description: "x".repeat(AI_CATALOG_LIMITS.descriptionMaxRunes + 1) }).ok, false);
  const { catalogs, issues } = collectAiCatalogs([
    contribution("z.catalog", [team, { ...team, title: "Again" }, rules]),
    contribution("a.catalog", [team, { ...team, id: "bad", kind: "x" }]),
    contribution("decl.catalog", [team], { runtime: "declarative" }),
    contribution("off.catalog", [team], { activeInWorkspace: false }),
  ]);
  assert.deepEqual(catalogs.map((catalog) => `${catalog.pluginId}/${catalog.id}/${catalog.kind}`), [
    "a.catalog/team/mcp",
    "z.catalog/team/mcp",
    "z.catalog/rules/skill",
  ]);
  assert.equal(catalogs[0].pluginName, "a.catalog name");
  assert.equal(issues.length, 2);
  assert.ok(issues.some((issue) => issue.pluginId === "z.catalog" && /duplicate/.test(issue.message)));
});

test("aiCatalogs: search results are plain text and keep the plugin as the source", () => {
  const response = parseAiCatalogSearchResponse({
    kind: "mcp",
    items: [
      { id: "io.github/acme", name: " Acme ", description: "Team server", docsUrl: "https://example.com/acme" },
      { id: "io.github/acme", name: "Duplicate" },
      { id: "bad id", name: "x" },
      { id: "js", name: "x", docsUrl: "javascript:alert(1)" },
      { id: "ctl", name: "a\u202eb" },
      { id: "plain", name: "Plain", install: { command: "rm" } },
    ],
    dropped: 2,
  });
  assert.deepEqual(response.items.map((item) => item.id), ["io.github/acme"]);
  assert.equal(response.items[0].name, "Acme");
  assert.equal(response.dropped, 7);
  assert.deepEqual(parseAiCatalogSearchResponse("nope"), { items: [], dropped: 0, truncated: false });
  assert.equal(pluginCatalogEntryId("acme.catalog", "team", "io.github/acme"), "plugin:acme.catalog/team/io.github/acme");
});

test("aiCatalogs: install content is bounded text", () => {
  assert.equal(parseAiCatalogInstallResponse({ kind: "skill", content: "---\nname: x\n---\nbody" }), "---\nname: x\n---\nbody");
  for (const value of [
    undefined,
    { content: "" },
    { content: "  \n" },
    { content: 3 },
    { content: "a\u0000b" },
    { content: "a\u202eb" },
    { content: "x".repeat(AI_CATALOG_LIMITS.installResultMaxBytes + 1) },
  ]) {
    assert.throws(() => parseAiCatalogInstallResponse(value), /invalid_plugin_catalog_content/);
  }
});



test("aiCatalogs: connector review shows the full command, URL, env and headers", () => {
  const text = describeMcpServersForReview([
    { id: "acme", transport: "stdio", command: "npx", args: "-y acme-mcp", url: "", enabled: true, env: { ACME_TOKEN: "abc" }, headers: {} },
    { id: "remote", transport: "http", command: "", args: "", url: "https://mcp.example.com", enabled: true, env: {}, headers: { "X-Team": "core" } },
  ]);
  assert.equal(text, "acme\n  npx -y acme-mcp\n  env ACME_TOKEN=abc\n\nremote\n  https://mcp.example.com\n  header X-Team: core");
});

test("aiCatalogs: install preview lists the catalogs", () => {
  assert.deepEqual(aiCatalogPreviewSummary({ aiCatalogs: [team, rules, { id: "x" }] }), [
    { kind: "mcp", title: "Team connectors" },
    { kind: "skill", title: "Team skills" },
  ]);
  assert.deepEqual(aiCatalogPreviewSummary(undefined), []);
});

async function sourceFiles(directory) {
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = new URL(entry.name + (entry.isDirectory() ? "/" : ""), directory);
    if (entry.isDirectory()) files.push(...(await sourceFiles(path)));
    else if (/\.(ts|vue|js)$/.test(entry.name)) files.push(path);
  }
  return files;
}

test("aiCatalogs: catalogs are never reachable from AI tools", async () => {
  const toolFiles = await sourceFiles(new URL("../src/services/ai-tools/", import.meta.url));
  assert.ok(toolFiles.length > 0);
  for (const file of toolFiles) {
    const source = await readFile(file, "utf8");
    assert.ok(!source.includes("plugin_host_ai_catalog"), `${fileURLToPath(file)} must not call plugin_host_ai_catalog_*`);
    assert.ok(!source.includes("plugin-ai-catalog-service"), `${fileURLToPath(file)} must not import the catalog service`);
  }
});
