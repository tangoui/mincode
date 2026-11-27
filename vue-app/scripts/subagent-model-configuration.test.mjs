import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import { buildSubagentModelAttemptOrder } from "../src/domain/ai/model-selection.ts";

function readSource(path) {
  return readFile(new URL(`../${path}`, import.meta.url), "utf8");
}

test("subagents inherit the parent model when no override is configured", () => {
  assert.deepEqual(buildSubagentModelAttemptOrder({
    inheritedModel: "main-model",
    configuredModels: ["main-model", "subagent-model"],
    fallbackEnabled: false,
  }), ["main-model"]);
});

test("subagents use their configured model and keep valid failovers", () => {
  assert.deepEqual(buildSubagentModelAttemptOrder({
    inheritedModel: "main-model",
    subagentModel: "subagent-model",
    configuredModels: ["main-model", "subagent-model", "backup-model"],
    fallbackEnabled: true,
    fallbackModels: ["backup-model", "subagent-model", "unknown-model"],
  }), ["subagent-model", "backup-model"]);
});

test("invalid stale overrides safely fall back to the parent model", () => {
  assert.deepEqual(buildSubagentModelAttemptOrder({
    inheritedModel: "main-model",
    subagentModel: "removed-model",
    configuredModels: ["main-model"],
    fallbackEnabled: false,
  }), ["main-model"]);
});

test("settings persist a profile-level subagent model and runtime applies it", async () => {
  const settings = await readSource("src/views/SettingsWorkspaceView.vue");
  const drawer = await readSource("src/components/ai/AiAssistantDrawer.vue");

  assert.match(await readSource("src/services/settings-model.ts"), /subagentModel: string/);
  assert.match(settings, /v-model="activeProfile\.subagentModel"/);
  assert.match(settings, /settings\.ai\.subagentModelInherit/);
  assert.match(drawer, /subagentModel: request\.profile\.subagentModel/);
  assert.match(drawer, /model: subagentModels\[0\] \?\? request\.config\.model/);
  assert.match(drawer, /models: subagentModels/);
});
