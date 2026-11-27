import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { nextTick } from "vue";
import type { Router } from "vue-router";
import { createAgentControlPaneOperation } from "@/services/agent-control-panes";
import { pendingAgentControlTerminal } from "@/services/agent-control-terminal-launch";
import { resolveTerminalLayoutTarget } from "@/domain/workspace/terminal-layout-scope";
import { useWorkbenchStore } from "@/stores/workbench";
import { useWorkspaceStatePreferencesStore } from "@/stores/workspace-state-preferences";

const bridge = vi.hoisted(() => ({ paneInfo: vi.fn() }));
vi.mock("@/services/agent-control-service", () => ({ invokeAgentControl: bridge.paneInfo }));

const persisted = vi.hoisted(() => ({
  load: vi.fn<(key: string) => Promise<unknown>>(),
  save: vi.fn<(key: string, value: unknown) => Promise<void>>(),
}));

vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: (key: string) => persisted.load(key),
  savePersistedValue: (key: string, value: unknown) => persisted.save(key, value),
}));

import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";

function localPane(id: string, cwd?: string) {
  return { id, type: "local", shellType: "system", cwd };
}

function splitNode(id: string, ...children: unknown[]) {
  return { id, type: "split", direction: "horizontal", children, sizes: children.map(() => 1 / children.length) };
}

function legacyLayout(root: unknown, activePaneId: string) {
  return { version: 3, root, activePaneId };
}

async function hydratedStore(stored: unknown = null) {
  persisted.load.mockImplementation(async () => stored);
  const store = useTerminalWorkspaceStore();
  await store.hydrate();
  return store;
}

beforeEach(() => {
  localStorage.clear();
  setActivePinia(createPinia());
  persisted.load.mockImplementation(async () => null);
  persisted.save.mockImplementation(async () => undefined);
});

