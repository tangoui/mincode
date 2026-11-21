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
  const cachedViews = new Set(cachedWorkspaceViewNames(["database", "projects", "http"].map((view) => ({ view }))));
  for (const view of ["DatabaseWorkspaceView", "ProjectWorkspaceView", "HttpWorkspaceView"]) {
    assert.ok(cachedViews.has(view), `${view} should be cached`);
  }
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
