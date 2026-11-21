import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const appSource = await readFile(new URL("../src/App.vue", import.meta.url), "utf8");
const databaseSource = await readFile(
  new URL("../src/views/DatabaseWorkspaceView.vue", import.meta.url),
  "utf8",
);
const databaseAiSource = await readFile(
  new URL("../src/composables/database/useAiDatabaseActions.ts", import.meta.url),
  "utf8",
);
const gitSource = await readFile(
  new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url),
  "utf8",
);
const httpSource = await readFile(
  new URL("../src/views/HttpWorkspaceView.vue", import.meta.url),
  "utf8",
);
const gitPanelSource = await readFile(
  new URL("../src/components/projects/ProjectGitPanel.vue", import.meta.url),
  "utf8",
);

test("keeps high-frequency workspace tabs in the bounded LRU cache while their tab is open", async () => {
  // The include list follows open tabs (domain/workspace/view-retention.ts): a view
  // with an open tab is cached, closing its last tab releases it.
  const { cachedWorkspaceViewNames } = await import("../src/domain/workspace/view-retention.ts");
  assert.match(appSource, /<KeepAlive\s+:include="cachedWorkspaceViews"/);
  const cachedViews = new Set(cachedWorkspaceViewNames(["database", "projects", "http", "plugin-view"].map((view) => ({ view }))));
  for (const view of ["DatabaseWorkspaceView", "ProjectWorkspaceView", "HttpWorkspaceView"]) {
    assert.ok(cachedViews.has(view), `${view} should be cached`);
  }
  // KeepAlive detaches deactivated DOM, which unloads iframes; plugin hosts stay mounted instead.
  assert.ok(!cachedViews.has("PluginWorkspaceView"), "plugin hosts must not be detached by KeepAlive");
  assert.match(appSource, /:max="workspaceCacheLimit \|\| undefined"/);
  assert.doesNotMatch(appSource, /Infinity/);
});

test("cached views expose stable component names", () => {
  assert.match(databaseSource, /defineOptions\(\{ name: "DatabaseWorkspaceView" \}\)/);
  assert.match(gitSource, /defineOptions\(\{ name: "ProjectWorkspaceView" \}\)/);
  assert.match(httpSource, /defineOptions\(\{ name: "HttpWorkspaceView" \}\)/);
});

