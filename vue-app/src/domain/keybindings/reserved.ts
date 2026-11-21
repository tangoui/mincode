/**
 * Keys the operating system or the WebView owns. `reserved` keys never reach
 * the page (or break the OS when intercepted) and are rejected in presets;
 * `webview` keys are browser accelerators the dispatcher must preventDefault —
 * bindable, but flagged as a warning in the settings UI.
 */
import { sequenceId, parseKeySequence, type KeySequence, type KeyboardPlatform } from "@/domain/keybindings/keys";

export type ReservedKeySeverity = "reserved" | "webview";

export interface ReservedKeyReason {
  severity: ReservedKeySeverity;
  /** Stable reason id for i18n (`keybindings.reserved.<id>` in the settings UI). */
  id: string;
}

const RESERVED: Record<KeyboardPlatform, Record<string, string>> = {
  mac: {
    "cmd+q": "quit",
    "cmd+h": "hide",
    "alt+cmd+h": "hide-others",
    "cmd+tab": "app-switcher",
    "cmd+space": "spotlight",
    "ctrl+space": "input-source",
    "ctrl+left": "spaces",
    "ctrl+right": "spaces",
    "ctrl+up": "mission-control",
    "ctrl+down": "app-expose",
    "ctrl+cmd+q": "lock-screen",
  },
  win: {
    "alt+f4": "close-window",
    "alt+space": "window-menu",
    "ctrl+space": "input-method",
    "alt+tab": "app-switcher",
    "meta+l": "lock-screen",
  },
  linux: {
    "ctrl+alt+l": "lock-screen",
    "ctrl+alt+left": "workspaces",
    "ctrl+alt+right": "workspaces",
    "ctrl+alt+t": "open-terminal",
    "ctrl+alt+delete": "log-out",
    "alt+tab": "app-switcher",
    "alt+f4": "close-window",
  },
};

const WEBVIEW: Record<KeyboardPlatform, Record<string, string>> = {
  mac: {},
  win: {
    f5: "reload",
    "ctrl+r": "reload",
    "ctrl+p": "print",
    "alt+left": "history",
    "alt+right": "history",
    "ctrl+=": "zoom",
    "ctrl+-": "zoom",
    "ctrl+0": "zoom",
    f12: "devtools",
    "ctrl+shift+i": "devtools",
  },
  linux: {
    f5: "reload",
    "ctrl+r": "reload",
    "ctrl+p": "print",
    "alt+left": "history",
    "alt+right": "history",
    "ctrl+=": "zoom",
    "ctrl+-": "zoom",
    "ctrl+0": "zoom",
    f12: "devtools",
    "ctrl+shift+i": "devtools",
  },
};

function index(table: Record<KeyboardPlatform, Record<string, string>>) {
  const result = {} as Record<KeyboardPlatform, Map<string, string>>;
  for (const platform of Object.keys(table) as KeyboardPlatform[]) {
    const map = new Map<string, string>();
    for (const [text, id] of Object.entries(table[platform])) {
      const sequence = parseKeySequence(text, platform);
      if (!sequence) throw new Error(`Invalid reserved key ${text}`);
      map.set(sequenceId(sequence), id);
    }
    result[platform] = map;
  }
  return result;
}

const RESERVED_INDEX = index(RESERVED);
const WEBVIEW_INDEX = index(WEBVIEW);

/** Checks the first chord (the part the OS / WebView would react to). */
export function reservedKeyReason(sequence: KeySequence, platform: KeyboardPlatform): ReservedKeyReason | null {
  if (sequence.kind !== "chords" || !sequence.chords[0]) return null;
  const first = sequenceId({ kind: "chords", chords: [sequence.chords[0]] });
  const reserved = RESERVED_INDEX[platform].get(first);
  if (reserved) return { severity: "reserved", id: reserved };
  const webview = WEBVIEW_INDEX[platform].get(first);
  if (webview) return { severity: "webview", id: webview };
  return null;
}
