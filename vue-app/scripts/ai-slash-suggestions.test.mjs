import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const drawerSource = await readFile(
  new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url),
  "utf8",
);

test("keeps slash suggestions available while a conversation is running", () => {
  const visibilityBlock = drawerSource.match(
    /const toolSuggestionsVisible = computed\([\s\S]*?\n\);/,
  )?.[0] ?? "";

  assert.match(visibilityBlock, /parseAiPromptSuggestionQuery\(input\.value\) !== null/);
  assert.doesNotMatch(visibilityBlock, /assistant\.loading/);
});