test("AI handlers follow cached view activation", () => {
  assert.match(databaseAiSource, /onActivated\(registerAiDatabaseWorkspaceActions\)/);
  assert.match(databaseAiSource, /onDeactivated\(\(\) => \{/);
  assert.match(databaseSource, /useAiDatabaseActions\(/);
  assert.match(httpSource, /onActivated\(registerAiHttpWorkspaceActions\)/);
  assert.match(httpSource, /onDeactivated\(\(\) => \{/);
});

test("Git polling pauses while its cached workspace is hidden", () => {
  assert.match(gitPanelSource, /onActivated\(startGitAutoRefresh\)/);
  assert.match(gitPanelSource, /onDeactivated\(\(\) => \{[\s\S]*?stopGitAutoRefresh\(\)/);
  assert.match(gitPanelSource, /onUnmounted\(\(\) => \{[\s\S]*?stopGitAutoRefresh\(\)/);
});

const pluginWorkspaceSource = await readFile(
  new URL("../src/views/PluginWorkspaceView.vue", import.meta.url),
  "utf8",
);
const pluginFrameSource = await readFile(
  new URL("../src/components/plugins/PluginViewFrame.vue", import.meta.url),
  "utf8",
);
const pluginWorkspaceCss = await readFile(
  new URL("../src/styles/workspaces/plugins/workspace.css", import.meta.url),
  "utf8",
);

test("plugin tabs retain independent iframe form state until the tab closes", () => {
  assert.match(pluginWorkspaceSource, /defineOptions\(\{ name: "PluginWorkspaceView" \}\)/);
  // Slots are visited live tabs plus closed tabs parked in the warm pool, in a
  // stable append-only DOM order (moving an iframe node reloads its page).
  assert.match(pluginWorkspaceSource, /v-for="slot in frameSlots"/);
  assert.match(pluginWorkspaceSource, /return slotOrder\.value\.flatMap\(/);
  assert.match(pluginWorkspaceSource, /class="plugin-view-workspace__pane"/);
  assert.match(pluginWorkspaceSource, /'is-active': slot.id === activePluginTabId/);
  assert.match(pluginWorkspaceSource, /:inert="slot.id !== activePluginTabId"/);
  assert.match(pluginWorkspaceSource, /:key="slot.key"/);
  assert.doesNotMatch(pluginWorkspaceSource, /v-show=/);
  assert.doesNotMatch(pluginWorkspaceSource, /useRoute\(/);
  assert.doesNotMatch(pluginWorkspaceSource, /localStorage|sessionStorage/);
});

test("plugin hosts stay in the document while other workspace tabs are shown", () => {
  // One host per workspace with plugin tabs, outside KeepAlive, hidden instead of detached.
  assert.match(appSource, /v-if="activeRoute\.name !== 'plugin-view'"/);
  assert.match(appSource, /<PluginWorkspaceView\s+v-for="workspaceId in pluginWorkspaceIds"/);
  assert.match(appSource, /:visible="pluginLayerVisible && workspaceId === workbench\.activeWorkspaceId"/);
  assert.match(appSource, /tab\.view === "plugin-view" && tab\.pluginView\?\.pluginId && tab\.pluginView\.viewId\) ids\.add\(tab\.workspaceId\)/);
  assert.match(pluginWorkspaceSource, /const workspaceId = props\.workspaceId;/);
  assert.match(pluginWorkspaceSource, /:class="\{ 'is-hidden': !visible \}"/);
  assert.match(pluginWorkspaceSource, /:inert="!visible"/);
  const css = pluginWorkspaceCss.match(/\.plugin-view-workspace\.is-hidden,[\s\S]*?\{([^}]*)\}/);
  assert.ok(css, "hidden plugin host rule not found");
  assert.match(css[1], /visibility: hidden/);
  assert.doesNotMatch(css[1], /display:\s*none/);
  // The ready iframe's own visibility: visible must not show through a hidden host or pane.
  assert.match(pluginWorkspaceCss, /\.plugin-view-workspace\.is-hidden \.plugin-view-frame__iframe\.is-ready,\s*\.plugin-view-workspace__pane:not\(\.is-active\) \.plugin-view-frame__iframe\.is-ready \{\s*visibility: hidden;/);
});


test("plugin tabs can pop out without running twice", async () => {
  const popupSource = await readFile(new URL("../src/services/popup-tab.ts", import.meta.url), "utf8");
  const excluded = popupSource.match(/const nonPopoutViews = new Set<WorkspaceView>\(\[([^\]]*)\]\)/);
  assert.ok(excluded, "popup exclusion list not found");
  assert.doesNotMatch(excluded[1], /"plugin-view"/);
  // The main window keeps a popped-out plugin tab listed but must not mount its iframe.
  assert.match(pluginWorkspaceSource, /const pluginTabs = computed\(\(\) => workbench\.tabs\.filter\(\(tab\) =>[\s\S]*?!workbench\.poppedOutTabLabels\[tab\.id\]/);
  assert.match(pluginWorkspaceSource, /const activePluginTabId = computed\([\s\S]*?!workbench\.poppedOutTabLabels\[tab\.id\]/);
  assert.match(pluginWorkspaceSource, /watch\(pluginTabs, \(tabs\) => \{[\s\S]*?visitedTabIds\.value = visitedTabIds\.value\.filter/);
});

test("plugin iframe verifies repeated loads and securely renews unresponsive documents", () => {
  assert.match(pluginFrameSource, /function handleFrameLoad\(\) \{[\s\S]*?frameLoadCount \+= 1;[\s\S]*?if \(frameLoadCount <= 1\) return;[\s\S]*?probePluginDocument\(\)/);
  assert.match(pluginFrameSource, /messagePort\.postMessage\(\{ type: "mincode\.host\.probe", id: probeId \}\)/);
  assert.match(pluginFrameSource, /data\.type === "mincode\.plugin\.probe"/);
  assert.match(pluginFrameSource, /if \(frameProbeId === probeId\) renewPluginDocument\(\)/);
  assert.match(pluginFrameSource, /function renewPluginDocument\(\) \{[\s\S]*?void startSession\(\)/);
  assert.doesNotMatch(pluginFrameSource, /frameLoadCount > 1[\s\S]{0,120}failClosed/);
});
