import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const root = new URL("../", import.meta.url);

test("installation preview never grants trust", async () => {
  const manager = await readFile(new URL("src/views/PluginManagerWorkspaceView.vue", root), "utf8");
  assert.doesNotMatch(manager, /trustDangerousActions/);
  assert.doesNotMatch(manager, /setPluginDangerousActionTrust/);
  assert.doesNotMatch(manager, /dangerousActionTrustSaveFailed/);
  // Only an information line saying trust can be turned on later in the plugin manager.
  assert.match(manager, /t\('plugins\.fullTrust\.previewHint'\)/);
  assert.match(manager, /<p v-if="previewHasNonReadActions" class="app-plugin-preview__trust-hint">/);
  for (const locale of ["zh", "en"]) {
    const messages = await readFile(new URL(`src/i18n/locales/${locale}.ts`, root), "utf8");
    assert.doesNotMatch(messages, /dangerousActionTrustSaveFailed|trustDangerousActions/, locale);
  }
});

test("trusted plugins skip action confirmation through the host", async () => {
  const frame = await readFile(new URL("src/components/plugins/PluginViewFrame.vue", root), "utf8");
  assert.match(frame, /authorizeWithoutPrompt\(pluginId, "viewAction"/);
  assert.doesNotMatch(frame, /getPluginDangerousActionTrust/);
  assert.match(frame, /confirmAction\(action, session\.pluginId\)/);
});