describe("terminal workspace store with terminal tabs", () => {
  it("upgrades legacy layouts in current and layoutsByTabId to a single terminal tab", async () => {
    const store = await hydratedStore({
      version: 4,
      current: legacyLayout(splitNode("split-a", localPane("pane-a1"), localPane("pane-a2")), "pane-a2"),
      activeLayoutTabId: "tab-a",
      layoutsByTabId: {
        "tab-a": legacyLayout(splitNode("split-a", localPane("pane-a1"), localPane("pane-a2")), "pane-a2"),
        "tab-b": legacyLayout(localPane("pane-b1"), "pane-b1"),
      },
    });

    expect(store.tabs).toHaveLength(1);
    expect(store.paneIds).toEqual(["pane-a1", "pane-a2"]);
    expect(store.activePaneId).toBe("pane-a2");
    expect(store.activeTabId).toBe(store.tabs[0]!.id);

    store.activateTab("tab-b");
    expect(store.tabs).toHaveLength(1);
    expect(store.paneIds).toEqual(["pane-b1"]);
    expect(store.activePaneId).toBe("pane-b1");
  });

  it("keeps a new split when the active layout is selected again before persistence", async () => {
    const store = await hydratedStore();
    store.activateTab("tab-a", { initialCwd: "/work" });
    store.persistLayout();
    const original = store.activePaneId;
    const first = store.splitPane(original, "horizontal");
    expect(first).toBeTruthy();

    store.activateTab("tab-a");
    const second = store.splitPane(original, "vertical");
    expect(second).toBeTruthy();
    expect(store.allPaneIds).toEqual(expect.arrayContaining([original, first?.id, second?.id]));
  });

  it("opens a controlled terminal tab without replacing a shared layout", async () => {
    const terminal = await hydratedStore();
    const workbench = useWorkbenchStore();
    const settings = useWorkspaceStatePreferencesStore();
    const firstWorkspace = workbench.activeWorkspaceId;
    const first = workbench.createTab("terminal");
    expect(first).toBeTruthy();
    await settings.setIsolationEnabled(false, firstWorkspace);
    const target = resolveTerminalLayoutTarget(workbench.tabs, first!, false, firstWorkspace);
    terminal.activateTab(first!.id, { initialCwd: "/work" }, target.layoutId, target.fallbackLayoutId);
    terminal.splitPane(terminal.activePaneId, "horizontal");
    const original = [...terminal.allPaneIds];

    expect(workbench.createWorkspace("Another workspace")).toBeTruthy();
    const router = {
      push: async () => {
        await nextTick();
        pendingAgentControlTerminal(terminal.activePaneId)?.started();
      },
    } as unknown as Router;
    bridge.paneInfo.mockImplementation(async (_command, { pane }) => ({
      id: pane, title: "", cwd: "/work", alive: true, state: "idle",
    }));
    const operate = createAgentControlPaneOperation(router);
    const result = await operate({ requestId: "new", op: "tab" }, new AbortController().signal);

    expect(terminal.allPaneIds).toEqual([...original, result?.id]);
    expect(terminal.tabs).toHaveLength(2);
  });

  it("normalizes a legacy layout without a valid activePaneId", async () => {
    const store = await hydratedStore({
      version: 4,
      current: { version: 3, root: localPane("pane-x"), activePaneId: "missing" },
      layoutsByTabId: {},
    });
    expect(store.tabs).toHaveLength(1);
    expect(store.activePaneId).toBe("pane-x");
  });

  it("adds more than 13 local tabs; paneIds covers only the active tab", async () => {
    const store = await hydratedStore();
    const firstPaneId = store.activePaneId;
    const firstTabId = store.activeTabId;

    const tab = store.addLocalTab();
    expect(tab).not.toBeNull();
    expect(store.tabs).toHaveLength(2);
    expect(store.activeTabId).toBe(tab!.id);
    expect(store.activeTab.id).toBe(tab!.id);
    expect(store.paneIds).toEqual([tab!.activePaneId]);
    expect(store.allPaneIds).toEqual(expect.arrayContaining([firstPaneId, tab!.activePaneId]));
    expect(store.allPaneIds).toHaveLength(2);
    expect(store.activeTabId).not.toBe(firstTabId);

    for (let i = 0; i < 13; i++) expect(store.addLocalTab()).not.toBeNull();
    expect(store.tabs).toHaveLength(15);
  });

  it("restores every tab and the active pane from a snapshot with 15 tabs", async () => {
    const tabs = Array.from({ length: 15 }, (_, i) => ({
      id: `terminal-tab-${i}`,
      root: localPane(`pane-${i}`),
      activePaneId: `pane-${i}`,
    }));
    const store = await hydratedStore({
      version: 5,
      current: { version: 4, tabs, activeTabId: "terminal-tab-14" },
      layoutsByTabId: {},
    });
    expect(store.tabs.map((tab) => tab.id)).toEqual(tabs.map((tab) => tab.id));
    expect(store.activeTabId).toBe("terminal-tab-14");
    expect(store.activePaneId).toBe("pane-14");
  });

  it("new local tabs use the default directory unless a folder is supplied", async () => {
    const store = await hydratedStore({
      version: 5,
      current: {
        version: 4,
        tabs: [{ id: "terminal-tab-1", root: localPane("pane-1", "/work/dir"), activePaneId: "pane-1" }],
        activeTabId: "terminal-tab-1",
      },
      layoutsByTabId: {},
    });
    const tab = store.addLocalTab();
    const pane = store.getPane(tab!.activePaneId);
    expect(pane?.type).toBe("local");
    expect(pane?.type === "local" ? pane.initialCwd : undefined).toBeUndefined();
    const explicit = store.addLocalTab({ initialCwd: "/other" });
    const explicitPane = store.getPane(explicit!.activePaneId);
    expect(explicitPane?.type === "local" ? explicitPane.initialCwd : undefined).toBe("/other");
  });

  it("closeTab returns every pane of the tab and refuses the last tab", async () => {
    const store = await hydratedStore();
    const firstTabId = store.activeTabId;
    const tab = store.addLocalTab()!;
    const split = store.splitPane(tab.activePaneId, "vertical")!;

    expect(store.closeTab(tab.id)).toEqual([tab.activePaneId, split.id]);
    expect(store.tabs).toHaveLength(1);
    expect(store.activeTabId).toBe(firstTabId);
    expect(store.closeTab(firstTabId)).toBeNull();
    expect(store.tabs).toHaveLength(1);
  });

  it("selectPane switches to the tab that owns the pane", async () => {
    const store = await hydratedStore();
    const firstTabId = store.activeTabId;
    const firstPaneId = store.activePaneId;
    const second = store.addLocalTab()!;
    expect(store.activeTabId).toBe(second.id);

    store.selectPane(firstPaneId);
    expect(store.activeTabId).toBe(firstTabId);
    expect(store.activePaneId).toBe(firstPaneId);
    expect(store.getPane(second.activePaneId)?.id).toBe(second.activePaneId);
  });

  it("closePane on a background tab's pane closes it there and leaves the active tab alone", async () => {
    const store = await hydratedStore();
    const backgroundTabId = store.activeTabId;
    const keptPaneId = store.activePaneId;
    const closing = store.splitPane(keptPaneId, "horizontal")!;
    const active = store.addLocalTab()!;
    const activeSplit = store.splitPane(active.activePaneId, "vertical")!;
    const activeTabBefore = JSON.stringify(store.activeTab);

    expect(store.closePane(closing.id)).toBe(true);
    expect(store.activeTabId).toBe(active.id);
    expect(JSON.stringify(store.activeTab)).toBe(activeTabBefore);
    expect(store.paneIds).toEqual([active.activePaneId, activeSplit.id]);
    const background = store.tabs.find((tab) => tab.id === backgroundTabId)!;
    expect(background.root.id).toBe(keptPaneId);
    expect(background.activePaneId).toBe(keptPaneId);
    expect(store.getPane(closing.id)).toBeUndefined();
  });

  it("selects, cycles and renames tabs", async () => {
    const store = await hydratedStore();
    const firstTabId = store.activeTabId;
    const second = store.addLocalTab()!;

    store.selectTab(firstTabId);
    expect(store.activeTabId).toBe(firstTabId);
    store.selectAdjacentTab(1);
    expect(store.activeTabId).toBe(second.id);
    store.selectAdjacentTab(1);
    expect(store.activeTabId).toBe(firstTabId);
    store.selectAdjacentTab(-1);
    expect(store.activeTabId).toBe(second.id);

    store.renameTab(second.id, "  build  ");
    expect(store.activeTab.title).toBe("build");
    store.renameTab(second.id, "");
    expect(store.activeTab.title).toBeUndefined();
  });

  it("paneIdsForLayout and localPaneIds cover every terminal tab", async () => {
    const store = await hydratedStore();
    store.activateTab("layout-a");
    const firstPaneId = store.activePaneId;
    const second = store.addLocalTab()!;

    expect(store.paneIdsForLayout("layout-a")).toEqual(expect.arrayContaining([firstPaneId, second.activePaneId]));
    expect(store.localPaneIds()).toEqual(expect.arrayContaining([firstPaneId, second.activePaneId]));

    expect(store.updateLocalPaneCwd(firstPaneId, "/moved")).toBe(true);
    const moved = store.getPane(firstPaneId);
    expect(moved?.type === "local" ? moved.cwd : undefined).toBe("/moved");

    store.activateTab("layout-b", {});
    expect(store.paneIdsForLayout("layout-a")).toEqual(expect.arrayContaining([firstPaneId, second.activePaneId]));
    expect(store.paneIdsForLayout("layout-a")).toHaveLength(2);
  });

  it("activateTab with an initial pane creates a fresh single-tab layout", async () => {
    const store = await hydratedStore();
    store.activateTab("layout-a");
    store.addLocalTab();
    store.activateTab("layout-b", { initialCwd: "/fresh" });
    expect(store.tabs).toHaveLength(1);
    const pane = store.getPane(store.activePaneId);
    expect(pane?.type === "local" ? pane.initialCwd : undefined).toBe("/fresh");

    store.activateTab("layout-a");
    expect(store.tabs).toHaveLength(2);
  });

  it("duplicateTab copies every terminal tab with new tab and pane ids", async () => {
    const store = await hydratedStore();
    store.activateTab("layout-a");
    const second = store.addLocalTab()!;
    store.splitPane(second.activePaneId, "horizontal");
    const sourceTabIds = store.tabs.map((tab) => tab.id);
    const sourcePaneIds = store.allPaneIds;

    expect(store.duplicateTab("layout-a", "layout-b")).toBe(true);
    store.activateTab("layout-b");

    expect(store.tabs).toHaveLength(sourceTabIds.length);
    for (const tab of store.tabs) expect(sourceTabIds).not.toContain(tab.id);
    expect(store.allPaneIds).toHaveLength(sourcePaneIds.length);
    for (const paneId of store.allPaneIds) expect(sourcePaneIds).not.toContain(paneId);
    expect(store.tabs.map((tab) => tab.id)).toContain(store.activeTabId);
  });

  it("drops broken tabs and keeps a valid activeTabId", async () => {
    const store = await hydratedStore({
      version: 5,
      current: {
        version: 4,
        tabs: [
          { id: "terminal-tab-bad", root: { id: "broken", type: "nope" }, activePaneId: "broken" },
          { id: "terminal-tab-good", root: localPane("pane-good"), activePaneId: "pane-good" },
        ],
        activeTabId: "terminal-tab-missing",
      },
      layoutsByTabId: {},
    });
    expect(store.tabs.map((tab) => tab.id)).toEqual(["terminal-tab-good"]);
    expect(store.activeTabId).toBe("terminal-tab-good");
    expect(store.activePaneId).toBe("pane-good");
  });

  it.each([
    ["empty tabs", { version: 4, tabs: [], activeTabId: "terminal-tab-x" }],
    [
      "only broken tabs",
      { version: 4, tabs: [{ id: "terminal-tab-bad", root: { id: "x", type: "split", children: [] }, activePaneId: "x" }], activeTabId: "terminal-tab-bad" },
    ],
  ])("falls back to a default local terminal for %s", async (_name, current) => {
    const store = await hydratedStore({ version: 5, current, layoutsByTabId: {} });
    expect(store.tabs).toHaveLength(1);
    expect(store.activeTabId).toBe(store.tabs[0]!.id);
    expect(store.root.type).toBe("local");
    expect(store.paneIds).toEqual([store.activePaneId]);
  });

  it("applyLayoutSnapshot accepts a legacy popup snapshot", async () => {
    const store = await hydratedStore();
    store.addLocalTab();
    expect(store.applyLayoutSnapshot(legacyLayout(localPane("popup-pane"), "popup-pane"))).toBe(true);
    expect(store.tabs).toHaveLength(1);
    expect(store.activePaneId).toBe("popup-pane");
    expect(store.applyLayoutSnapshot({ version: 4, tabs: [], activeTabId: "x" })).toBe(false);
    expect(store.activePaneId).toBe("popup-pane");
  });

  it("layoutSnapshot round-trips all tabs and persists the v5 workspace", async () => {
    const store = await hydratedStore();
    store.activateTab("layout-a");
    const second = store.addLocalTab()!;
    store.renameTab(second.id, "logs");
    const snapshot = store.layoutSnapshot();
    expect(snapshot.version).toBe(4);
    expect(snapshot.tabs).toHaveLength(2);
    expect(snapshot.activeTabId).toBe(second.id);

    await store.persistLayout();
    const saved = persisted.save.mock.calls.at(-1)?.[1] as { version: number; current: { tabs: unknown[] } };
    expect(saved.version).toBe(5);
    expect(saved.current.tabs).toHaveLength(2);

    setActivePinia(createPinia());
    const reloaded = await hydratedStore(saved);
    expect(reloaded.tabs.map((tab) => tab.id)).toEqual(snapshot.tabs.map((tab) => tab.id));
    expect(reloaded.activeTab.title).toBe("logs");
  });
});

