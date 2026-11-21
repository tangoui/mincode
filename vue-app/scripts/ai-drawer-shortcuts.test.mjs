import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { adjacentConversationId } from "../src/domain/ai/drawer-shortcuts.ts";
import { KEYBINDING_COMMANDS } from "../src/domain/keybindings/catalog.ts";

// Esc / ⌘⇧O / ⌘⇧F / Alt+↑↓ key matching moved to the keybinding dispatcher
// (HOST_KEYBINDING_RULES); keybinding-persistence.test.mjs checks the keys.

test("adjacent conversation stops at the list edges", () => {
  const ids = ["a", "b", "c"];
  assert.equal(adjacentConversationId(ids, "b", -1), "a");
  assert.equal(adjacentConversationId(ids, "b", 1), "c");
  assert.equal(adjacentConversationId(ids, "a", -1), undefined);
  assert.equal(adjacentConversationId(ids, "c", 1), undefined);
  assert.equal(adjacentConversationId(ids, "hidden", 1), "a");
  assert.equal(adjacentConversationId([], "a", 1), undefined);
});

test("drawer registers the panel commands and settings documents them", async () => {
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  const settings = await readFile(new URL("../src/views/SettingsWorkspaceView.vue", import.meta.url), "utf8");
  assert.doesNotMatch(drawer, /handlePanelShortcut/);
  assert.match(drawer, /registerKeybindingContext\("ai\.running", \(\) => assistant\.loading\)/);
  assert.match(drawer, /registerKeybindingContext\("ai\.conversations\.global"/);
  assert.match(drawer, /registerKeybindingContext\("ai\.multiline\.field"/);
  assert.match(drawer, /registerKeybindingContext\("ai\.message\.selection"/);
  for (const id of ["ai:stop", "ai:new-conversation", "ai:search-conversations", "ai:previous-conversation", "ai:next-conversation", "ai:copy-message"]) {
    assert.match(drawer, new RegExp(`\\["${id}", `));
  }
  assert.match(drawer, /groupedConversations\.value\.flatMap/);
  assert.match(drawer, /for \(const dispose of keybindingDisposers\) dispose\(\)/);
  // Settings lists every catalog command (KeybindingSettingsPanel), including the AI panel ones.
  assert.match(settings, /defineAsyncComponent\(\(\) => import\("@\/components\/settings\/KeybindingSettingsPanel\.vue"\)\)/);
  const catalog = new Set(KEYBINDING_COMMANDS.map((command) => command.id));
  for (const id of ["ai:stop", "ai:new-conversation", "ai:search-conversations", "ai:previous-conversation", "ai:next-conversation", "ai:copy-message"]) {
    assert.ok(catalog.has(id), id);
  }
  for (const locale of ["zh", "en"]) {
    const source = await readFile(new URL(`../src/i18n/locales/${locale}.ts`, import.meta.url), "utf8");
    for (const name of ["group.ai", "group.aiDesc", "aiStop", "aiNewConversation", "aiSwitchConversation", "aiSearchConversations", "scope.ai"]) {
      assert.match(source, new RegExp(`"settings\\.shortcuts\\.${name.replace(".", "\\.")}"`));
    }
  }
});
