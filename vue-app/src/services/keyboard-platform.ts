import { keyboardPlatformFrom, type KeyboardPlatform } from "@/domain/keybindings/keys";

let cached: KeyboardPlatform | undefined;

/** Keyboard platform of the running WebView (⌘ on macOS, Ctrl on Windows/Linux). */
export function currentKeyboardPlatform(): KeyboardPlatform {
  if (cached) return cached;
  if (typeof navigator === "undefined") return "linux";
  const userAgentData = (navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData;
  cached = keyboardPlatformFrom(userAgentData?.platform || navigator.platform || navigator.userAgent);
  return cached;
}
