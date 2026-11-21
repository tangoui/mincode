import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const [tabBar, workspaceManagement, workbench, scopedPersistence, deletionService, localProject, sshView, styles, zh, en] =
  await Promise.all([
    readFile(new URL("../src/components/layout/AppTabBar.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/composables/use-workspace-management.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/stores/workbench.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/services/workspace-scoped-persistence.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/services/workspace-state-deletion.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/stores/local-project.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/views/SshWorkspaceView.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/styles/shared/workspace-menus.css", import.meta.url), "utf8"),
    readFile(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/i18n/locales/en.ts", import.meta.url), "utf8"),
  ]);

test("recently deleted workspaces expose a separately confirmed permanent delete action", () => {
  assert.match(
    tabBar,
    /@click="permanentlyDeleteWorkspace\(\$event, record\.workspace\.id\)"/,
  );
  assert.match(workspaceManagement, /t\("tab\.workspacePermanentDeleteConfirm"/);
  assert.match(workspaceManagement, /confirmButtonText: t\("tab\.permanentlyDelete"\)/);
  assert.match(workspaceManagement, /type: "error"/);
  assert.match(tabBar, /class="app-workspace-menu__action is-danger"/);
  assert.match(styles, /\.app-workspace-menu__action\.is-danger:hover/);
});

test("permanent deletion removes recoverable metadata only after scoped cleanup", () => {
  assert.match(workbench, /function permanentlyDeleteWorkspace\(id: string\)/);
  assert.match(workspaceManagement, /await deleteWorkspaceScopePersistence\(scopeId\)/);
  assert.match(
    workspaceManagement,
    /await deleteWorkspaceScopePersistence\(scopeId\)[\s\S]*?workbench\.permanentlyDeleteWorkspace\(id\)/,
  );
  assert.match(workspaceManagement, /const terminalLayoutIds = record\.tabs\.map/);
  assert.match(workspaceManagement, /terminalWorkspace\.discardTabs\(terminalLayoutIds\)/);
  assert.match(workspaceManagement, /!workspaceStatePreferences\.isolationEnabled/);
});

test("permanent deletion clears scoped runtime state but preserves global durable resources", () => {
  for (const key of [
    "app_browser_state_v1",
    "app_http_state_v1",
    "app_db_query_tabs_v1",
    "app_sessions_live_terminals_v1",
  ]) {
    assert.match(deletionService, new RegExp(`"${key}"`));
  }
  assert.match(deletionService, /deleteAiScopeIds: \[normalizedScopeId\]/);
  // Browser-local mode keeps AI history in IndexedDB, out of the native batch's reach.
  assert.match(deletionService, /await deleteBrowserAiScopeHistory\(normalizedScopeId\)/);
  assert.match(deletionService, /LOCAL_PROJECT_WORKSPACES_STORAGE_KEY/);
  assert.match(deletionService, /SSH_SESSIONS_STORAGE_KEY/);
  assert.doesNotMatch(deletionService, /app_settings_v2/);
  assert.doesNotMatch(deletionService, /app_ssh_connections_v1/);
  assert.doesNotMatch(deletionService, /app_http_resources_v1/);
});

test("deleted scopes cannot be recreated by delayed cached-view writes", () => {
  assert.match(scopedPersistence, /isWorkspaceScopePermanentlyDeleted\(scopeId\)/);
  assert.match(
    scopedPersistence,
    /function save\(scopeId: string, value: T\)[\s\S]*?return Promise\.resolve\(\)/,
  );
  assert.match(localProject, /filter\(\(\[workspaceId\]\) => !isWorkspaceScopePermanentlyDeleted\(workspaceId\)\)/);
  assert.match(sshView, /filter\(\(session\) => !isWorkspaceScopePermanentlyDeleted\(session\.workspaceScopeId\)\)/);
});

test("permanent delete copy is localized and states the irreversible boundary", () => {
  for (const locale of [zh, en]) {
    assert.match(locale, /"tab\.permanentlyDeleteWorkspace"/);
    assert.match(locale, /"tab\.workspacePermanentDeleteConfirm"/);
    assert.match(locale, /"tab\.workspacePermanentlyDeleted"/);
  }
  assert.match(zh, /无法恢复/);
  assert.match(en, /cannot be restored/);
});
