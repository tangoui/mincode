import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

function readSource(path) {
  return readFile(new URL(`../${path}`, import.meta.url), "utf8");
}

test("plugin management owns the capability catalog tab", async () => {
  const [manager, catalog, router] = await Promise.all([
    readSource("src/views/PluginManagerWorkspaceView.vue"),
    readSource("src/components/plugins/PluginCapabilityCatalog.vue"),
    readSource("src/router/index.ts"),
  ]);

  assert.match(manager, /<el-tabs[\s\S]*plugins\.tab\.plugins/);
  assert.match(manager, /<el-tab-pane[\s\S]*plugins\.tab\.capabilities/);
  assert.match(manager, /<PluginCapabilityCatalog\s*\/>/);
  assert.match(catalog, /registerNativeAiCapabilities/);
  assert.match(catalog, /groupedCapabilities/);
  assert.match(
    router,
    /path:\s*"\/workflows",[\s\S]*?redirect:\s*\{\s*name:\s*"plugins",\s*query:\s*\{\s*tab:\s*"capabilities"\s*\}\s*\}/,
  );
});

test("workflow automation has no workspace entry or AI capability bridge", async () => {
  const [
    sidebar,
    views,
    workspaceTypes,
    nativeTools,
    terminal,
    http,
    database,
  ] = await Promise.all([
    readSource("src/services/sidebar-items.ts"),
    readSource("src/router/workspace-views.ts"),
    readSource("src/types/workspace.ts"),
    readSource("src/services/ai-native-tools.ts"),
    readSource("src/components/terminal/LocalTerminal.vue"),
    readSource("src/views/HttpWorkspaceView.vue"),
    readSource("src/views/DatabaseWorkspaceView.vue"),
  ]);

  assert.doesNotMatch(sidebar, /id: "workflows"/);
  assert.doesNotMatch(views, /WorkflowWorkspaceView|workflows:/);
  assert.doesNotMatch(workspaceTypes, /"workflows"/);
  assert.doesNotMatch(
    nativeTools,
    /workflowCapabilities|workflowAction|listWorkflowRunbooks/,
  );
  for (const source of [terminal, http, database]) {
    assert.doesNotMatch(
      source,
      /notifyWorkflowSuggestion|workflow-automation-store/,
    );
  }
});
