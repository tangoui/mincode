import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const [sidebarItems, sidebar, projectView, zh, en] = await Promise.all([
  readFile(new URL("../src/services/sidebar-items.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/components/layout/AppSidebar.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/i18n/locales/en.ts", import.meta.url), "utf8"),
]);

test("source control is available from the activity bar without adding a duplicate workspace view", () => {
  assert.match(sidebarItems, /id: "source-control"[\s\S]*?view: "projects"[\s\S]*?query: \{ panel: "git" \}/);
  assert.match(sidebar, /function openItem\(item:/);
  assert.match(sidebar, /const view = sidebarItemView\(item\);[\s\S]*?workbench\.openView\(view\)/);
  assert.match(sidebar, /router\.push\(sidebarItemRoute\(item\)\)/);
  assert.match(sidebar, /isItemActive\(item\)/);
  assert.match(sidebarItems, /id: "source-control"[\s\S]*?placement: "main"[\s\S]*?view: "projects"/);
});

test("project source tab and activity-bar shortcut share the route-backed Git panel", () => {
  assert.match(projectView, /useRoute\(\)/);
  assert.match(projectView, /route\.query\.panel === "git"/);
  assert.match(projectView, /if \(sideMode\.value !== "git"\) void openSourceControl\(\)/);
  assert.match(projectView, /if \(PANEL_SIDE_MODES\.has\(sideMode\.value\)\) sideMode\.value = "explorer"/);
  assert.match(projectView, /watch\(sideMode,[\s\S]*?setProjectPanelRoute\(mode === "git" \? "git" : "resources"\)/);
  assert.match(projectView, /watch\([\s\S]*?route\.query\.panel[\s\S]*?openSourceControl/);
});

test("source-control navigation is localized", () => {
  assert.match(zh, /"nav\.sourceControl": "版本"/);
  assert.match(en, /"nav\.sourceControl": "Source Control"/);
});
