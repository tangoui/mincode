import assert from "node:assert/strict";
import test from "node:test";
import { readSource } from "./read-source.mjs";
import {
  AI_ANTHROPIC_AUTH_MODES,
  normalizeAnthropicAuth,
} from "../src/domain/ai/anthropic-auth.ts";

const source = (path) => readSource(new URL(path, import.meta.url));

const requestBuilders = [
  "../src/components/database/query/DatabaseAiQueryDialog.vue",
  "../src/components/ai/AiAssistantDrawer.vue",
  "../src/services/ai-agent-loop.ts",
];

test("Anthropic auth modes are x-api-key and bearer", () => {
  assert.deepEqual([...AI_ANTHROPIC_AUTH_MODES], ["x-api-key", "bearer"]);
});

test("normalizeAnthropicAuth keeps bearer and defaults everything else to x-api-key", () => {
  assert.equal(normalizeAnthropicAuth("bearer"), "bearer");
  assert.equal(normalizeAnthropicAuth("x-api-key"), "x-api-key");
  assert.equal(normalizeAnthropicAuth("Bearer"), "x-api-key");
  assert.equal(normalizeAnthropicAuth(undefined), "x-api-key");
  assert.equal(normalizeAnthropicAuth(null), "x-api-key");
  assert.equal(normalizeAnthropicAuth(1), "x-api-key");
});

test("every AI request builder forwards anthropicAuth", async () => {
  for (const path of requestBuilders) {
    assert.match(await source(path), /anthropicAuth/, `${path} must forward anthropicAuth`);
  }
});

test("settings normalize and send anthropicAuth for the profile", async () => {
  const view = await source("../src/views/SettingsWorkspaceView.vue");
  assert.match(view, /anthropicAuth: AiAnthropicAuth;/);
  assert.match(view, /anthropicAuth: normalizeAnthropicAuth\(profile\.anthropicAuth\)/);
  assert.match(view, /v-if="activeProfile\.apiFormat === 'anthropic'"[\s\S]{0,400}v-model="activeProfile\.anthropicAuth"/);
  // Model listing and connection tests must use the same credential header as chat requests.
  const fetchModels = view.match(/await fetchAiModels\(\{[\s\S]*?\}\)/)?.[0] ?? "";
  const testConnection = view.match(/await testAiConnection\(\{[\s\S]*?\}\)/)?.[0] ?? "";
  assert.match(fetchModels, /anthropicAuth: profile\.anthropicAuth/);
  assert.match(testConnection, /anthropicAuth: profile\.anthropicAuth/);
});

test("anthropic auth copy exists in both locales", async () => {
  const zh = await source("../src/i18n/locales/zh.ts");
  const en = await source("../src/i18n/locales/en.ts");
  assert.match(zh, /"settings\.ai\.anthropicAuth": "认证方式"/);
  assert.match(zh, /"settings\.ai\.anthropicAuthHint": "Claude Code 使用 ANTHROPIC_AUTH_TOKEN 时选 Bearer"/);
  assert.match(en, /"settings\.ai\.anthropicAuth": "Authentication"/);
  assert.match(en, /"settings\.ai\.anthropicAuthHint": "Choose Bearer when Claude Code uses ANTHROPIC_AUTH_TOKEN"/);
});
