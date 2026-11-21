import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { pickRecent } from "@/domain/connections/overview";
import { useSshRecentStore } from "@/stores/ssh-recent";

vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn(),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));

beforeEach(() => {
  setActivePinia(createPinia());
  vi.mocked(loadPersistedValue).mockReset();
  vi.mocked(savePersistedValue).mockClear();
});

describe("ssh recent store", () => {
  it("records and persists when a host was opened", () => {
    const store = useSshRecentStore();
    store.markOpened("a", 100);
    expect(store.openedAt.a).toBe(100);
    expect(savePersistedValue).toHaveBeenCalledWith("app_ssh_recent_v1", {
      version: 1,
      openedAt: { a: 100 },
      clearedAt: 0,
    });
  });

  it("clearing hides older records until the host is opened again", () => {
    const store = useSshRecentStore();
    store.markOpened("a", 100);
    store.clearRecent(200);
    expect(pickRecent(["a"], (id) => store.openedAt[id], store.clearedAt)).toEqual([]);
    store.markOpened("a", 300);
    expect(pickRecent(["a"], (id) => store.openedAt[id], store.clearedAt)).toEqual(["a"]);
    expect(savePersistedValue).toHaveBeenLastCalledWith("app_ssh_recent_v1", {
      version: 1,
      openedAt: { a: 300 },
      clearedAt: 200,
    });
  });

  it("prunes records of hosts that no longer exist and only saves on change", () => {
    const store = useSshRecentStore();
    store.markOpened("a", 1);
    store.markOpened("b", 2);
    vi.mocked(savePersistedValue).mockClear();
    store.prune(new Set(["b"]));
    expect(store.openedAt).toEqual({ b: 2 });
    expect(savePersistedValue).toHaveBeenCalledTimes(1);
    store.prune(new Set(["b"]));
    expect(savePersistedValue).toHaveBeenCalledTimes(1);
  });

  it("ignores invalid persisted fields on hydrate", async () => {
    vi.mocked(loadPersistedValue).mockResolvedValueOnce({ version: 1, openedAt: { a: "x", b: 5 }, clearedAt: "y" });
    const store = useSshRecentStore();
    await store.hydrate();
    expect(loadPersistedValue).toHaveBeenCalledWith("app_ssh_recent_v1");
    expect(store.openedAt).toEqual({ b: 5 });
    expect(store.clearedAt).toBe(0);
  });

  it("ignores persisted data with an unknown version", async () => {
    vi.mocked(loadPersistedValue).mockResolvedValueOnce({ version: 2, openedAt: { b: 5 }, clearedAt: 9 });
    const store = useSshRecentStore();
    await store.hydrate();
    expect(store.openedAt).toEqual({});
    expect(store.clearedAt).toBe(0);
  });

  it("keeps hosts opened before hydration finished", async () => {
    let resolve!: (value: unknown) => void;
    vi.mocked(loadPersistedValue).mockReturnValueOnce(new Promise((r) => { resolve = r; }));
    const store = useSshRecentStore();
    const pending = store.hydrate();
    store.markOpened("a", 50);
    resolve({ version: 1, openedAt: { a: 10, b: 5 }, clearedAt: 3 });
    await pending;
    expect(store.openedAt).toEqual({ a: 50, b: 5 });
    expect(store.clearedAt).toBe(3);
    expect(savePersistedValue).toHaveBeenLastCalledWith("app_ssh_recent_v1", {
      version: 1,
      openedAt: { a: 50, b: 5 },
      clearedAt: 3,
    });
  });
});
