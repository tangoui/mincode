import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { settingsIntegrations } from "./settings-integrations-module.mjs";

const { loadMcpRegistryEntry, searchMcpRegistry } = settingsIntegrations;

// Settings → AI → Discover lists only the built-in catalog and plugin catalogs
// (contributes.aiCatalogs). The connector & skill market plugin replaced the
// community registries (404), Tencent SkillHub and GitHub repository search.
const pluginEntry = {
  id: "plugin:acme.catalog/team/acme",
  name: "Acme",
  description: "",
  source: "plugin",
  installRef: "plugin:acme.catalog/team/acme",
};

function recordFetch(t) {
  const urls = [];
  t.mock.method(globalThis, "fetch", async (url) => {
    urls.push(String(url));
    return new Response("[]", { status: 200 });
  });
  return urls;
}

test("Discover: connector search never contacts the removed community registry", async (t) => {
  const urls = recordFetch(t);
  const all = await searchMcpRegistry("", async () => [pluginEntry]);
  assert.ok(all.some((entry) => entry.source === "builtin"), "built-in connectors are listed");
  assert.ok(all.some((entry) => entry.id === pluginEntry.id), "plugin catalog entries are listed");
  assert.ok(all.every((entry) => entry.source === "builtin" || entry.source === "plugin"));
  const filtered = await searchMcpRegistry("git", async () => []);
  assert.ok(filtered.some((entry) => entry.source === "builtin"));
  assert.deepEqual(urls, []);
});


test("Discover: installing a built-in entry still works offline", async (t) => {
  const urls = recordFetch(t);
  const [connector] = await searchMcpRegistry("File System");
  const servers = await loadMcpRegistryEntry(connector);
  assert.equal(servers[0].command, "npx");
  assert.deepEqual(urls, []);
});

test("Discover: the settings integrations module no longer imports SkillHub or GitHub repository search", async () => {
  const source = await readFile(new URL("../src/domain/ai/settings-integrations.ts", import.meta.url), "utf8");
  for (const removed of ["skill-repository", "parseTencentSkillHubResponse", "tencentSkillHubListUrl", "mcp-registry/main", "skill-registry/main"]) {
    assert.ok(!source.includes(removed), `settings-integrations.ts still references ${removed}`);
  }
});
