import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { PLUGIN_CAPABILITY_TONES, pluginCapabilityTone } from "../src/domain/plugins/capability-tone.ts";

test("each capability keeps one tone everywhere", () => {
  assert.equal(pluginCapabilityTone("network"), pluginCapabilityTone("network"));
  for (const name of ["commands", "network", "notifications", "secrets", "storage", "ui", "", "shell"]) {
    assert.ok(PLUGIN_CAPABILITY_TONES.includes(pluginCapabilityTone(name)), name);
  }
});

test("common capabilities on one card get different tones", () => {
  const tones = ["commands", "network", "notifications", "secrets", "storage", "ui"].map(pluginCapabilityTone);
  assert.equal(new Set(tones).size, tones.length);
});

test("every tone has a themed style on the plugin cards", async () => {
  const css = await readFile(new URL("../src/styles/workspaces/plugins/workspace.css", import.meta.url), "utf8");
  for (const tone of PLUGIN_CAPABILITY_TONES) {
    assert.match(css, new RegExp(`\\.plugin-manager-capabilities \\.el-tag\\[data-tone="${tone}"\\][^}]*--el-tag-text-color: [^;]*var\\(--app-`));
  }
});
