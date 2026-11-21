import { acceptHMRUpdate, defineStore } from "pinia";
import { ref } from "vue";
import { Window } from "@/services/desktop-window";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

/**
 * A plugin view can temporarily take over parts of the host (features.immersive and
 * features.keyboardCapture): immersive mode hides the MinCode chrome and makes the window
 * fullscreen; keyboard capture pauses MinCode shortcuts so key combinations reach the page.
 * At most one session holds each at a time. The host can always end both (exitAll, the
 * release chord), and they end when the session goes away (releaseSession).
 */

type WindowFullscreen = (on: boolean) => Promise<void>;

let windowFullscreen: WindowFullscreen = async (on) => {
  if (!isDesktopRuntime()) return;
  await (on ? Window.Fullscreen() : Window.UnFullscreen());
};

/** Replaces the window fullscreen call (tests). */
export function setPluginWindowFullscreen(next: WindowFullscreen) {
  windowFullscreen = next;
}

type MenuAccelerators = (enabled: boolean) => Promise<void>;

// macOS application-menu shortcuts (such as Cmd+, for Settings) fire before the page sees the key.
let menuAccelerators: MenuAccelerators = async (enabled) => {
  if (!isDesktopRuntime()) return;
  await invokeBackend("desktop_set_menu_accelerators", { enabled });
};

/** Replaces the application-menu shortcut toggle (tests). */
export function setPluginMenuAccelerators(next: MenuAccelerators) {
  menuAccelerators = next;
}

function toggleMenuAccelerators(enabled: boolean) {
  // A menu that keeps its shortcuts only costs the page those few chords.
  void menuAccelerators(enabled).catch(() => undefined);
}

export const usePluginTakeoverStore = defineStore("plugin-takeover", () => {
  const immersive = ref<{ sessionId: string } | null>(null);
  const capture = ref<{ sessionId: string } | null>(null);
  const immersiveListeners = new Set<(sessionId: string, active: boolean) => void>();
  const captureListeners = new Set<(sessionId: string) => void>();

  function notifyImmersive(sessionId: string, active: boolean) {
    for (const listener of [...immersiveListeners]) listener(sessionId, active);
  }

  async function setWindowFullscreen(on: boolean) {
    try {
      await windowFullscreen(on);
    } catch {
      // The chrome is hidden either way; a window that cannot go fullscreen only loses screen space.
    }
  }

  async function enterImmersive(sessionId: string) {
    const previous = immersive.value;
    if (previous?.sessionId === sessionId) return;
    immersive.value = { sessionId };
    if (previous) notifyImmersive(previous.sessionId, false);
    notifyImmersive(sessionId, true);
    if (!previous) await setWindowFullscreen(true);
  }

  async function endImmersive() {
    const current = immersive.value;
    if (!current) return;
    immersive.value = null;
    notifyImmersive(current.sessionId, false);
    await setWindowFullscreen(false);
  }

  /** Only the session that entered immersive mode may leave it this way. */
  async function exitImmersive(sessionId: string) {
    if (immersive.value?.sessionId !== sessionId) return false;
    await endImmersive();
    return true;
  }

  function startCapture(sessionId: string) {
    const previous = capture.value;
    if (previous?.sessionId === sessionId) return;
    capture.value = { sessionId };
    if (previous) for (const listener of [...captureListeners]) listener(previous.sessionId);
    else toggleMenuAccelerators(false);
  }

  function endCapture() {
    const current = capture.value;
    if (!current) return;
    capture.value = null;
    toggleMenuAccelerators(true);
    for (const listener of [...captureListeners]) listener(current.sessionId);
  }

  function releaseCapture(sessionId: string) {
    if (capture.value?.sessionId !== sessionId) return false;
    endCapture();
    return true;
  }

  /** The host release chord: ends capture for whichever session holds it. */
  function releaseCaptureByHost() {
    endCapture();
  }

  /** The host exit control: ends immersive mode and keyboard capture. */
  async function exitAll() {
    endCapture();
    await endImmersive();
  }

  /** The session closed, was revoked or hidden: drop whatever it held. */
  async function releaseSession(sessionId: string) {
    if (capture.value?.sessionId === sessionId) endCapture();
    if (immersive.value?.sessionId === sessionId) await endImmersive();
  }

  function keyboardCaptured() {
    return capture.value !== null;
  }

  function onImmersiveChange(listener: (sessionId: string, active: boolean) => void) {
    immersiveListeners.add(listener);
    return () => { immersiveListeners.delete(listener); };
  }

  function onCaptureReleased(listener: (sessionId: string) => void) {
    captureListeners.add(listener);
    return () => { captureListeners.delete(listener); };
  }

  return {
    immersive, capture,
    enterImmersive, exitImmersive, exitAll,
    startCapture, releaseCapture, releaseCaptureByHost, keyboardCaptured,
    releaseSession, onImmersiveChange, onCaptureReleased,
  };
});

if (import.meta.hot) import.meta.hot.accept(acceptHMRUpdate(usePluginTakeoverStore, import.meta.hot));
