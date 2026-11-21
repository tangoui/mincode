/**
 * Keyboard sequence model for configurable keybindings.
 *
 * Bindings are written as text ("mod+shift+p", chords "cmd+k cmd+s", double
 * modifier "shift shift") and normalized to physical keys (`KeyboardEvent.code`)
 * so ⌥-letters on macOS (which produce "¬", "∂", ...) and non-US layouts still
 * resolve to the printed key position. `mod` expands to ⌘ on macOS and Ctrl on
 * Windows/Linux. Everything here is pure and platform-injected for tests.
 */

export type KeyboardPlatform = "mac" | "win" | "linux";
export type ModifierName = "ctrl" | "shift" | "alt" | "meta";

export interface KeyChord {
  ctrl: boolean;
  shift: boolean;
  alt: boolean;
  meta: boolean;
  /** Normalized `KeyboardEvent.code`, e.g. "KeyK", "Digit1", "ArrowUp", "F12". */
  code: string;
}

export type KeySequence =
  | { kind: "chords"; chords: KeyChord[] }
  | { kind: "double"; modifier: ModifierName };

/** Longest chord sequence accepted (VS Code supports two; we allow a small margin). */
export const MAX_KEY_CHORDS = 2;
export const MAX_KEY_TEXT_LENGTH = 64;

export interface KeyboardEventLike {
  key: string;
  code?: string;
  ctrlKey: boolean;
  shiftKey: boolean;
  altKey: boolean;
  metaKey: boolean;
}

const MODIFIER_ALIASES: Record<string, ModifierName | "mod"> = {
  mod: "mod",
  cmdorctrl: "mod",
  ctrl: "ctrl",
  control: "ctrl",
  shift: "shift",
  alt: "alt",
  option: "alt",
  opt: "alt",
  meta: "meta",
  cmd: "meta",
  command: "meta",
  win: "meta",
  super: "meta",
};

const NAMED_KEYS: Record<string, string> = {
  up: "ArrowUp",
  down: "ArrowDown",
  left: "ArrowLeft",
  right: "ArrowRight",
  enter: "Enter",
  return: "Enter",
  escape: "Escape",
  esc: "Escape",
  tab: "Tab",
  space: "Space",
  backspace: "Backspace",
  delete: "Delete",
  del: "Delete",
  insert: "Insert",
  home: "Home",
  end: "End",
  pageup: "PageUp",
  pagedown: "PageDown",
  "/": "Slash",
  "\\": "Backslash",
  ".": "Period",
  ",": "Comma",
  ";": "Semicolon",
  "'": "Quote",
  "`": "Backquote",
  "[": "BracketLeft",
  "]": "BracketRight",
  "-": "Minus",
  "=": "Equal",
};

const CODE_TOKENS: Record<string, string> = Object.fromEntries(
  Object.entries(NAMED_KEYS)
    // Canonical token per code: the first alias listed above wins.
    .filter(([token, code], index, all) => all.findIndex(([, other]) => other === code) === index && token)
    .map(([token, code]) => [code, token]),
);

/** Codes that are aliases of a canonical code (numpad, left/right variants). */
const CODE_ALIASES: Record<string, string> = {
  NumpadEnter: "Enter",
  NumpadDivide: "Slash",
  NumpadDecimal: "Period",
  NumpadSubtract: "Minus",
  NumpadEqual: "Equal",
  IntlBackslash: "Backquote",
};

const MODIFIER_CODES = new Set([
  "ShiftLeft", "ShiftRight", "ControlLeft", "ControlRight", "AltLeft", "AltRight",
  "MetaLeft", "MetaRight", "OSLeft", "OSRight", "CapsLock", "Fn", "FnLock",
]);

export function keyboardPlatformFrom(platform: string | undefined | null): KeyboardPlatform {
  const value = (platform ?? "").toLowerCase();
  if (/mac|iphone|ipad|ipod|darwin/.test(value)) return "mac";
  if (/win/.test(value)) return "win";
  return "linux";
}

function tokenToCode(token: string): string | null {
  const lower = token.toLowerCase();
  if (/^[a-z]$/.test(lower)) return `Key${lower.toUpperCase()}`;
  if (/^[0-9]$/.test(lower)) return `Digit${lower}`;
  const fn = /^f([1-9]|1[0-9]|2[0-4])$/.exec(lower);
  if (fn) return `F${fn[1]}`;
  return NAMED_KEYS[lower] ?? null;
}

export function normalizeKeyCode(code: string): string {
  if (CODE_ALIASES[code]) return CODE_ALIASES[code];
  const numpadDigit = /^Numpad([0-9])$/.exec(code);
  if (numpadDigit) return `Digit${numpadDigit[1]}`;
  return code;
}

