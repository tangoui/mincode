import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { pluginCapabilityTone } from "../src/domain/plugins/capability-tone.ts";

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");

test("ui.wasm keeps a fixed warning tone, distinct from ui", () => {
  assert.equal(pluginCapabilityTone("ui.wasm"), "amber");
  assert.notEqual(pluginCapabilityTone("ui.wasm"), pluginCapabilityTone("ui"));
});

test("enabling and install/upgrade previews disclose ui.wasm", async () => {
  const manager = await readSource("../src/views/PluginManagerWorkspaceView.vue");
  assert.match(manager, /plugin\.capabilities\.includes\("ui\.wasm"\) \? t\("plugins\.capabilityUiWasm"\) : ""/, "enable confirmation");
  assert.match(manager, /<p v-if="preview\.manifest\.capabilities\?\.includes\('ui\.wasm'\)" class="is-warning">[\s\S]{0,120}t\('plugins\.capabilityUiWasm'\)/, "install preview warning");
  const zh = await readSource("../src/i18n/locales/zh.ts");
  const en = await readSource("../src/i18n/locales/en.ts");
  assert.match(zh, /"plugins\.capabilityUiWasm":\s*"WebAssembly（ui\.wasm）：[^"]+"/);
  assert.match(en, /"plugins\.capabilityUiWasm":\s*"WebAssembly \(ui\.wasm\): [^"]+"/);
});
