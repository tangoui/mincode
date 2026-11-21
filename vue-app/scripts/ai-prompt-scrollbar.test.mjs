import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const [composer, drawerStyles] = await Promise.all([
  readFile(new URL("../src/components/ai/AiPromptComposer.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/styles/workspaces/ai/drawer.css", import.meta.url), "utf8"),
]);

test("AI prompt overflow is handled by Element Plus scrollbar", () => {
  assert.match(
    composer,
    /<el-scrollbar ref="promptScrollbar" class="ai-assistant-composer__prompt-scrollbar" max-height="158px" always>/,
  );
  assert.match(composer, /:autosize="\{ minRows: 2 \}"/);
  assert.doesNotMatch(composer, /maxRows:\s*7/);
  assert.match(composer, /promptScrollbar\.value\?\.setScrollTop/);
  assert.match(
    drawerStyles,
    /\.ai-assistant-composer__input \.el-textarea__inner\s*\{[\s\S]*?overflow: hidden !important;/,
  );
});
