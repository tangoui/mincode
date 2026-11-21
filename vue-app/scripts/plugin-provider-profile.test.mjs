import assert from "node:assert/strict";
import test from "node:test";
import {
  aiProviderSelectOptions,
  aiProviderSelectValue,
  fillAiProviderPlaceholder,
  pluginProviderProfilePatch,
  unresolvedBaseUrlPlaceholders,
} from "../src/domain/ai/plugin-provider-profile.ts";

const zhipuFree = {
  pluginId: "ai-provider-pack",
  pluginName: "AI Provider Pack",
  id: "ai-provider-pack.zhipu",
  label: "Zhipu (Free)",
  baseUrl: "https://open.bigmodel.cn/api/paas/v4/",
  host: "open.bigmodel.cn",
  placeholders: [],
  apiFormat: "openai",
  model: "GLM-4.7-Flash",
  models: [{ id: "GLM-4.7-Flash", free: true, tools: true }, { id: "GLM-4V-Flash", free: true, tools: false }],
  contextWindow: 128000,
  maxInputTokens: 120000,
  maxOutputTokens: 8192,
  tier: "free",
  region: "cn",
  verifiedAt: "2026-10-29",
  signupUrl: "https://open.bigmodel.cn/usercenter/apikeys",
};
const source = { pluginId: "ai-provider-pack", providerId: "ai-provider-pack.zhipu" };
const cloudflareUrl = "https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1";

test("aiProviderSelectValue keeps built-in values and prefixes plugin presets", () => {
  assert.equal(aiProviderSelectValue({ provider: "zhipu" }), "zhipu");
  assert.equal(aiProviderSelectValue({ provider: "plugin", source }), "plugin:ai-provider-pack.zhipu");
});

test("aiProviderSelectOptions orders built-in, plugin, then custom", () => {
  const options = aiProviderSelectOptions(["openai", "zhipu", "custom"], [zhipuFree], { provider: "zhipu" });
  assert.deepEqual(options.map((option) => option.value), ["openai", "zhipu", "plugin:ai-provider-pack.zhipu", "custom"]);
  assert.deepEqual(options.map((option) => option.kind), ["builtin", "builtin", "plugin", "builtin"]);
  assert.equal(options[2].provider, zhipuFree);
});

test("aiProviderSelectOptions keeps an orphan option for a profile whose plugin is gone", () => {
  const options = aiProviderSelectOptions(["openai", "custom"], [], { provider: "plugin", source });
  assert.deepEqual(options.at(-1), { value: "plugin:ai-provider-pack.zhipu", kind: "orphan" });
  assert.equal(aiProviderSelectOptions(["openai", "custom"], [zhipuFree], { provider: "plugin", source }).filter((option) => option.kind === "orphan").length, 0);
});

test("pluginProviderProfilePatch fills a profile like a built-in preset", () => {
  const patch = pluginProviderProfilePatch(zhipuFree);
  assert.equal(patch.provider, "plugin");
  assert.deepEqual(patch.source, source);
  assert.equal(patch.apiKey, "");
  assert.equal(patch.baseUrl, zhipuFree.baseUrl);
  assert.equal(patch.model, "GLM-4.7-Flash");
  assert.deepEqual(patch.commonModels, ["GLM-4.7-Flash", "GLM-4V-Flash"]);
  assert.deepEqual(patch.noToolModels, ["GLM-4V-Flash"]);
  assert.equal(patch.failoverEnabled, false);
  assert.deepEqual(patch.failoverModels, []);
  assert.equal(patch.contextWindow, 128000);
});

test("unresolvedBaseUrlPlaceholders lists unfilled names", () => {
  assert.deepEqual(unresolvedBaseUrlPlaceholders(cloudflareUrl), ["account_id"]);
  assert.deepEqual(unresolvedBaseUrlPlaceholders(zhipuFree.baseUrl), []);
});

test("fillAiProviderPlaceholder replaces a placeholder with a safe value", () => {
  const filled = fillAiProviderPlaceholder(cloudflareUrl, "account_id", "abc123");
  assert.equal(filled, "https://api.cloudflare.com/client/v4/accounts/abc123/ai/v1");
  assert.deepEqual(unresolvedBaseUrlPlaceholders(filled), []);
});

for (const value of ["evil.com/x", "a@b", "..", "x?y", "x#y", "", "a b", "x".repeat(129)]) {
  test(`fillAiProviderPlaceholder rejects ${JSON.stringify(value.slice(0, 12))}`, () => {
    assert.equal(fillAiProviderPlaceholder(cloudflareUrl, "account_id", value), undefined);
  });
}

test("fillAiProviderPlaceholder ignores a name the URL does not contain", () => {
  assert.equal(fillAiProviderPlaceholder(cloudflareUrl, "other", "abc"), undefined);
});