/** Canonical lower-case token for a code ("KeyK" → "k", "ArrowUp" → "up"). */
export function codeToToken(code: string): string {
  const letter = /^Key([A-Z])$/.exec(code);
  if (letter) return letter[1]!.toLowerCase();
  const digit = /^Digit([0-9])$/.exec(code);
  if (digit) return digit[1]!;
  if (/^F([1-9]|1[0-9]|2[0-4])$/.test(code)) return code.toLowerCase();
  return CODE_TOKENS[code] ?? code.toLowerCase();
}

function parseChord(text: string, platform: KeyboardPlatform): KeyChord | null {
  // A trailing "+" is not a supported key; "=" is spelled "=".
  const parts = text.split("+");
  if (parts.some((part) => part === "")) return null;
  const chord: KeyChord = { ctrl: false, shift: false, alt: false, meta: false, code: "" };
  for (let index = 0; index < parts.length; index++) {
    const token = parts[index]!.toLowerCase();
    const modifier = MODIFIER_ALIASES[token];
    if (index < parts.length - 1) {
      if (!modifier) return null;
      const name: ModifierName = modifier === "mod" ? (platform === "mac" ? "meta" : "ctrl") : modifier;
      if (chord[name]) return null;
      chord[name] = true;
      continue;
    }
    const code = tokenToCode(token);
    if (!code) return null;
    chord.code = code;
  }
  return chord.code ? chord : null;
}

/**
 * Parses binding text for a platform. Returns null for invalid text so callers
 * can report the rule instead of throwing.
 */
export function parseKeySequence(text: string, platform: KeyboardPlatform): KeySequence | null {
  if (typeof text !== "string") return null;
  const trimmed = text.trim();
  if (!trimmed || trimmed.length > MAX_KEY_TEXT_LENGTH) return null;
  const parts = trimmed.split(/\s+/);
  if (parts.length === 2) {
    const first = MODIFIER_ALIASES[parts[0]!.toLowerCase()];
    const second = MODIFIER_ALIASES[parts[1]!.toLowerCase()];
    if (first && second) {
      if (first !== second) return null;
      const modifier: ModifierName = first === "mod" ? (platform === "mac" ? "meta" : "ctrl") : first;
      return { kind: "double", modifier };
    }
  }
  if (parts.length > MAX_KEY_CHORDS) return null;
  const chords: KeyChord[] = [];
  for (const part of parts) {
    const chord = parseChord(part, platform);
    if (!chord) return null;
    chords.push(chord);
  }
  return { kind: "chords", chords };
}

export function chordId(chord: KeyChord): string {
  const parts: string[] = [];
  if (chord.ctrl) parts.push("ctrl");
  if (chord.shift) parts.push("shift");
  if (chord.alt) parts.push("alt");
  if (chord.meta) parts.push("meta");
  parts.push(codeToToken(chord.code));
  return parts.join("+");
}

/** Canonical, platform-independent text ("ctrl+shift+p", "meta+k meta+s", "shift shift"). */
export function sequenceId(sequence: KeySequence): string {
  if (sequence.kind === "double") return `${sequence.modifier} ${sequence.modifier}`;
  return sequence.chords.map(chordId).join(" ");
}

export function chordsEqual(left: KeyChord, right: KeyChord): boolean {
  return (
    left.code === right.code &&
    left.ctrl === right.ctrl &&
    left.shift === right.shift &&
    left.alt === right.alt &&
    left.meta === right.meta
  );
}

export function isModifierCode(code: string): boolean {
  return MODIFIER_CODES.has(code);
}

/** Modifier named by a modifier key code ("ShiftLeft" → "shift"). */
export function modifierFromCode(code: string): ModifierName | null {
  if (code.startsWith("Shift")) return "shift";
  if (code.startsWith("Control")) return "ctrl";
  if (code.startsWith("Alt")) return "alt";
  if (code.startsWith("Meta") || code.startsWith("OS")) return "meta";
  return null;
}

function codeFromKey(key: string): string | null {
  if (!key || key === "Unidentified" || key === "Dead" || key === "Process") return null;
  if (key.length === 1) return tokenToCode(key === " " ? "space" : key);
  return tokenToCode(key.replace(/^Arrow/, ""));
}

/**
 * Converts a keyboard event into a chord. Returns null for bare modifier
 * presses and keys without a physical code we understand.
 */
export function keyChordFromEvent(event: KeyboardEventLike): KeyChord | null {
  const rawCode = event.code ?? "";
  if (rawCode && isModifierCode(rawCode)) return null;
  const code = rawCode ? normalizeKeyCode(rawCode) : codeFromKey(event.key);
  if (!code || isModifierCode(code)) return null;
  return {
    ctrl: event.ctrlKey,
    shift: event.shiftKey,
    alt: event.altKey,
    meta: event.metaKey,
    code,
  };
}

