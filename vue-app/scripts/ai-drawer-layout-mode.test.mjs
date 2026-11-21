import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";

const [appSource, drawerSource, storeSource, shellSource, foundationStyles, drawerStyles] = await Promise.all([
  readFile(new URL("../src/App.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/stores/ai-assistant.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/stores/ai-assistant-shell.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/styles/foundation/foundation.css", import.meta.url), "utf8"),
  readFile(new URL("../src/styles/workspaces/ai/drawer.css", import.meta.url), "utf8"),
]);

async function readVueSources(directory) {
  const sources = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const url = new URL(entry.name + (entry.isDirectory() ? "/" : ""), directory);
    if (entry.isDirectory()) {
      sources.push(...await readVueSources(url));
    } else if (entry.name.endsWith(".vue")) {
      sources.push({ path: url.pathname, source: await readFile(url, "utf8") });
    }
  }
  return sources;
}

test("AI conversation header switches between overlay and docked layouts", () => {
  assert.match(drawerSource, /class="ai-assistant-header__layout-toggle"/);
  assert.match(drawerSource, /assistant\.setLayoutMode\(assistant\.layoutMode === "docked" \? "overlay" : "docked"\)/);
  // The AI tab surface (route "ai") renders the shell full-size too, so the toggle is
  // hidden whenever the shell is fullscreen, which still covers assistant.fullscreen.
  assert.match(
    drawerSource,
    /const shellFullscreen = computed\(\(\) => assistant\.fullscreen \|\| tabSurfaceActive\.value\)/,
  );
  assert.match(
    drawerSource,
    /v-if="!isPopupWindow\(\) && !shellFullscreen"[\s\S]{0,200}?class="ai-assistant-header__layout-toggle"/,
  );
});

test("AI layout preference is persisted independently of conversations", () => {
  assert.match(shellSource, /export type AiAssistantLayoutMode = "overlay" \| "docked"/);
  assert.match(shellSource, /export const AI_ASSISTANT_LAYOUT_MODE_KEY = "app_ai_layout_mode"/);
  assert.match(storeSource, /savePersistedValue\(AI_ASSISTANT_LAYOUT_MODE_KEY, value\)/);
});

test("docked layout reserves App Shell space and falls back on small windows", () => {
  assert.match(appSource, /'is-ai-docked': assistant\.open && assistant\.layoutMode === 'docked' && !assistant\.fullscreen/);
  assert.match(appSource, /'--ai-assistant-width': `\$\{assistant\.width\}px`/);
  assert.match(
    foundationStyles,
    /\.app-workspace-shell\.is-ai-docked \.app-workspace-content\s*\{[\s\S]*?margin-right:\s*var\(--ai-assistant-width, 420px\);/,
  );
  assert.match(
    foundationStyles,
    /@media \(max-width: 900px\)[\s\S]*?\.app-workspace-shell\.is-ai-docked \.app-workspace-content\s*\{[\s\S]*?margin-right:\s*0;/,
  );
});

test("dialogs and drawers stay above the AI conversation in every layout", async () => {
  assert.doesNotMatch(appSource, /app-ai-docked/);
  assert.doesNotMatch(foundationStyles, /html\.app-ai-docked/);
  assert.match(
    drawerStyles,
    /\.ai-assistant-drawer\s*\{[\s\S]*?z-index:\s*80;/,
  );

  const vueSources = await readVueSources(new URL("../src/", import.meta.url));
  const modalTags = vueSources.flatMap(({ path, source }) =>
    [...source.matchAll(/<el-(?:dialog|drawer)\b[^>]*>/g)].map((match) => ({
      path,
      tag: match[0],
    })),
  );
  assert.ok(modalTags.length > 0, "expected Element Plus dialogs or drawers");
  for (const { path, tag } of modalTags) {
    assert.match(tag, /\bappend-to-body\b/, `${path} must teleport ${tag.split(/\s/, 1)[0]} to body`);
  }
});
