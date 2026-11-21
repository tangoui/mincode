import { describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia, type Pinia } from "pinia";

// 一个共享的“后端”与事件总线，模拟多个 WebView 连接同一个 Go 进程。
interface FakeState {
  enabled: boolean;
  autoLockMinutes: number;
  lockOnLaunch: boolean;
  locked: boolean;
  revision: number;
}

const backend = vi.hoisted(() => {
  const listeners = new Set<(payload: unknown) => void>();
  const store = {
    passcode: undefined as string | undefined,
    failed: 0,
    state: { enabled: false, autoLockMinutes: 10, lockOnLaunch: true, locked: false, revision: 0 } as FakeState,
    calls: [] as Array<{ command: string; args: Record<string, unknown> }>,
  };
  function publicState() {
    return { ...store.state };
  }
  function commit(change: (s: FakeState) => void) {
    change(store.state);
    store.state.revision += 1;
    const snapshot = publicState();
    for (const listener of listeners) listener(snapshot);
    return snapshot;
  }
  function attempt(ok: boolean) {
    if (!ok) store.failed += 1;
    else store.failed = 0;
    return { ok, failedAttempts: store.failed, retryAfterMs: 0, state: publicState() };
  }
  async function invoke(command: string, args: Record<string, unknown> = {}) {
    store.calls.push({ command, args });
    switch (command) {
      case "app_lock_state":
        return publicState();
      case "app_lock_lock":
        if (!store.state.enabled || store.state.locked) return publicState();
        return commit((s) => { s.locked = true; });
      case "app_lock_unlock": {
        if (store.passcode !== undefined && args.passcode !== store.passcode) return attempt(false);
        if (!store.state.locked) return attempt(true);
        commit((s) => { s.locked = false; });
        return attempt(true);
      }
      case "app_lock_set_passcode": {
        if (store.passcode !== undefined && args.currentPasscode !== store.passcode) return attempt(false);
        store.passcode = String(args.passcode);
        commit((s) => { s.enabled = true; s.locked = false; });
        return attempt(true);
      }
      case "app_lock_disable": {
        if (store.passcode !== undefined && args.currentPasscode !== store.passcode) return attempt(false);
        store.passcode = undefined;
        commit((s) => { s.enabled = false; s.locked = false; });
        return attempt(true);
      }
      case "app_lock_set_preferences":
        return commit((s) => {
          if (typeof args.autoLockMinutes === "number") s.autoLockMinutes = args.autoLockMinutes;
          if (typeof args.lockOnLaunch === "boolean") s.lockOnLaunch = args.lockOnLaunch;
        });
      default:
        throw new Error(`unexpected command ${command}`);
    }
  }
  function reset() {
    listeners.clear();
    store.passcode = undefined;
    store.failed = 0;
    store.state = { enabled: false, autoLockMinutes: 10, lockOnLaunch: true, locked: false, revision: 0 };
    store.calls = [];
  }
  return { listeners, store, invoke, reset };
});

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  invokeBackend: (command: string, args?: Record<string, unknown>) => backend.invoke(command, args),
  emitAppEvent: async () => undefined,
  listenAppEvent: (_event: string, handler: (payload: unknown) => void) => {
    backend.listeners.add(handler);
    return () => backend.listeners.delete(handler);
  },
}));

const { useAppLockStore } = await import("@/stores/app-lock");

async function settle() {
  for (let i = 0; i < 5; i += 1) await Promise.resolve();
  await new Promise((resolve) => setTimeout(resolve, 0));
}

async function openWindow() {
  const pinia: Pinia = createPinia();
  setActivePinia(pinia);
  const store = useAppLockStore();
  store.ensureListening();
  await store.hydrate();
  return store;
}

describe("app lock across windows", () => {
  it("rejects the old passcode after another window changed it (stale verifier)", async () => {
    backend.reset();
    const main = await openWindow();
    await main.enable("old-pass");
    const popup = await openWindow();

    expect(await main.changePasscode("old-pass", "new-pass")).toBe(true);
    await settle();

    popup.lock();
    await settle();
    expect(main.locked).toBe(true);
    expect(popup.locked).toBe(true);

    // 旧实现里 popup 用缓存的旧记录校验并“解锁成功”，然后把旧记录写回存储。
    expect(await popup.unlock("old-pass")).toBe(false);
    expect(popup.locked).toBe(true);
    expect(main.locked).toBe(true);
    expect(backend.store.passcode).toBe("new-pass");

    expect(await popup.unlock("new-pass")).toBe(true);
    await settle();
    expect(main.locked).toBe(false);
  });

  it("propagates enable/disable to every window", async () => {
    backend.reset();
    const main = await openWindow();
    const popup = await openWindow();
    await main.enable("secret-1");
    await settle();
    expect(popup.configured).toBe(true);

    expect(await main.disable("secret-1")).toBe(true);
    await settle();
    expect(popup.configured).toBe(false);
    // 关闭后其他窗口的锁定请求不再生效，也不会携带过期配置写回。
    expect(popup.lock()).toBe(false);
  });

  it("ignores spoofed or stale broadcast payloads and re-reads the backend", async () => {
    backend.reset();
    const main = await openWindow();
    await main.enable("secret-2");
    main.lock();
    await settle();
    expect(main.locked).toBe(true);

    // 任意渲染进程都能发同名事件；载荷本身不被信任，只触发重新拉取。
    for (const listener of backend.listeners) listener({ locked: false, enabled: false, revision: 9999 });
    await settle();
    expect(main.locked).toBe(true);
    expect(main.configured).toBe(true);
  });

  it("never sends verifier material and never writes the store key directly", async () => {
    backend.reset();
    const main = await openWindow();
    await main.enable("secret-3");
    main.lock();
    await settle();
    await main.unlock("secret-3");
    await main.setAutoLockMinutes(5);
    await main.setLockOnLaunch(false);
    for (const { command, args } of backend.store.calls) {
      expect(command.startsWith("store_")).toBe(false);
      expect(command).not.toBe("passcode_verify");
      for (const key of ["salt", "hash", "iterations", "algorithm"]) expect(args).not.toHaveProperty(key);
    }
    expect(main.autoLockMinutes).toBe(5);
    expect(main.lockOnLaunch).toBe(false);
  });

  it("keeps failed attempts in sync with the backend counter", async () => {
    backend.reset();
    const main = await openWindow();
    await main.enable("secret-4");
    main.lock();
    await settle();
    expect(await main.unlock("nope")).toBe(false);
    expect(await main.unlock("nope")).toBe(false);
    expect(main.failedAttempts).toBe(2);
    expect(main.locked).toBe(true);
  });
});
