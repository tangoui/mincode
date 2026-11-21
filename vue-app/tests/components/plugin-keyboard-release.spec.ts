import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { handlePluginKeyboardRelease, isPluginKeyboardReleaseChord } from "@/services/plugin-keyboard-release";
import { setPluginWindowFullscreen, usePluginTakeoverStore } from "@/stores/plugin-takeover";
// Static imports: loading the dispatcher and keybindings store inside the test counted against its 5 s timeout and
// timed out under a loaded full run.
import { installKeybindingDispatcher } from "@/services/keybinding-dispatcher";
import { registerCommand } from "@/services/command-registry";
import { useKeybindingsStore } from "@/stores/keybindings";

const key = (init: KeyboardEventInit) => new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });

describe("plugin keyboard release chord", () => {
  it("is Ctrl+Alt+Enter only", () => {
    expect(isPluginKeyboardReleaseChord(key({ key: "Enter", ctrlKey: true, altKey: true }))).toBe(true);
    expect(isPluginKeyboardReleaseChord(key({ key: "Enter", ctrlKey: true }))).toBe(false);
    expect(isPluginKeyboardReleaseChord(key({ key: "Enter", ctrlKey: true, altKey: true, shiftKey: true }))).toBe(false);
    expect(isPluginKeyboardReleaseChord(key({ key: "Enter", metaKey: true, altKey: true }))).toBe(false);
    expect(isPluginKeyboardReleaseChord(key({ key: "k", ctrlKey: true, altKey: true }))).toBe(false);
  });
});

describe("plugin keyboard release handling", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    setPluginWindowFullscreen(async () => undefined);
  });

  it("ends capture and immersive mode, and swallows the chord", async () => {
    const store = usePluginTakeoverStore();
    await store.enterImmersive("s-1");
    store.startCapture("s-1");
    const event = key({ key: "Enter", ctrlKey: true, altKey: true });
    expect(handlePluginKeyboardRelease(event)).toBe(true);
    await Promise.resolve();
    expect(event.defaultPrevented).toBe(true);
    expect(store.capture).toBeNull();
    expect(store.immersive).toBeNull();
  });

  it("leaves the chord alone when nothing is taken over", () => {
    const event = key({ key: "Enter", ctrlKey: true, altKey: true });
    expect(handlePluginKeyboardRelease(event)).toBe(false);
    expect(event.defaultPrevented).toBe(false);
  });
});

describe("keybinding dispatcher while a plugin holds the keyboard", () => {
  it("suspends MinCode shortcuts and resumes after the release chord", async () => {
    const store = usePluginTakeoverStore();
    let runs = 0;
    const unregister = registerCommand({ id: "builtin:command-palette", source: "builtin", title: "Palette", execute: () => { runs += 1; } });
    const uninstall = installKeybindingDispatcher();
    try {
      const mac = useKeybindingsStore().platform === "mac";
      const palette = () => key({ key: "k", code: "KeyK", metaKey: mac, ctrlKey: !mac });
      document.body.dispatchEvent(palette());
      await Promise.resolve();
      expect(runs).toBe(1);

      store.startCapture("s-1");
      document.body.dispatchEvent(palette());
      await Promise.resolve();
      expect(runs).toBe(1);

      const chord = key({ key: "Enter", code: "Enter", ctrlKey: true, altKey: true });
      document.body.dispatchEvent(chord);
      await Promise.resolve();
      expect(chord.defaultPrevented).toBe(true);
      expect(store.capture).toBeNull();

      document.body.dispatchEvent(palette());
      await Promise.resolve();
      expect(runs).toBe(2);
    } finally {
      uninstall();
      unregister();
    }
  });
});
