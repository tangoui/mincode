import { usePluginTakeoverStore } from "@/stores/plugin-takeover";

/**
 * Ctrl+Alt+Enter (as in Windows Remote Desktop) always belongs to MinCode: while a plugin view holds
 * the keyboard or immersive mode, it gives both back. The keybinding dispatcher calls this first in
 * its capture-phase keydown listener, so the chord works however the page handles keys.
 */
export function isPluginKeyboardReleaseChord(event: KeyboardEvent) {
  return event.key === "Enter" && event.ctrlKey && event.altKey && !event.shiftKey && !event.metaKey;
}

/** Returns true when the chord ended a takeover; the event is then swallowed. */
export function handlePluginKeyboardRelease(event: KeyboardEvent) {
  if (!isPluginKeyboardReleaseChord(event)) return false;
  const takeover = usePluginTakeoverStore();
  if (!takeover.capture && !takeover.immersive) return false;
  event.preventDefault();
  event.stopPropagation();
  void takeover.exitAll();
  return true;
}
