import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

// The release notes used a native overflow scroller inside the settings dialog's
// Element Plus scrollbar: a thick system scrollbar sat next to the thin outer one.
test("the update changelog scrolls inside an Element Plus scrollbar", async () => {
  const view = await readFile(new URL("../src/views/SettingsWorkspaceView.vue", import.meta.url), "utf8");
  const css = await readFile(new URL("../src/styles/workspaces/settings/dialog.css", import.meta.url), "utf8");
  assert.match(view, /<div class="settings-update-card__notes">\s*<el-scrollbar max-height="360px">\s*<div class="settings-update-card__notes-body">/);
  const notes = css.match(/\.settings-update-card__notes \{([^}]*)\}/)?.[1] ?? "";
  assert.doesNotMatch(notes, /overflow|max-height|padding/, "the Element Plus scrollbar owns scrolling, height and the content inset");
  assert.match(css, /\.settings-update-card__notes-body \{[^}]*padding: 12px 16px;/);
});
