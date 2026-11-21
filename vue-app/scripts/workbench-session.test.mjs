import assert from "node:assert/strict";
import test from "node:test";
import {
  DEFAULT_WORKSPACE_ID,
  WORKBENCH_SESSION_VERSION,
  normalizeWorkbenchSession,
  prependDeletedWorkspace,
} from "../src/domain/workspace/workbench-session.ts";

function workspace(id) {
  return { id, name: id, createdAt: 1, updatedAt: 1 };
}

function tab(workspaceId, index = 1) {
  return {
    id: `${workspaceId}:http:${index}`,
    workspaceId,
    view: "http",
    titleKey: "ignored-legacy-title",
    closable: true,
  };
}

function deletedRecord(index) {
  const item = workspace(`workspace:deleted-${index}`);
  const itemTab = tab(item.id);
  return {
    workspace: item,
    tabs: [itemTab],
    lastActiveTabId: itemTab.id,
    deletedAt: index,
  };
}

test("migrates version 1 sessions to version 2 without changing workspace ids", () => {
  const restored = normalizeWorkbenchSession({
    version: 1,
    workspaces: [
      workspace(DEFAULT_WORKSPACE_ID),
      workspace("workspace:active"),
    ],
    activeWorkspaceId: "workspace:active",
    tabs: [tab("workspace:active")],
    activeTabId: "workspace:active:http:1",
    lastActiveTabByWorkspace: {
      "workspace:active": "workspace:active:http:1",
    },
  });

  assert.ok(restored);
  assert.equal(restored.version, WORKBENCH_SESSION_VERSION);
  assert.equal(restored.activeWorkspaceId, "workspace:active");
  assert.equal(restored.tabs[0].workspaceId, "workspace:active");
  assert.deepEqual(restored.deletedWorkspaces, []);
});

test("migrates legacy workflow tabs into plugin management", () => {
  const legacyTabId = `${DEFAULT_WORKSPACE_ID}:workflows`;
  const restored = normalizeWorkbenchSession({
    version: 3,
    workspaces: [workspace(DEFAULT_WORKSPACE_ID)],
    activeWorkspaceId: DEFAULT_WORKSPACE_ID,
    tabs: [
      {
        id: legacyTabId,
        workspaceId: DEFAULT_WORKSPACE_ID,
        view: "workflows",
        titleKey: "nav.workflows",
        closable: true,
      },
    ],
    activeTabId: legacyTabId,
    lastActiveTabByWorkspace: { [DEFAULT_WORKSPACE_ID]: legacyTabId },
    deletedWorkspaces: [],
  });

  assert.ok(restored);
  assert.equal(restored.tabs.length, 1);
  assert.equal(restored.tabs[0].view, "plugins");
  assert.equal(restored.tabs[0].id, `${DEFAULT_WORKSPACE_ID}:plugins`);
  assert.equal(restored.tabs[0].titleKey, "nav.plugins");
  assert.equal(restored.tabs[0].title, undefined);
  assert.equal(restored.activeTabId, `${DEFAULT_WORKSPACE_ID}:plugins`);
  assert.equal(
    restored.lastActiveTabByWorkspace[DEFAULT_WORKSPACE_ID],
    `${DEFAULT_WORKSPACE_ID}:plugins`,
  );
});

test("retains every recoverable workspace record instead of silently capping the recycle list", () => {
  const deletedWorkspaces = Array.from({ length: 25 }, (_, index) =>
    deletedRecord(index),
  );
  const restored = normalizeWorkbenchSession({
    version: 2,
    workspaces: [workspace(DEFAULT_WORKSPACE_ID)],
    activeWorkspaceId: DEFAULT_WORKSPACE_ID,
    tabs: [],
    activeTabId: "",
    lastActiveTabByWorkspace: {},
    deletedWorkspaces,
  });

  assert.ok(restored);
  assert.equal(restored.deletedWorkspaces.length, 25);
  assert.equal(
    restored.deletedWorkspaces[24].workspace.id,
    "workspace:deleted-24",
  );
  assert.equal(
    restored.deletedWorkspaces[24].tabs[0].workspaceId,
    restored.deletedWorkspaces[24].workspace.id,
  );
});

