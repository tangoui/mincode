import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const drawerSource = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
const drawerStyles = await readFile(new URL("../src/styles/workspaces/ai/drawer.css", import.meta.url), "utf8");

test("AI drawer close button remains circular", () => {
  assert.match(
    drawerSource,
    /class="ai-assistant-header__close"[\s\S]*?\btext\b[\s\S]*?\bcircle\b[\s\S]*?:icon="Close"/,
  );
  assert.match(
    drawerStyles,
    /\.ai-assistant-header__close\.el-button\s*\{[\s\S]*?width:\s*28px;[\s\S]*?height:\s*28px;[\s\S]*?aspect-ratio:\s*1;[\s\S]*?border-radius:\s*50%;/,
  );
});

test("fullscreen AI drawer keeps a visible left boundary", () => {
  assert.match(
    drawerStyles,
    /\.ai-assistant-drawer\.is-fullscreen \.ai-assistant-drawer__panel\s*\{[\s\S]*?border-left:\s*1px solid var\(--app-border\);/,
  );
  assert.doesNotMatch(
    drawerStyles,
    /\.ai-assistant-drawer\.is-fullscreen \.ai-assistant-drawer__panel\s*\{[\s\S]*?border-left:\s*0;/,
  );
});

test("conversation sidebar keeps a left boundary against the message area", () => {
  assert.match(
    drawerStyles,
    /\.ai-conversation-sidebar\s*\{[\s\S]*?border-left:\s*1px solid var\(--app-border\);/,
  );
});

test("fullscreen AI drawer preserves the main activity rail", () => {
  assert.match(
    drawerSource,
    /'is-popup-window':\s*isPopupWindow\(\)/,
  );
  assert.match(
    drawerStyles,
    /\.ai-assistant-drawer\.is-fullscreen:not\(\.is-popup-window\)\s*\{[\s\S]*?left:\s*var\(--app-activity-rail-width,\s*56px\);/,
  );
});
