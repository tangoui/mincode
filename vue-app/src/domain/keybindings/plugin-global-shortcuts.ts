/**
 * Pure model of plugin global shortcuts (`contributes.globalShortcuts`) on the
 * host side: the backend status list (plugin_global_shortcuts_list), state
 * tags for the settings page, accelerator parsing / display / physical
 * comparison, recorded chord → accelerator, the in-app keys pushed to the
 * backend for the overlap warning and the install preview summary (notice
 * keys live in plugin-global-shortcut-notices.ts). The Go host
 * (backend/plugin/global_shortcuts.go) owns registration, conflicts
 * and settings; nothing here touches Vue or IPC.
 */
import contributionContract from "../plugins/contribution-points.json" with { type: "json" };
import type { MessageKey } from "@/i18n/locales/zh";
import { formatKeySequence, parseKeySequence, type KeyChord, type KeySequence, type KeyboardPlatform, type ModifierName } from "@/domain/keybindings/keys";
import { monacoDefaultBindings } from "@/domain/keybindings/monaco-defaults";

export const GLOBAL_SHORTCUTS_CONTRIBUTION_POINT = "globalShortcuts";

interface GlobalShortcutsContract {
  maxEntries: number;
  limits: { titleMaxRunes: number; localesMax: number; keyMaxLength: number };
  lists: { reservedAccelerators: string[]; reservedDefaultAccelerators: string[] };
}
const contract = (contributionContract as unknown as { points: Record<string, GlobalShortcutsContract> }).points[
  GLOBAL_SHORTCUTS_CONTRIBUTION_POINT
]!;

/** Mirrors `points.globalShortcuts` in plugins/api/contribution-points.json. */
export const GLOBAL_SHORTCUT_LIMITS = {
  maxEntries: contract.maxEntries,
  titleMaxRunes: contract.limits.titleMaxRunes,
  localesMax: contract.limits.localesMax,
  keyMaxLength: contract.limits.keyMaxLength,
} as const;

/** The backend keeps at most this many in-app keys (plugin_global_shortcuts_set_inapp_keys). */
export const GLOBAL_SHORTCUT_IN_APP_KEYS_MAX = 512;

/**
 * Always shown while recording a global shortcut: macOS (Carbon hotkeys) and
 * Windows (RegisterHotKey) deliver a combo that is already registered — by the
 * system, MinCode's own activation shortcut, a plugin or another app — to its
 * owner, so the WebView never sees it and the recorder cannot capture it.
 */
export const GLOBAL_SHORTCUT_RECORDER_REGISTERED_HINT_KEY =
  "settings.keybindings.pluginGlobal.recorderRegisteredHint" satisfies MessageKey;

/**
 * Localized message for a failed set/reset. The Go host's errors are English
 * (global_shortcuts.go): `plugin "x" has no global shortcut "y"` and exactly
 * `global shortcut settings are not available` (the bridge may prefix
 * "Error: "); anything else keeps the raw text.
 */
export function globalShortcutSaveError(message: string): { key: MessageKey; error: string } {
  if (message.includes("has no global shortcut")) return { key: "settings.keybindings.pluginGlobal.error.missing", error: message };
  if (message.replace(/^Error:\s*/, "").trim() === "global shortcut settings are not available") {
    return { key: "settings.keybindings.pluginGlobal.error.unavailable", error: message };
  }
  return { key: "settings.keybindings.pluginGlobal.saveFailed", error: message };
}

function truncateRunes(value: string, max: number): string {
  return Array.from(value).slice(0, max).join("");
}

export const GLOBAL_SHORTCUT_STATES = ["enabled", "disabled", "conflict", "occupied", "unsupported", "inactive"] as const;
export type GlobalShortcutState = (typeof GLOBAL_SHORTCUT_STATES)[number];

export interface GlobalShortcutStatus {
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  /** The manifest's default key. */
  defaultKey: string;
  /** The effective key (custom or default). */
  key: string;
  /** The user's switch. */
  enabled: boolean;
  state: GlobalShortcutState;
  /** "mincode" or "<pluginId>/<id>" of the entry holding the combo. */
  conflictWith?: string;
  /** An in-app binding uses the same physical combo (yellow note only). */
  inAppOverlap?: boolean;
  /** On before a plugin update changed the entry; off until the user turns it on again. */
  changedByUpdate?: boolean;
}

