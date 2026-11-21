import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  AI_PROVIDER_LIMITS,
  aiProviderPreviewCount,
  aiProviderText,
  collectAiProviders,
} from "../src/domain/plugins/ai-providers.ts";

const repo = fileURLToPath(new URL("../../", import.meta.url));
const corpus = join(repo, "tools/verify/fixtures/contributions");
const contract = JSON.parse(await readFile(join(repo, "plugins/api/contribution-points.json"), "utf8"));

function contribution(pluginId, entries, overrides = {}) {
  return {
    pluginId,
    pluginName: `${pluginId} name`,
    version: "1.0.0",
    source: "local",
    runtime: "declarative",
    activeInWorkspace: true,
    point: { fingerprint: "f", entries, issues: [] },
    ...overrides,
  };
}

function validEntry(overrides = {}) {
  return {
    id: "acme.zhipu",
    label: "Zhipu (Free)",
    labelLocalized: { zh: "智谱（免费）" },
    baseUrl: "https://open.bigmodel.cn/api/paas/v4/",
    apiFormat: "openai",
    model: "GLM-4.7-Flash",
    models: [{ id: "GLM-4.7-Flash", free: true }, { id: "GLM-4V-Flash", free: true, tools: false }],
    contextWindow: 128000,
    maxInputTokens: 120000,
    maxOutputTokens: 8192,
    tier: "free",
    region: "cn",
    verifiedAt: "2026-10-29",
    quotaNote: "Free models with no time limit",
    signupUrl: "https://open.bigmodel.cn/usercenter/apikeys",
    ...overrides,
  };
}

test("limits mirror the contract", () => {
  const spec = contract.points.aiProviders;
  assert.equal(AI_PROVIDER_LIMITS.maxEntries, spec.maxEntries);
  for (const [key, value] of Object.entries(spec.limits)) assert.equal(AI_PROVIDER_LIMITS[key], value, key);
});

test("collectAiProviders accepts a valid entry and normalizes it", () => {
  const { providers, issues } = collectAiProviders([contribution("acme", [validEntry()])]);
  assert.deepEqual(issues, []);
  assert.equal(providers.length, 1);
  const [provider] = providers;
  assert.equal(provider.pluginId, "acme");
  assert.equal(provider.pluginName, "acme name");
  assert.equal(provider.host, "open.bigmodel.cn");
  assert.deepEqual(provider.placeholders, []);
  assert.deepEqual(provider.models.map((model) => model.tools), [true, false]);
});

test("collectAiProviders computes the host itself instead of trusting it", () => {
  const { providers } = collectAiProviders([contribution("acme", [validEntry({ host: "api.openai.com" })])]);
  assert.equal(providers[0].host, "open.bigmodel.cn");
});

test("collectAiProviders keeps a path placeholder", () => {
  const { providers, issues } = collectAiProviders([contribution("acme", [validEntry({
    baseUrl: "https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1",
    placeholders: [{ name: "account_id", label: "Account ID" }],
  })])]);
  assert.deepEqual(issues, []);
  assert.equal(providers[0].host, "api.cloudflare.com");
  assert.equal(providers[0].placeholders[0].name, "account_id");
});

const placeholder = (name) => ({ name, label: name });
const rejects = {
  "http base url": { baseUrl: "http://open.bigmodel.cn/v4" },
  userinfo: { baseUrl: "https://u:p@open.bigmodel.cn/v4" },
  "query string": { baseUrl: "https://open.bigmodel.cn/v4?x=1" },
  "host placeholder": { baseUrl: "https://{h}.example.com/v1", placeholders: [placeholder("h")] },
  "undeclared placeholder": { baseUrl: "https://api.example.com/{account_id}/v1" },
  "unused placeholder": { placeholders: [placeholder("account_id")] },
  "three placeholders": { baseUrl: "https://api.example.com/{a}/{b}/{c}", placeholders: [placeholder("a"), placeholder("b"), placeholder("c")] },
  "partner tier": { tier: "partner" },
  "unknown region": { region: "mars" },
  "unknown api format": { apiFormat: "gemini" },
  "model not listed": { model: "glm-5" },
  "no models": { models: [] },
  "limited without caveat": { tier: "limited" },
  "http signup url": { signupUrl: "http://open.bigmodel.cn/keys" },
  "bidi label": { label: "Zhipu\u202e" },
  "foreign id": { id: "other.zhipu" },
  "bad verified date": { verifiedAt: "2026/10/29" },
};
for (const [name, overrides] of Object.entries(rejects)) {
  test(`collectAiProviders rejects: ${name}`, () => {
    const { providers, issues } = collectAiProviders([contribution("acme", [validEntry(overrides)])]);
    assert.deepEqual(providers, []);
    assert.equal(issues.length, 1);
    assert.equal(issues[0].stage, "frontend");
  });
}

test("collectAiProviders drops duplicate ids and reports host issues", () => {
  const input = contribution("acme", [validEntry(), validEntry()]);
  input.point.issues = [{ entry: 3, id: "acme.bad", message: "bad" }];
  const { providers, issues } = collectAiProviders([input]);
  assert.equal(providers.length, 1);
  assert.deepEqual(issues.map((issue) => issue.stage), ["host", "frontend"]);
});

test("aiProviderText falls back from locale to language to the base text", () => {
  assert.equal(aiProviderText("智谱", { zh: "智谱（免费）" }, "zh-CN"), "智谱（免费）");
  assert.equal(aiProviderText("Zhipu", { zh: "智谱" }, "en"), "Zhipu");
  assert.equal(aiProviderText(undefined, undefined, "en"), "");
});

test("fixture corpus: frontend layer for aiProviders", async () => {
  const expectations = JSON.parse(await readFile(join(corpus, "expectations.json"), "utf8"));
  let checked = 0;
  for (const [name, expected] of Object.entries(expectations.cases)) {
    if (!name.includes("/aiProviders-")) continue;
    const valid = name.startsWith("valid/");
    if (!valid && !(expected.rejectedBy ?? []).includes("frontend")) continue;
    const dir = join(corpus, name);
    const manifest = JSON.parse(await readFile(join(dir, "plugin.json"), "utf8"));
    const entries = await Promise.all(manifest.contributes.aiProviders.map(async (entry) =>
      entry.path ? JSON.parse(await readFile(join(dir, entry.path), "utf8")) : entry));
    const { providers, issues } = collectAiProviders([contribution(manifest.id, entries)]);
    if (valid) {
      assert.deepEqual(issues, [], name);
      assert.equal(providers.length, expected.points.aiProviders, name);
    } else {
      assert.ok(issues.some((issue) => issue.stage === "frontend"), `${name}: frontend accepted`);
    }
    checked += 1;
  }
  assert.ok(checked >= 4, `checked ${checked} aiProviders fixtures`);
});

test("aiProviderPreviewCount counts inline and file entries for the install preview", () => {
  assert.equal(aiProviderPreviewCount({ aiProviders: [{ path: "providers/a.json" }, validEntry()] }), 2);
  assert.equal(aiProviderPreviewCount({ aiProviders: "x" }), 0);
  assert.equal(aiProviderPreviewCount(undefined), 0);
  assert.equal(aiProviderPreviewCount({ aiProviders: Array.from({ length: 40 }, () => ({})) }), AI_PROVIDER_LIMITS.maxEntries);
});
