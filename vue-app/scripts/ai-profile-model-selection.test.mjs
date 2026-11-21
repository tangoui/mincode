import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

function readSource(path) {
  return readFile(new URL(`../${path}`, import.meta.url), "utf8");
}

test("the assistant model picker includes every configured AI profile", async () => {
  const drawer = await readSource("src/components/ai/AiAssistantDrawer.vue");
  const composer = await readSource("src/components/ai/AiPromptComposer.vue");

  assert.match(
    drawer,
    /stored\.aiProfiles \?\? \[\][\s\S]*?profileModelOptions/,
  );
  assert.match(drawer, /modelOptionValue\(profileId, model\)/);
  assert.match(
    drawer,
    /stored\.aiProfiles\?\.find\(\(item\) => item\.id === selectedOption\.profileId\)/,
  );
  assert.match(composer, /groupedModels/);
  assert.match(composer, /model\.profileLabel/);
});
