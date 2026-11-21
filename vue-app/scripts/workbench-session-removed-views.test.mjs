import assert from "node:assert/strict";
import test from "node:test";
import {
  DEFAULT_WORKSPACE_ID,
  normalizeWorkbenchSession,
} from "../src/domain/workspace/workbench-session.ts";

const workspaceId = DEFAULT_WORKSPACE_ID;
const tabs = ["projects", "terminal", "browser"].map((view) => ({
  id: `${workspaceId}:${view}`,
  workspaceId,
  view,
}));

for (const view of ["projects", "browser"]) {
  test(`restoring an active ${view} tab falls back to the dashboard`, () => {
    const activeTabId = `${workspaceId}:${view}`;
    const session = normalizeWorkbenchSession(JSON.parse(JSON.stringify({
      version: 4,
      activeWorkspaceId: workspaceId,
      tabs,
      activeTabId,
      lastActiveTabByWorkspace: { [workspaceId]: activeTabId },
    })));

    assert.ok(session);
    assert.deepEqual(session.tabs.map((tab) => tab.view), ["terminal"]);
    // An empty active tab ID tells the workbench to render its dashboard.
    assert.equal(session.activeTabId, "");
    assert.deepEqual(session.lastActiveTabByWorkspace, {});
  });
}