test("retains the archived workspace scope id across a session round trip", () => {
  const record = { ...deletedRecord(1), stateScopeId: "workspace:shared-anchor" };
  const restored = normalizeWorkbenchSession({
    version: WORKBENCH_SESSION_VERSION,
    workspaces: [workspace(DEFAULT_WORKSPACE_ID)],
    activeWorkspaceId: DEFAULT_WORKSPACE_ID,
    tabs: [],
    activeTabId: "",
    lastActiveTabByWorkspace: {},
    deletedWorkspaces: [record],
  });

  assert.ok(restored);
  assert.equal(restored.deletedWorkspaces[0].stateScopeId, "workspace:shared-anchor");
});

test("re-deleting a restored workspace moves one stable-id record to the front", () => {
  const records = [deletedRecord(1), deletedRecord(2), deletedRecord(3)];
  const updated = { ...deletedRecord(2), deletedAt: 99 };
  const next = prependDeletedWorkspace(records, updated);

  assert.equal(next.length, 3);
  assert.equal(next[0].workspace.id, "workspace:deleted-2");
  assert.equal(next[0].deletedAt, 99);
  assert.equal(
    next.filter((item) => item.workspace.id === "workspace:deleted-2").length,
    1,
  );
});

test("restores at most one instance of the same plugin view per workspace", () => {
  const pluginTab = (id, workspaceId) => ({
    id,
    workspaceId,
    view: "plugin-view",
    titleKey: "ignored-legacy-title",
    closable: true,
    pluginView: { pluginId: "example.visual", viewId: "dashboard" },
  });
  const restored = normalizeWorkbenchSession({
    version: WORKBENCH_SESSION_VERSION,
    workspaces: [workspace(DEFAULT_WORKSPACE_ID), workspace("workspace:other")],
    activeWorkspaceId: DEFAULT_WORKSPACE_ID,
    tabs: [
      pluginTab("legacy-random-id-one", DEFAULT_WORKSPACE_ID),
      pluginTab("legacy-random-id-two", DEFAULT_WORKSPACE_ID),
      pluginTab("other-workspace-id", "workspace:other"),
    ],
    activeTabId: "legacy-random-id-one",
    lastActiveTabByWorkspace: {},
    deletedWorkspaces: [],
  });

  assert.ok(restored);
  assert.equal(restored.tabs.length, 2);
  assert.equal(
    restored.tabs.filter((item) => item.workspaceId === DEFAULT_WORKSPACE_ID)
      .length,
    1,
  );
  assert.equal(
    restored.tabs.filter((item) => item.workspaceId === "workspace:other")
      .length,
    1,
  );
});

test("drops service result tabs from a restored session: their content lived in memory only", () => {
  const restored = normalizeWorkbenchSession({
    version: WORKBENCH_SESSION_VERSION,
    workspaces: [workspace(DEFAULT_WORKSPACE_ID)],
    activeWorkspaceId: DEFAULT_WORKSPACE_ID,
    tabs: [
      tab(DEFAULT_WORKSPACE_ID),
      {
        id: `${DEFAULT_WORKSPACE_ID}:service-result:r1`,
        workspaceId: DEFAULT_WORKSPACE_ID,
        view: "service-result",
        titleKey: "ignored",
        title: "Reverse text 12:00:00",
        closable: true,
        serviceResult: { resultId: "r1" },
      },
    ],
    activeTabId: `${DEFAULT_WORKSPACE_ID}:service-result:r1`,
    lastActiveTabByWorkspace: { [DEFAULT_WORKSPACE_ID]: `${DEFAULT_WORKSPACE_ID}:service-result:r1` },
    deletedWorkspaces: [],
  });

  assert.ok(restored);
  assert.deepEqual(restored.tabs.map((item) => item.view), ["http"]);
  assert.equal(restored.activeTabId, "");
  assert.deepEqual(restored.lastActiveTabByWorkspace, {});
});
