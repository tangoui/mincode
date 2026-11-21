import { beforeEach, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const host = vi.hoisted(() => ({
  invoke: vi.fn<(command: string, args?: unknown) => Promise<unknown>>(),
  listen: vi.fn<(name: string, callback: (event: unknown) => void) => () => void>(),
}));

vi.mock("@/services/desktop-bridge", () => ({
  invokeBackend: host.invoke,
  listenAppEvent: host.listen,
  isDesktopRuntime: () => true,
}));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn(async () => undefined),
  savePersistedValue: vi.fn(async () => undefined),
}));
vi.mock("@/services/notifications", () => ({ notifyApp: vi.fn() }));

import { useAppUpdateStore } from "@/stores/app-update";

beforeEach(() => {
  setActivePinia(createPinia());
  host.invoke.mockReset();
  host.listen.mockReset();
});

it("treats a release check with no update as up to date", async () => {
  host.invoke.mockResolvedValueOnce({
    available: false, version: "", notes: "", currentVersion: "0.4.0", platform: "darwin-arm64",
  });
  const store = useAppUpdateStore();
  expect((await store.checkNow()).available).toBe(false);
  expect(store.pendingVersion).toBeUndefined();
  expect(store.checking).toBe(false);
});

it("tracks download progress, then uses restart after the verified update is ready", async () => {
  const stop = vi.fn();
  host.listen.mockReturnValue(stop);
  host.invoke.mockImplementationOnce(async () => {
    const callback = host.listen.mock.calls[0]![1];
    callback({ kind: "progress", done: 1, total: 2 });
    expect(useAppUpdateStore().progressPercent).toBe(50);
    callback({ kind: "patch-failed" });
    expect(useAppUpdateStore().progressPercent).toBeUndefined();
    return { version: "99.0.0", ready: true };
  });
  host.invoke.mockResolvedValueOnce(null);
  const store = useAppUpdateStore();

  await store.stageUpdate("99.0.0");
  expect(host.listen).toHaveBeenCalledWith("app-update:progress", expect.any(Function));
  expect(host.invoke).toHaveBeenCalledWith("download_and_install", { version: "99.0.0" });
  expect(store.readyVersion).toBe("99.0.0");
  expect(store.downloading).toBe(false);
  expect(stop).toHaveBeenCalledOnce();

  await store.restart();
  expect(host.invoke).toHaveBeenLastCalledWith("restart_app");
});

it("keeps manual updates available and never offers a restart", async () => {
  const releaseURL = "https://github.com/tangoui/mincode/releases/latest";
  const check = {
    available: true, version: "99.0.0", notes: "", currentVersion: "0.4.0", platform: "linux-arm64",
  };
  host.invoke
    .mockResolvedValueOnce({ ...check, releaseURL })
    .mockResolvedValueOnce(check)
    .mockResolvedValueOnce({ version: "99.0.0", ready: false, releaseURL });
  host.listen.mockReturnValue(() => {});
  const store = useAppUpdateStore();

  expect(await store.checkNow()).toMatchObject({ releaseURL });
  expect(store.pendingVersion).toBe("99.0.0");

  await store.checkNow();
  expect(await store.stageUpdate("99.0.0")).toMatchObject({ ready: false, releaseURL });
  expect(store.info?.releaseURL).toBe(releaseURL);
  expect(store.readyVersion).toBeUndefined();
  expect(store.downloading).toBe(false);
});