const MAC_KEY_LABELS: Record<string, string> = {
  ArrowUp: "↑",
  ArrowDown: "↓",
  ArrowLeft: "←",
  ArrowRight: "→",
  Enter: "↩",
  Backspace: "⌫",
  Delete: "⌦",
  Escape: "⎋",
  Tab: "⇥",
  Space: "␣",
  PageUp: "⇞",
  PageDown: "⇟",
  Home: "↖",
  End: "↘",
};

const TEXT_KEY_LABELS: Record<string, string> = {
  ArrowUp: "Up",
  ArrowDown: "Down",
  ArrowLeft: "Left",
  ArrowRight: "Right",
  Escape: "Esc",
  PageUp: "PageUp",
  PageDown: "PageDown",
};

const PUNCTUATION_LABELS: Record<string, string> = {
  Slash: "/",
  Backslash: "\\",
  Period: ".",
  Comma: ",",
  Semicolon: ";",
  Quote: "'",
  Backquote: "`",
  BracketLeft: "[",
  BracketRight: "]",
  Minus: "-",
  Equal: "=",
};

export function formatKeyCode(code: string, platform: KeyboardPlatform): string {
  const letter = /^Key([A-Z])$/.exec(code);
  if (letter) return letter[1]!;
  const digit = /^Digit([0-9])$/.exec(code);
  if (digit) return digit[1]!;
  if (PUNCTUATION_LABELS[code]) return PUNCTUATION_LABELS[code];
  if (platform === "mac" && MAC_KEY_LABELS[code]) return MAC_KEY_LABELS[code];
  return TEXT_KEY_LABELS[code] ?? code;
}

const MAC_MODIFIER_GLYPHS: Record<ModifierName, string> = { ctrl: "⌃", alt: "⌥", shift: "⇧", meta: "⌘" };

function textModifierLabel(modifier: ModifierName, platform: KeyboardPlatform): string {
  if (modifier === "meta") return platform === "win" ? "Win" : "Super";
  return { ctrl: "Ctrl", shift: "Shift", alt: "Alt" }[modifier];
}

export function formatKeyChord(chord: KeyChord, platform: KeyboardPlatform): string {
  const key = formatKeyCode(chord.code, platform);
  if (platform === "mac") {
    // Apple order: ⌃ ⌥ ⇧ ⌘
    const glyphs = (["ctrl", "alt", "shift", "meta"] as const)
      .filter((modifier) => chord[modifier])
      .map((modifier) => MAC_MODIFIER_GLYPHS[modifier])
      .join("");
    return `${glyphs}${key}`;
  }
  // VS Code order on Windows/Linux: Ctrl+Shift+Alt+Win
  const modifiers = (["ctrl", "shift", "alt", "meta"] as const)
    .filter((modifier) => chord[modifier])
    .map((modifier) => textModifierLabel(modifier, platform));
  return [...modifiers, key].join("+");
}

/** Human label: "⇧⌘P", "⌘K ⌘S", "⇧⇧" on macOS; "Ctrl+Shift+P", "Ctrl+K Ctrl+S", "Shift Shift" elsewhere. */
export function formatKeySequence(sequence: KeySequence, platform: KeyboardPlatform): string {
  if (sequence.kind === "double") {
    if (platform === "mac") return MAC_MODIFIER_GLYPHS[sequence.modifier].repeat(2);
    const label = textModifierLabel(sequence.modifier, platform);
    return `${label} ${label}`;
  }
  return sequence.chords.map((chord) => formatKeyChord(chord, platform)).join(" ");
}

/** Formats binding text directly; returns the raw text when it cannot be parsed. */
export function formatKeyText(text: string, platform: KeyboardPlatform): string {
  const sequence = parseKeySequence(text, platform);
  return sequence ? formatKeySequence(sequence, platform) : text;
}

/** True when the chord uses the platform's primary modifier (⌘ on macOS, Ctrl elsewhere) only. */
export function isPrimaryModifierChord(chord: KeyChord, platform: KeyboardPlatform): boolean {
  return platform === "mac"
    ? chord.meta && !chord.ctrl && !chord.alt
    : chord.ctrl && !chord.meta && !chord.alt;
}

/**
 * Terminal find key (⌘F on macOS, Ctrl+F on Windows/Linux, no Shift/Alt). Used by
 * the terminal key handler until phase 4 moves terminal keys to the keymap.
 */
export function isTerminalFindKey(event: KeyboardEventLike, platform: KeyboardPlatform): boolean {
  const chord = keyChordFromEvent(event);
  return Boolean(chord && chord.code === "KeyF" && !chord.shift && isPrimaryModifierChord(chord, platform));
}
