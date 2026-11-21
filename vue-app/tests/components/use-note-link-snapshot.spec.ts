import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import type { Router } from "vue-router";
import type { LinkObjectSnapshot } from "@/domain/notes/link-targets";
import type { NoteLink, NoteLinkType } from "@/domain/notes/types";

const persistence = vi.hoisted(() => ({ loadAiWorkspaceOverview: vi.fn() }));
const actions = vi.hoisted(() => ({ requestAiWorkspaceAction: vi.fn() }));
const aiShell = vi.hoisted(() => ({ loadAiAssistantStore: vi.fn() }));

vi.mock("@/services/ai-conversation-persistence", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  ...persistence,
}));
vi.mock("@/services/ai-workspace-actions", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  ...actions,
}));
vi.mock("@/stores/ai-assistant-shell", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  ...aiShell,
}));
// Pretend the desktop AI store exists so the overview is read; everything else keeps its browser behavior.
vi.mock("@/services/desktop-bridge", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/services/desktop-bridge")>();
  return {
    ...original,
    canInvoke: (command: Parameters<typeof original.canInvoke>[0]) =>
      command === "ai_store_load_workspace" || original.canInvoke(command),
  };
});

import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import {
  invalidateNoteLinkAiOverviews,
  openLinkTarget,
  useLinkObjectSnapshot,
} from "@/composables/use-note-link-snapshot";

function mountSnapshots(count: number) {
  const snapshots: Array<ReturnType<typeof useLinkObjectSnapshot>> = [];
  const Probe = defineComponent({
    setup() {
      snapshots.push(useLinkObjectSnapshot());
      return () => h("div");
    },
  });
  const wrapper = mount(defineComponent({ render: () => Array.from({ length: count }, () => h(Probe)) }));
  return { wrapper, snapshots };
}

function link(objectType: NoteLinkType, objectId: string): NoteLink {
  return { objectType, objectId, parentId: "", scopeId: "", nameSnapshot: objectId, scopeNameSnapshot: "", createdAt: 1 };
}

function snapshot(): LinkObjectSnapshot {
  return {
    ssh: new Map([["ssh-1", "prod"]]),
    database: new Map([["db-1", "orders"]]),
    http: new Map(),
    httpCollections: new Map(),
    currentProjects: new Map(),
    otherWorkspaceProjects: new Map(),
    currentAi: new Map(),
    currentScopeId: "scope-a",
    liveScopeIds: new Set(["scope-a"]),
    workspaceNames: new Map(),
    unavailable: new Set(),
  };
}

beforeEach(() => {
  persistence.loadAiWorkspaceOverview.mockReset();
  actions.requestAiWorkspaceAction.mockReset();
  invalidateNoteLinkAiOverviews();
});

describe("useLinkObjectSnapshot", () => {
  it("shares one AI overview load between snapshot instances", async () => {
    persistence.loadAiWorkspaceOverview.mockResolvedValue({ conversations: [{ id: "conv-1", title: "Plan" }] });
    const { wrapper, snapshots } = mountSnapshots(2);
    await flushPromises();

    expect(persistence.loadAiWorkspaceOverview).toHaveBeenCalledTimes(1);
    for (const value of snapshots) {
      expect(value.value.currentAi.get("conv-1")).toBe("Plan");
      expect(value.value.unavailable.has("ai")).toBe(false);
    }

    // An explicit invalidation reloads once for all instances.
    persistence.loadAiWorkspaceOverview.mockResolvedValue({ conversations: [{ id: "conv-2", title: "Next" }] });
    invalidateNoteLinkAiOverviews();
    await flushPromises();
    expect(persistence.loadAiWorkspaceOverview).toHaveBeenCalledTimes(2);
    for (const value of snapshots) expect(value.value.currentAi.get("conv-2")).toBe("Next");

    // A snapshot created later reuses the cache.
    const later = mountSnapshots(1);
    await flushPromises();
    expect(persistence.loadAiWorkspaceOverview).toHaveBeenCalledTimes(2);
    later.wrapper.unmount();
    wrapper.unmount();
  });

  it("marks AI as unavailable when the overview fails to load", async () => {
    persistence.loadAiWorkspaceOverview.mockRejectedValue(new Error("disk"));
    const { wrapper, snapshots } = mountSnapshots(1);
    await flushPromises();
    expect(snapshots[0]!.value.unavailable.has("ai")).toBe(true);
    wrapper.unmount();
  });
});

describe("database availability", () => {
  it("depends only on loading the saved connection list, not on query loading or errors", async () => {
    persistence.loadAiWorkspaceOverview.mockResolvedValue({ conversations: [] });
    const { wrapper, snapshots } = mountSnapshots(1);
    await flushPromises();
    const database = useDatabaseWorkspaceStore();
    expect(database.connectionsStatus).toBe("ready");
    expect(snapshots[0]!.value.unavailable.has("database")).toBe(false);

    // loading / error 也反映单次查询等运行时状态。
    database.loading = true;
    database.error = "query failed";
    await flushPromises();
    expect(snapshots[0]!.value.unavailable.has("database")).toBe(false);
    wrapper.unmount();
  });
});

describe("openLinkTarget", () => {
  it("only opens the SSH and database pages, never a session or connection", async () => {
    const push = vi.fn().mockResolvedValue(undefined);
    const router = { push } as unknown as Router;

    await openLinkTarget(link("ssh", "ssh-1"), snapshot(), router);
    await openLinkTarget(link("database", "db-1"), snapshot(), router);

    expect(push).toHaveBeenCalledWith({ name: "ssh" });
    expect(push).toHaveBeenCalledWith({ name: "database" });
    expect(actions.requestAiWorkspaceAction).not.toHaveBeenCalled();
  });

  it("does nothing for links whose state is unknown", async () => {
    const push = vi.fn().mockResolvedValue(undefined);
    const value = { ...snapshot(), ssh: new Map(), unavailable: new Set<NoteLinkType>(["ssh"]) };
    await openLinkTarget(link("ssh", "ssh-1"), value, { push } as unknown as Router);
    expect(push).not.toHaveBeenCalled();
  });

  it("switches to an AI conversation that moved into the current workspace using the current scope", async () => {
    const switchToConversation = vi.fn().mockResolvedValue(true);
    aiShell.loadAiAssistantStore.mockResolvedValue({ hydrated: true, show: vi.fn(), switchToConversation });
    const value = { ...snapshot(), currentAi: new Map([["conv-1", "Plan"]]), liveScopeIds: new Set(["scope-a", "scope-b"]) };
    const stale = { ...link("ai", "conv-1"), scopeId: "scope-b" };
    await openLinkTarget(stale, value, { push: vi.fn() } as unknown as Router);
    expect(switchToConversation).toHaveBeenCalledWith("conv-1", "scope-a");
  });
});
