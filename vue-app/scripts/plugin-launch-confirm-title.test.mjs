import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { processLaunchTitle } from "../src/domain/plugins/title.ts";

// The host sends ResolvedProcessLaunch.titleLocalized as a per-language map
// (Go map[string]string). Treating it as a string made the launch confirmation
// throw while rendering, so the dialog never appeared and its promise never settled.
test("launch titles resolve the per-language map from the host", () => {
  const launch = { entryKey: "cua-driver", title: "Cua Driver", titleLocalized: { en: "Cua Driver", zh: "Cua 后台电脑操作" } };
  assert.equal(processLaunchTitle(launch, ["zh"]), "Cua 后台电脑操作");
  assert.equal(processLaunchTitle(launch, ["en"]), "Cua Driver");
  assert.equal(processLaunchTitle({ ...launch, titleLocalized: undefined }, ["zh"]), "Cua Driver");
  assert.equal(processLaunchTitle({ entryKey: "lint", title: "", titleLocalized: null }, ["zh"]), "lint");
  // Anything else from the bridge never throws.
  assert.equal(processLaunchTitle({ ...launch, titleLocalized: "旧格式" }, ["zh"]), "旧格式");
  assert.equal(processLaunchTitle({ ...launch, titleLocalized: { zh: 7 } }, ["zh"]), "Cua Driver");
});

test("the confirmation body and the launch type use the map", async () => {
  const body = await readFile(new URL("../src/components/plugins/PluginLaunchConfirmContent.vue", import.meta.url), "utf8");
  assert.match(body, /processLaunchTitle\(props\.launch, localeChain\.value\)/);
  assert.doesNotMatch(body, /visibleTerminalText\(props\.launch\.titleLocalized/);
  const service = await readFile(new URL("../src/services/plugin-launch-confirm.ts", import.meta.url), "utf8");
  assert.match(service, /titleLocalized\?: Record<string, string>;/);
});
