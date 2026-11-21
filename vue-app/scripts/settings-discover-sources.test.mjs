import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { settingsIntegrations } from "./settings-integrations-module.mjs";

const { loadMcpRegistryEntry, loadSkillRegistryEntry, searchMcpRegistry, searchSkillRegistry } = settingsIntegrations;

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
  const all = await searchMcpRegistry("");
  assert.ok(all.some((entry) => entry.source === "builtin"), "built-in connectors are listed");
  assert.ok(all.every((entry) => entry.source === "builtin"));
  const filtered = await searchMcpRegistry("git");
  assert.ok(filtered.some((entry) => entry.source === "builtin"));
  assert.deepEqual(urls, []);
});

test("Discover: skill search never contacts the community registry, SkillHub or GitHub", async (t) => {
  const urls = recordFetch(t);
  const all = await searchSkillRegistry("");
  assert.ok(all.some((entry) => entry.source === "builtin"), "built-in skills are listed");
  assert.ok(all.every((entry) => entry.source === "builtin"));
  await searchSkillRegistry("review");
  const repository = await searchSkillRegistry("https://github.com/mattpocock/skills");
  assert.deepEqual(repository.map((entry) => entry.id), [], "a repository URL is an ordinary keyword");
  assert.deepEqual(urls, []);
});

test("Discover: installing a built-in entry still works offline", async (t) => {
  const urls = recordFetch(t);
  const [connector] = await searchMcpRegistry("File System");
  const servers = await loadMcpRegistryEntry(connector);
  assert.equal(servers[0].command, "npx");
  const [skill] = await searchSkillRegistry("代码审查");
  assert.equal((await loadSkillRegistryEntry(skill)).name, "代码审查助手");
  assert.deepEqual(urls, []);
});

test("Discover: the settings integrations module no longer imports SkillHub or GitHub repository search", async () => {
  const source = await readFile(new URL("../src/domain/ai/settings-integrations.ts", import.meta.url), "utf8");
  for (const removed of ["skill-repository", "parseTencentSkillHubResponse", "tencentSkillHubListUrl", "mcp-registry/main", "skill-registry/main"]) {
    assert.ok(!source.includes(removed), `settings-integrations.ts still references ${removed}`);
  }
});