describe("terminal tab callers cover every terminal tab", () => {
  const source = (path: string) => readFileSync(resolve(process.cwd(), path), "utf8");

  it("paneIdsForLayout covers both tabs of a two-tab workspace (attention markers and cleanup)", async () => {
    const store = await hydratedStore();
    store.activateTab("layout-a");
    const firstPaneId = store.activePaneId;
    const second = store.addLocalTab()!;
    const split = store.splitPane(second.activePaneId, "horizontal")!;
    store.selectTab(store.tabs[0]!.id);

    expect(store.paneIds).toEqual([firstPaneId]);
    expect([...store.paneIdsForLayout("layout-a")].sort()).toEqual([firstPaneId, second.activePaneId, split.id].sort());
  });

  it("tab cleanup and attention pruning watch every terminal tab", () => {
    const cleanup = source("src/composables/use-terminal-tab-cleanup.ts");
    expect(cleanup).toMatch(/terminalWorkspace\.tabs/);
    expect(cleanup).not.toMatch(/terminalWorkspace\.root/);
    const attention = source("src/composables/use-terminal-attention.ts");
    expect(attention).not.toMatch(/terminalWorkspace\.root/);
    expect(attention).toMatch(/terminalWorkspace\.paneIds\b/);
  });

  it("the AI assistant lists, watches and closes panes across every terminal tab", () => {
    const drawer = source("src/components/ai/AiAssistantDrawer.vue");
    expect(drawer).not.toMatch(/terminalPanes\(terminalWorkspace\.root\)/);
    expect(drawer).not.toMatch(/terminalWorkspace\.root\b/);
    expect(drawer).toMatch(/\(\) => terminalWorkspace\.tabs/);
    const host = source("src/services/app-ai-capability-deps.ts");
    expect(host).toMatch(/aiCloseTargetForPane\(/);
    expect(host).not.toMatch(/terminalWorkspace\.canClose/);
    expect(host).toMatch(/terminalWorkspace\.closeTab\(/);
  });
});
