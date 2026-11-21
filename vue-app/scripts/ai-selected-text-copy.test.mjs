import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { URL } from "node:url";

const drawerSource = await readFile(
  new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url),
  "utf8",
);

// ⌘/Ctrl+C now reaches the drawer as the keymap command ai:copy-message
// (HOST_KEYBINDING_RULES: `text.selection && ai.message.selection`). The
// dispatcher never takes ⌘C from inputs/contenteditable/Monaco; see
// keybinding-persistence.test.mjs for the key-level checks.
test("AI 消息选中文本可通过系统复制快捷键写入剪贴板", () => {
  assert.match(drawerSource, /\["ai:copy-message", copySelectedMessage\]/);
  assert.match(drawerSource, /selection\.rangeCount === 0 \|\| selection\.isCollapsed/);
  assert.match(drawerSource, /closest\("\.ai-message-card__content"\)/);
  assert.match(drawerSource, /anchorBody !== focusBody/);
  assert.match(drawerSource, /await copyText\(selectedText\)/);
});

test("选区复制不接管编辑控件，并正确管理全局监听", () => {
  assert.match(drawerSource, /registerKeybindingContext\("ai\.message\.selection", \(\) => Boolean\(selectedMessageText\(\)\)\)/);
  assert.doesNotMatch(drawerSource, /window\.addEventListener\("keydown"/);
  assert.match(drawerSource, /for \(const dispose of keybindingDisposers\) dispose\(\)/);
});
