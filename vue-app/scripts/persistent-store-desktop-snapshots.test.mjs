import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const read = (path) => readFile(new URL(`../src/${path}`, import.meta.url), "utf8");

// Every key read synchronously from localStorage before native hydration must keep a
// desktop browser snapshot, otherwise first paint regresses to defaults.
const SYNC_READERS = [
  ["services/settings-persistence.ts", "app_settings_v2"],
  ["services/ui-theme.ts", "app_settings_v2"],
  ["stores/workbench.ts", "app_workspaces_v1"],
  ["stores/workbench.ts", "app_active_workspace_v1"],
  ["stores/terminal-workspace.ts", "app_terminal_workspace_v1"],
  ["stores/ssh-connections.ts", "app_ssh_connections_v1"],
  ["components/layout/AppSidebar.vue", "app_sidebar_order_v2"],
  ["stores/ai-assistant-shell.ts", "app_ai_drawer_width"],
  ["stores/ai-assistant-shell.ts", "app_ai_layout_mode"],
  ["stores/ai-assistant-shell.ts", "app_ai_assistant_placement"],
  ["stores/ai-assistant.ts", "app_ai_conversation_sidebar_width"],
  ["views/HttpWorkspaceView.vue", "app_http_request_height"],
  ["views/DatabaseWorkspaceView.vue", "app_db_scripts"],
  ["views/DatabaseWorkspaceView.vue", "app_db_query_history"],
];

test("synchronously read keys are registered for desktop browser snapshots", async () => {
  const store = await read("services/persistent-store.ts");
  const registry = store.match(/DESKTOP_BROWSER_SNAPSHOT_KEYS = new Set<string>\(\[([\s\S]*?)\]\);/)?.[1];
  assert.ok(registry, "registry literal not found");
  for (const [file, key] of SYNC_READERS) {
    assert.match(await read(file), new RegExp(`"${key}"`), `${file} no longer uses ${key}`);
    assert.match(registry, new RegExp(`"${key}"`), `${key} (read by ${file}) must keep a desktop snapshot`);
  }
});

test("resizable panes register their synchronously read keys", async () => {
  const source = await read("composables/use-resizable-pane.ts");
  assert.match(source, /registerDesktopBrowserSnapshotKeys\(options\.sizeKey, options\.collapsedKey\)/);
  assert.ok(
    source.indexOf("registerDesktopBrowserSnapshotKeys(options") < source.indexOf("readStoredNumber(options.sizeKey"),
  );
});

test("desktop saves default to native-only persistence", async () => {
  const store = await read("services/persistent-store.ts");
  assert.match(store, /writeBrowserSnapshotInDesktop = writesDesktopBrowserSnapshot\(key\)/);
  assert.match(store, /options\.writeBrowserSnapshotInDesktop \?\? writesDesktopBrowserSnapshot\(key\)/);
  const secure = await read("services/secure-persistent-store.ts");
  assert.match(secure, /!desktopRuntime \|\| writesDesktopBrowserSnapshot\(key\)/);
  assert.doesNotMatch(secure, /localStorage\.setItem/);
});