/** The value `conflictWith` carries for MinCode's own global shortcut. */
export const GLOBAL_SHORTCUT_HOST_CONFLICT = "mincode";

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === "object" && !Array.isArray(value));
}

function text(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function titles(value: unknown): Record<string, string> | undefined {
  if (!isRecord(value)) return undefined;
  const out: Record<string, string> = {};
  for (const [locale, title] of Object.entries(value).slice(0, GLOBAL_SHORTCUT_LIMITS.localesMax)) {
    const clean = text(title);
    if (clean && [...clean].length <= GLOBAL_SHORTCUT_LIMITS.titleMaxRunes) out[locale] = clean;
  }
  return Object.keys(out).length ? out : undefined;
}

function parseStatus(value: unknown): GlobalShortcutStatus | undefined {
  if (!isRecord(value)) return undefined;
  const pluginId = text(value.pluginId);
  const id = text(value.id);
  const key = text(value.key);
  const defaultKey = text(value.defaultKey);
  const state = value.state as GlobalShortcutState;
  if (!pluginId || !id || !key || !defaultKey || typeof value.enabled !== "boolean" || !GLOBAL_SHORTCUT_STATES.includes(state)) {
    return undefined;
  }
  const status: GlobalShortcutStatus = {
    pluginId,
    pluginName: text(value.pluginName) || pluginId,
    id,
    title: text(value.title) || id,
    defaultKey,
    key,
    enabled: value.enabled,
    state,
  };
  const localized = titles(value.titleLocalized);
  if (localized) status.titleLocalized = localized;
  const conflictWith = text(value.conflictWith);
  if (conflictWith) status.conflictWith = conflictWith;
  if (value.inAppOverlap === true) status.inAppOverlap = true;
  if (value.changedByUpdate === true) status.changedByUpdate = true;
  return status;
}

/** Validates plugin_global_shortcuts_list; malformed entries are dropped. */
export function parseGlobalShortcutStatuses(value: unknown): GlobalShortcutStatus[] {
  if (!Array.isArray(value)) return [];
  return value.map(parseStatus).filter((status): status is GlobalShortcutStatus => status !== undefined);
}

export function parseGlobalShortcutStatus(value: unknown): GlobalShortcutStatus | undefined {
  return parseStatus(value);
}

export interface GlobalShortcutGroup {
  pluginId: string;
  pluginName: string;
  entries: GlobalShortcutStatus[];
}

/** Groups the list per plugin, keeping the backend order. */
export function groupGlobalShortcuts(statuses: readonly GlobalShortcutStatus[]): GlobalShortcutGroup[] {
  const groups = new Map<string, GlobalShortcutGroup>();
  for (const status of statuses) {
    let group = groups.get(status.pluginId);
    if (!group) {
      group = { pluginId: status.pluginId, pluginName: status.pluginName, entries: [] };
      groups.set(status.pluginId, group);
    }
    group.entries.push(status);
  }
  return [...groups.values()];
}

export interface GlobalShortcutStateTag {
  key: MessageKey;
  type: "success" | "info" | "warning" | "danger";
  /** Conflict partner ("MinCode" or "<plugin> · <title>"). */
  name?: string;
}

const STATE_TAG_TYPES: Record<GlobalShortcutState, GlobalShortcutStateTag["type"]> = {
  enabled: "success",
  disabled: "info",
  conflict: "danger",
  occupied: "warning",
  unsupported: "info",
  inactive: "info",
};

/** The settings row's state tag; `titleOf` localizes another entry's title. */
export function globalShortcutStateTag(
  status: GlobalShortcutStatus,
  statuses: readonly GlobalShortcutStatus[],
  titleOf: (status: GlobalShortcutStatus) => string,
): GlobalShortcutStateTag {
  const tag: GlobalShortcutStateTag = {
    key: `settings.keybindings.pluginGlobal.state.${status.state}` as MessageKey,
    type: STATE_TAG_TYPES[status.state],
  };
  if (status.state === "conflict") {
    const owner = status.conflictWith ?? "";
    if (owner === GLOBAL_SHORTCUT_HOST_CONFLICT) {
      tag.name = "MinCode";
    } else {
      const other = statuses.find((entry) => `${entry.pluginId}/${entry.id}` === owner);
      tag.name = other ? `${other.pluginName} · ${titleOf(other)}` : owner;
    }
  }
  return tag;
}

// ---------------------------------------------------------------------------
// Accelerators (accelerator syntax, platform-neutral)
// ---------------------------------------------------------------------------

type AcceleratorModifier = "cmdOrCtrl" | "ctrl" | "alt" | "shift" | "super";
/** The backend's platform names. */
export type AcceleratorPlatform = "mac" | "windows" | "linux";

const ACCELERATOR_MODIFIERS: Record<string, AcceleratorModifier> = {
  cmdorctrl: "cmdOrCtrl",
  cmd: "cmdOrCtrl",
  command: "cmdOrCtrl",
  ctrl: "ctrl",
  optionoralt: "alt",
  alt: "alt",
  option: "alt",
  shift: "shift",
  super: "super",
};

const ACCELERATOR_NAMED_KEYS = new Set([
  "backspace", "tab", "return", "enter", "escape", "left", "right", "up", "down", "space", "delete",
  "forward delete", "home", "end", "page up", "page down", "numlock",
  ...Array.from({ length: 35 }, (_, index) => `f${index + 1}`),
]);

interface ParsedAccelerator {
  modifiers: AcceleratorModifier[];
  /** Lower case; "+" for Plus; "return" for Enter. */
  key: string;
}

/** Parses an accelerator like the Go host (parseGlobalAccelerator); null when invalid. */
function parseAccelerator(value: string): ParsedAccelerator | null {
  if (typeof value !== "string" || !value || value.length > GLOBAL_SHORTCUT_LIMITS.keyMaxLength || value.trim() !== value) return null;
  const parts = value.split("+");
  const modifiers: AcceleratorModifier[] = [];
  for (const part of parts.slice(0, -1)) {
    const modifier = ACCELERATOR_MODIFIERS[part.toLowerCase()];
    if (!modifier || modifiers.includes(modifier)) return null;
    modifiers.push(modifier);
  }
  const last = parts[parts.length - 1]!.toLowerCase();
  let key: string;
  if (last === "plus") key = "+";
  else if (ACCELERATOR_NAMED_KEYS.has(last)) key = last === "enter" ? "return" : last;
  else if (last.length === 1 && last > " " && last < "\u007f") key = last;
  else return null;
  return { modifiers, key };
}

const PHYSICAL_ORDER = ["Ctrl", "Alt", "Shift", "Cmd", "Win"] as const;

function physicalOf(parsed: ParsedAccelerator, platform: AcceleratorPlatform): string {
  const names = new Set<string>();
  for (const modifier of parsed.modifiers) {
    if (modifier === "cmdOrCtrl") names.add(platform === "mac" ? "Cmd" : "Ctrl");
    else if (modifier === "super") names.add(platform === "mac" ? "Cmd" : "Win");
    else names.add({ ctrl: "Ctrl", alt: "Alt", shift: "Shift" }[modifier]);
  }
  return [...PHYSICAL_ORDER.filter((name) => names.has(name)), parsed.key].join("+");
}

export function acceleratorPlatform(platform: KeyboardPlatform | AcceleratorPlatform): AcceleratorPlatform {
  return platform === "win" ? "windows" : platform;
}

/** The physical chord on a platform ("Shift+Cmd+a"), like the host's conflict check; "" when invalid. */
export function acceleratorPhysical(value: string, platform: KeyboardPlatform | AcceleratorPlatform): string {
  const parsed = parseAccelerator(value);
  return parsed ? physicalOf(parsed, acceleratorPlatform(platform)) : "";
}

/** Two accelerators press the same physical keys on `platform`. */
export function acceleratorsOverlap(left: string, right: string, platform: KeyboardPlatform | AcceleratorPlatform): boolean {
  const physical = acceleratorPhysical(left, platform);
  return physical !== "" && physical === acceleratorPhysical(right, platform);
}

const RESERVED_ALIASES: Record<string, string> = { option: "Alt", super: "Win", del: "delete" };

/** A contract list of "<platform>:<physical chord>" entries as platform → physical chords. */
function physicalTable(entries: readonly string[]): Record<AcceleratorPlatform, Set<string>> {
  const out: Record<AcceleratorPlatform, Set<string>> = { mac: new Set(), windows: new Set(), linux: new Set() };
  for (const entry of entries) {
    const [scope, chord = ""] = entry.split(":");
    const parts = chord.split("+").map((part) => RESERVED_ALIASES[part.toLowerCase()] ?? part);
    const key = parts.pop()!.toLowerCase();
    const modifiers = new Set(parts);
    const physical = [...PHYSICAL_ORDER.filter((name) => modifiers.has(name)), key].join("+");
    const platforms: AcceleratorPlatform[] = scope === "any" ? ["mac", "windows", "linux"] : [scope as AcceleratorPlatform];
    for (const platform of platforms) out[platform]?.add(physical);
  }
  return out;
}

/** Contract reservedAccelerators: system shortcuts, refused for any key. */
const RESERVED = physicalTable(contract.lists.reservedAccelerators);
/** Contract reservedDefaultAccelerators: application-standard combos (copy, save, ...), refused only as manifest defaults. */
const APP_STANDARD = physicalTable(contract.lists.reservedDefaultAccelerators);

function matchesTable(
  parsed: ParsedAccelerator,
  table: Record<AcceleratorPlatform, Set<string>>,
  platforms: readonly AcceleratorPlatform[] = ["mac", "windows", "linux"],
): boolean {
  const forms = parsed.key === "return" ? [parsed, { ...parsed, key: "enter" }] : [parsed];
  return platforms.some((platform) => forms.some((form) => table[platform].has(physicalOf(form, platform))));
}

export type GlobalShortcutKeyIssue = "invalid" | "modifier" | "reserved";

/** Mirrors the host's ValidAccelerator: syntax, a non-Shift modifier, not reserved on any desktop platform. */
export function globalShortcutKeyIssue(value: string): GlobalShortcutKeyIssue | undefined {
  const parsed = parseAccelerator(value);
  if (!parsed) return "invalid";
  if (!parsed.modifiers.some((modifier) => modifier !== "shift")) return "modifier";
  return matchesTable(parsed, RESERVED) ? "reserved" : undefined;
}

/**
 * An application-standard combination (CmdOrCtrl plus C, V, X, Z, A, S, W, Q,
 * F, N, T, O or P with no other modifier) on the current platform. Plugins
 * cannot ship one as a default (the host checks defaults on every desktop
 * platform); a user may choose one, with a warning: once on, the shortcut
 * takes the combo away from every application on this machine.
 */
export function globalShortcutAppStandard(value: string, platform: KeyboardPlatform | AcceleratorPlatform): boolean {
  const parsed = parseAccelerator(value);
  return parsed !== null && matchesTable(parsed, APP_STANDARD, [acceleratorPlatform(platform)]);
}

// Accelerator key ↔ KeyboardEvent.code

const KEY_TO_CODE: Record<string, string> = {
  backspace: "Backspace", tab: "Tab", return: "Enter", escape: "Escape", left: "ArrowLeft", right: "ArrowRight",
  up: "ArrowUp", down: "ArrowDown", space: "Space", delete: "Delete", "forward delete": "Delete", home: "Home",
  end: "End", "page up": "PageUp", "page down": "PageDown", numlock: "NumLock",
  "/": "Slash", "\\": "Backslash", ".": "Period", ",": "Comma", ";": "Semicolon", "'": "Quote", "`": "Backquote",
  "[": "BracketLeft", "]": "BracketRight", "-": "Minus", "=": "Equal",
};

const CODE_TO_KEY: Record<string, string> = {
  Backspace: "Backspace", Tab: "Tab", Enter: "Enter", Escape: "Escape", ArrowLeft: "Left", ArrowRight: "Right",
  ArrowUp: "Up", ArrowDown: "Down", Space: "Space", Delete: "Delete", Home: "Home", End: "End",
  PageUp: "Page Up", PageDown: "Page Down",
  Slash: "/", Backslash: "\\", Period: ".", Comma: ",", Semicolon: ";", Quote: "'", Backquote: "`",
  BracketLeft: "[", BracketRight: "]", Minus: "-", Equal: "=",
};

function keyToCode(key: string): string | undefined {
  if (/^[a-z]$/.test(key)) return `Key${key.toUpperCase()}`;
  if (/^[0-9]$/.test(key)) return `Digit${key}`;
  if (/^f([1-9]|[12][0-9]|3[0-5])$/.test(key)) return key.toUpperCase();
  return KEY_TO_CODE[key];
}

function codeToKey(code: string): string | undefined {
  const letter = /^Key([A-Z])$/.exec(code);
  if (letter) return letter[1];
  const digit = /^Digit([0-9])$/.exec(code);
  if (digit) return digit[1];
  if (/^F([1-9]|1[0-9]|2[0-4])$/.test(code)) return code;
  return CODE_TO_KEY[code];
}

/** The accelerator as an in-app key sequence on `platform` (for display); null when not expressible. */
export function acceleratorToKeySequence(value: string, platform: KeyboardPlatform): KeySequence | null {
  const parsed = parseAccelerator(value);
  const code = parsed ? keyToCode(parsed.key) : undefined;
  if (!parsed || !code) return null;
  const chord: KeyChord = { ctrl: false, shift: false, alt: false, meta: false, code };
  for (const modifier of parsed.modifiers) {
    const name: ModifierName =
      modifier === "cmdOrCtrl" ? (platform === "mac" ? "meta" : "ctrl") : modifier === "super" ? "meta" : modifier;
    chord[name] = true;
  }
  return { kind: "chords", chords: [chord] };
}

/**
 * Displays an accelerator with the keybinding formatter used by the rest of the
 * keybinding UI ("⇧⌘A", "Ctrl+Shift+A"); keys it cannot express keep the raw text.
 */
export function formatAccelerator(value: string, platform: KeyboardPlatform): string {
  const sequence = acceleratorToKeySequence(value, platform);
  return sequence ? formatKeySequence(sequence, platform) : value;
}

/**
 * A recorded chord as an accelerator. The platform's primary modifier (⌘ on
 * macOS, Ctrl elsewhere) becomes CmdOrCtrl like manifest defaults; macOS Ctrl
 * stays Ctrl and the Windows/Super key becomes Super. Null for keys the host has no
 * name for (Insert, numpad operators, ...).
 */
export function chordToAccelerator(chord: KeyChord, platform: KeyboardPlatform): string | null {
  const key = codeToKey(chord.code);
  if (!key) return null;
  const primary: ModifierName = platform === "mac" ? "meta" : "ctrl";
  const parts: string[] = [];
  if (chord[primary]) parts.push("CmdOrCtrl");
  if (platform === "mac" && chord.ctrl) parts.push("Ctrl");
  if (chord.alt) parts.push("Alt");
  if (chord.shift) parts.push("Shift");
  if (platform !== "mac" && chord.meta) parts.push("Super");
  parts.push(key);
  return parts.join("+");
}

/**
 * In-app bindings as accelerators for plugin_global_shortcuts_set_inapp_keys:
 * the first chord of each chord binding that has a non-Shift modifier (a global
 * shortcut always has one; a chord sequence starts with its first chord),
 * deduplicated and capped at the backend limit. Double-modifier bindings and
 * keys without an accelerator name are skipped.
 */
export function inAppAccelerators(
  bindings: readonly { sequence: KeySequence }[],
  platform: KeyboardPlatform,
): string[] {
  const keys = new Set<string>();
  for (const binding of bindings) {
    if (keys.size >= GLOBAL_SHORTCUT_IN_APP_KEYS_MAX) break;
    if (binding.sequence.kind !== "chords") continue;
    const first = binding.sequence.chords[0];
    if (!first || !(first.ctrl || first.alt || first.meta)) continue;
    const accelerator = chordToAccelerator(first, platform);
    if (accelerator) keys.add(accelerator);
  }
  return [...keys];
}

/**
 * The in-app keys pushed for the overlap note: the resolved bindings plus
 * Monaco's own default editor keys (monaco-defaults; Monaco registers them
 * itself, so not every one is a resolved binding).
 */
export function globalShortcutInAppKeys(
  bindings: readonly { sequence: KeySequence }[],
  platform: KeyboardPlatform,
): string[] {
  const monaco = monacoDefaultBindings(platform).flatMap((binding) => {
    const sequence = parseKeySequence(binding.key, platform);
    return sequence ? [{ sequence }] : [];
  });
  return inAppAccelerators([...bindings, ...monaco], platform);
}

export interface GlobalShortcutCandidate {
  /** The accelerator to save; absent when the chord has no accelerator name. */
  key?: string;
  issue?: GlobalShortcutKeyIssue;
  /** Pressing it inside MinCode also triggers an in-app binding. */
  inAppOverlap: boolean;
  /** An application-standard combo (copy, save, ...): allowed, with a warning. */
  appStandard?: boolean;
}

/**
 * Live analysis of a chord recorded for a plugin global shortcut. There is no
 * "same as MinCode's activation shortcut" check: a registered combo never
 * reaches the recorder on macOS / Windows (see the registered-combo hint), and
 * the backend reports any such conflict after saving.
 */
export function analyzeGlobalShortcutCandidate(input: {
  chord: KeyChord;
  platform: KeyboardPlatform;
  inAppKeys: readonly string[];
}): GlobalShortcutCandidate {
  const key = chordToAccelerator(input.chord, input.platform);
  if (!key) return { issue: "invalid", inAppOverlap: false };
  const result: GlobalShortcutCandidate = {
    key,
    inAppOverlap: input.inAppKeys.some((other) => acceleratorsOverlap(key, other, input.platform)),
  };
  const issue = globalShortcutKeyIssue(key);
  if (issue) result.issue = issue;
  else if (globalShortcutAppStandard(key, input.platform)) result.appStandard = true;
  return result;
}

// ---------------------------------------------------------------------------
// Install preview and notices
// ---------------------------------------------------------------------------

export interface GlobalShortcutPreviewEntry {
  /** The manifest entry id ("" when missing). */
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  /** The manifest default key (accelerator). */
  key: string;
}

/** Install preview: each declared shortcut with its default key (all start switched off). */
export function globalShortcutPreviewSummary(contributes: unknown): GlobalShortcutPreviewEntry[] {
  const entries = isRecord(contributes) ? contributes[GLOBAL_SHORTCUTS_CONTRIBUTION_POINT] : undefined;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, GLOBAL_SHORTCUT_LIMITS.maxEntries).flatMap((entry) => {
    if (!isRecord(entry)) return [];
    const id = truncateRunes(text(entry.id), 32);
    const title = truncateRunes(text(entry.title), GLOBAL_SHORTCUT_LIMITS.titleMaxRunes) || id;
    const key = text(entry.key).slice(0, GLOBAL_SHORTCUT_LIMITS.keyMaxLength);
    if (!title || !key) return [];
    const localized = titles(entry.titleLocalized);
    return [localized ? { id, title, titleLocalized: localized, key } : { id, title, key }];
  });
}

/**
 * Update preview: the previewed entries the backend reports as on now and
 * changed by this package (InstallPreview.globalShortcutsChanged), which the
 * update turns off.
 */
export function globalShortcutsChangedByUpdate(
  entries: readonly GlobalShortcutPreviewEntry[],
  changedIds: readonly string[] | undefined,
): GlobalShortcutPreviewEntry[] {
  if (!changedIds?.length) return [];
  const changed = new Set(changedIds);
  return entries.filter((entry) => entry.id && changed.has(entry.id));
}

export { globalShortcutNoticeKey } from "@/domain/keybindings/plugin-global-shortcut-notices";
