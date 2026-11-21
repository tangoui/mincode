import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const settings = vi.hoisted(() => ({ value: {} as Record<string, unknown> }));
const sent = vi.hoisted(() => [] as Array<{ title: string; body: string; paneId: string }>);
vi.mock("@/services/settings-persistence", () => ({ readSettingsSnapshot: () => settings.value }));
vi.mock("@/services/notifications", () => ({
  notifyTerminalAttention: (title: string, body: string, paneId: string) => sent.push({ title, body, paneId }),
}));

import { useTerminalAttentionStore } from "@/stores/terminal-attention";

let focused = false;
const owned = new Set(["a", "b", "c"]);

function setup() {
  const store = useTerminalAttentionStore();
  store.configure({
    owns: (paneId) => owned.has(paneId),
    describe: (paneId) => `Tab ${paneId}`,
  });
  store.ensureListening();
  return store;
}

function setFocus(value: boolean) {
  focused = value;
  window.dispatchEvent(new Event(value ? "focus" : "blur"));
}

describe("terminal attention store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    settings.value = {};
    sent.length = 0;
    owned.clear();
    for (const id of ["a", "b", "c"]) owned.add(id);
    vi.spyOn(document, "hasFocus").mockImplementation(() => focused);
    focused = false;
    vi.useFakeTimers();
    vi.setSystemTime(10_000);
  });

  it("marks unseen panes, notifies the system once per burst, and ignores unknown panes", () => {
    const store = setup();
    store.report({ paneId: "a", reason: "completed", at: 1 });
    store.report({ paneId: "b", reason: "waiting_approval", message: "rm -rf build", at: 2 });
    store.report({ paneId: "zzz", reason: "bell", at: 3 });
    expect([...store.items.keys()]).toEqual(["a", "b"]);
    expect(store.count).toBe(2);
    expect(sent).toEqual([{ title: "Tab a", body: "已完成", paneId: "a" }]);
    vi.setSystemTime(12_000);
    store.report({ paneId: "c", reason: "failed", message: "exit 1", at: 4 });
    expect(sent.at(-1)).toEqual({ title: "Tab c", body: "失败了 · exit 1", paneId: "c" });
  });

  it("marks even the pane on screen (no system notification in front); switching to a pane acknowledges it", () => {
    const store = setup();
    setFocus(true);
    store.setVisiblePanes("terminal", ["a"]);
    store.report({ paneId: "a", reason: "quiet", at: 1 });
    expect(store.items.has("a")).toBe(true);
    expect(sent).toHaveLength(0);

    // Re-publishing the same visible panes, or the window regaining focus, keeps the marker.
    store.setVisiblePanes("terminal", ["a"]);
    setFocus(false);
    setFocus(true);
    expect(store.items.has("a")).toBe(true);

    // In the background the announcement is a system notification.
    setFocus(false);
    vi.setSystemTime(12_000);
    store.report({ paneId: "b", reason: "completed", at: 2 });
    expect(sent).toEqual([{ title: "Tab b", body: "已完成", paneId: "b" }]);

    // Switching to a pane while unfocused does not count; while focused it does.
    store.setVisiblePanes("sessions", ["b"]);
    expect(store.items.has("b")).toBe(true);
    store.setVisiblePanes("sessions", []);
    setFocus(true);
    store.setVisiblePanes("sessions", ["b"]);
    expect(store.items.has("b")).toBe(false);

    store.dismiss(["a"]);
    expect(store.count).toBe(0);
  });

  it("follows the settings switches", () => {
    const store = setup();
    setFocus(true);
    settings.value = { terminalAttentionSystemNotify: false };
    store.report({ paneId: "a", reason: "completed", at: 1 });
    setFocus(false);
    store.report({ paneId: "b", reason: "completed", at: 2 });
    expect([...store.items.keys()]).toEqual(["a", "b"]);
    expect(sent).toHaveLength(0);

    settings.value = { terminalAttention: false };
    store.report({ paneId: "c", reason: "failed", at: 3 });
    expect(store.items.has("c")).toBe(false);
  });

  it("drops panes whose tab closed and focuses a pane on request", () => {
    const store = setup();
    store.report({ paneId: "a", reason: "quiet", at: 1 });
    store.report({ paneId: "b", reason: "quiet", at: 1 });
    owned.delete("a");
    store.prune();
    expect([...store.items.keys()]).toEqual(["b"]);
    store.setSessionPanes([["b", "CC 1"]]);
    expect(store.sessionPanes.get("b")).toBe("CC 1");
    store.requestFocus("b");
    store.requestFocus("b");
    expect(store.focusRequest).toEqual({ paneId: "b", seq: 2 });
    store.dismiss(["b"]);
    expect(store.count).toBe(0);
    store.stop();
  });
});
