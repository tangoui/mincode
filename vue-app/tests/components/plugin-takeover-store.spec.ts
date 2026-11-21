import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { usePluginTakeoverStore, setPluginMenuAccelerators, setPluginWindowFullscreen } from "@/stores/plugin-takeover";

const windowCalls: boolean[] = [];
const menuCalls: boolean[] = [];

beforeEach(() => {
  setActivePinia(createPinia());
  windowCalls.length = 0;
  menuCalls.length = 0;
  setPluginWindowFullscreen(async (on) => { windowCalls.push(on); });
  setPluginMenuAccelerators(async (enabled) => { menuCalls.push(enabled); });
});

describe("plugin immersive mode", () => {
  it("lets one session enter and only that session exit", async () => {
    const store = usePluginTakeoverStore();
    const changes: Array<[string, boolean]> = [];
    store.onImmersiveChange((sessionId, active) => changes.push([sessionId, active]));
    await store.enterImmersive("s-1");
    expect(store.immersive).toEqual({ sessionId: "s-1" });
    expect(windowCalls).toEqual([true]);
    expect(await store.exitImmersive("s-2")).toBe(false);
    expect(store.immersive).toEqual({ sessionId: "s-1" });
    expect(await store.exitImmersive("s-1")).toBe(true);
    expect(store.immersive).toBeNull();
    expect(windowCalls).toEqual([true, false]);
    expect(changes).toEqual([["s-1", true], ["s-1", false]]);
  });

  it("hands immersive mode to a newer session and tells the previous one", async () => {
    const store = usePluginTakeoverStore();
    const changes: Array<[string, boolean]> = [];
    store.onImmersiveChange((sessionId, active) => changes.push([sessionId, active]));
    await store.enterImmersive("s-1");
    await store.enterImmersive("s-2");
    expect(store.immersive).toEqual({ sessionId: "s-2" });
    expect(changes).toEqual([["s-1", true], ["s-1", false], ["s-2", true]]);
    expect(windowCalls).toEqual([true]);
  });

  it("the host exit always works and ends keyboard capture too", async () => {
    const store = usePluginTakeoverStore();
    await store.enterImmersive("s-1");
    store.startCapture("s-1");
    await store.exitAll();
    expect(store.immersive).toBeNull();
    expect(store.capture).toBeNull();
    expect(windowCalls).toEqual([true, false]);
  });

  it("releases a session's takeover when it goes away", async () => {
    const store = usePluginTakeoverStore();
    await store.enterImmersive("s-1");
    store.startCapture("s-1");
    await store.releaseSession("s-2");
    expect(store.immersive).toEqual({ sessionId: "s-1" });
    await store.releaseSession("s-1");
    expect(store.immersive).toBeNull();
    expect(store.capture).toBeNull();
  });

  it("keeps the layout hidden when the window cannot go fullscreen", async () => {
    setPluginWindowFullscreen(async () => { throw new Error("not available"); });
    const store = usePluginTakeoverStore();
    await store.enterImmersive("s-1");
    expect(store.immersive).toEqual({ sessionId: "s-1" });
  });
});

describe("plugin keyboard capture", () => {
  it("is held by one session and released only by it or the host", () => {
    const store = usePluginTakeoverStore();
    const released: string[] = [];
    store.onCaptureReleased((sessionId) => released.push(sessionId));
    expect(store.keyboardCaptured()).toBe(false);
    store.startCapture("s-1");
    expect(store.keyboardCaptured()).toBe(true);
    expect(store.releaseCapture("s-2")).toBe(false);
    expect(store.releaseCapture("s-1")).toBe(true);
    expect(store.keyboardCaptured()).toBe(false);
    expect(released).toEqual(["s-1"]);
  });

  it("the host release chord ends capture for whoever holds it", () => {
    const store = usePluginTakeoverStore();
    const released: string[] = [];
    store.onCaptureReleased((sessionId) => released.push(sessionId));
    store.startCapture("s-1");
    store.releaseCaptureByHost();
    expect(store.capture).toBeNull();
    expect(released).toEqual(["s-1"]);
  });
});

describe("application menu shortcuts", () => {
  it("are turned off while a plugin holds the keyboard and back on after", () => {
    const store = usePluginTakeoverStore();
    store.startCapture("s-1");
    store.startCapture("s-2");
    expect(menuCalls).toEqual([false]);
    store.releaseCapture("s-2");
    expect(menuCalls).toEqual([false, true]);
    store.releaseCapture("s-2");
    expect(menuCalls).toEqual([false, true]);
  });
});
