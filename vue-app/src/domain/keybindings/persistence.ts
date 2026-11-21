/**
 * Persisted keybinding settings (global key `app_keybindings_v1`): the chosen
 * preset id, user overrides and options. Pure validation and the idempotent
 * migration of the legacy command-palette shortcut live here so the store
 * stays thin and the rules are testable without Pinia.
 */
import { parseWhenExpression } from "@/domain/commands/when";
import { isBindableCommandId } from "@/domain/keybindings/catalog";
import { parseKeySequence, type KeyboardPlatform } from "@/domain/keybindings/keys";
import { KEYBOARD_PLATFORMS, MINCODE_PRESET_ID, type KeymapPresetRule } from "@/domain/keybindings/preset";

export const KEYBINDINGS_STORAGE_KEY = "app_keybindings_v1";
export const MAX_KEYBINDING_OVERRIDES = 500;
const MAX_PRESET_ID_LENGTH = 64;

export interface KeybindingOverride {
  command: string;
  /** Omitted only for removals of every binding of `command`. */
  key?: string;
  when?: string;
  /** Removes the matching preset/default binding instead of adding one. */
  remove?: boolean;
  platform?: KeyboardPlatform | KeyboardPlatform[];
}

export interface KeybindingOptions {
  /** Double-modifier bindings (Shift Shift); undefined follows the preset default. */
  doubleModifier?: boolean;
  /** Phase 4: send bound keys to the shell while the terminal has focus. */
  sendKeybindingsToShell: boolean;
}

export interface PersistedKeybindings {
  version: 1;
  preset: string;
  overrides: KeybindingOverride[];
  options: KeybindingOptions;
  migrations: { paletteShortcut?: true };
}

export function defaultPersistedKeybindings(): PersistedKeybindings {
  return {
    version: 1,
    preset: MINCODE_PRESET_ID,
    overrides: [],
    options: { sendKeybindingsToShell: false },
    migrations: {},
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function normalizePlatform(value: unknown): KeyboardPlatform | KeyboardPlatform[] | undefined | null {
  if (value === undefined) return undefined;
  const isPlatform = (item: unknown): item is KeyboardPlatform =>
    typeof item === "string" && (KEYBOARD_PLATFORMS as readonly string[]).includes(item);
  if (isPlatform(value)) return value;
  if (Array.isArray(value) && value.length > 0 && value.every(isPlatform)) return [...new Set(value)];
  return null;
}

function platformsOf(platform: KeyboardPlatform | KeyboardPlatform[] | undefined): readonly KeyboardPlatform[] {
  if (!platform) return KEYBOARD_PLATFORMS;
  return Array.isArray(platform) ? platform : [platform];
}

/** Validates one stored override; returns null when it cannot be used. */
export function normalizeKeybindingOverride(value: unknown): KeybindingOverride | null {
  if (!isRecord(value)) return null;
  const command = typeof value.command === "string" ? value.command.trim() : "";
  if (!isBindableCommandId(command)) return null;
  const remove = value.remove === true;
  const platform = normalizePlatform(value.platform);
  if (platform === null) return null;
  const override: KeybindingOverride = { command };
  if (value.key !== undefined) {
    if (typeof value.key !== "string" || !value.key.trim()) return null;
    const key = value.key.trim();
    if (!platformsOf(platform).some((item) => parseKeySequence(key, item))) return null;
    override.key = key;
  } else if (!remove) {
    return null;
  }
  if (value.when !== undefined) {
    if (typeof value.when !== "string") return null;
    const when = value.when.trim();
    if (when) {
      try {
        parseWhenExpression(when);
      } catch {
        return null;
      }
      override.when = when;
    }
  }
  if (remove) override.remove = true;
  if (platform !== undefined) override.platform = platform;
  return override;
}

/**
 * The plugin that provides a missing preset, by the `<pluginId>.` namespace
 * (for the "install from marketplace" notice). Undefined for MinCode.
 */
export function presetPluginId(presetId: string): string | undefined {
  const dot = presetId.indexOf(".");
  return dot > 0 ? presetId.slice(0, dot) : undefined;
}

/** Validates the stored document; invalid overrides are dropped and counted. */
export function normalizePersistedKeybindings(value: unknown): { state: PersistedKeybindings; dropped: number } {
  const state = defaultPersistedKeybindings();
  if (!isRecord(value) || value.version !== 1) return { state, dropped: 0 };
  if (typeof value.preset === "string" && value.preset.trim() && value.preset.length <= MAX_PRESET_ID_LENGTH) {
    state.preset = value.preset.trim();
  }
  let dropped = 0;
  if (Array.isArray(value.overrides)) {
    for (const raw of value.overrides) {
      const override = state.overrides.length < MAX_KEYBINDING_OVERRIDES ? normalizeKeybindingOverride(raw) : null;
      if (override) state.overrides.push(override);
      else dropped++;
    }
  }
  if (isRecord(value.options)) {
    if (typeof value.options.doubleModifier === "boolean") state.options.doubleModifier = value.options.doubleModifier;
    state.options.sendKeybindingsToShell = value.options.sendKeybindingsToShell === true;
  }
  if (isRecord(value.migrations) && value.migrations.paletteShortcut === true) {
    state.migrations.paletteShortcut = true;
  }
  return { state, dropped };
}

/** Converts overrides into resolver rules (`remove` becomes the `-command` form). */
export function overridesToRules(overrides: readonly KeybindingOverride[]): KeymapPresetRule[] {
  return overrides.map((override) => {
    const rule: KeymapPresetRule = { command: override.remove ? `-${override.command}` : override.command };
    if (override.key !== undefined) rule.key = override.key;
    if (override.when) rule.when = override.when;
    if (override.platform !== undefined) rule.platform = override.platform;
    return rule;
  });
}

export type LegacyPaletteShortcut = "mod+k" | "mod+p" | "mod+shift+p";

const PALETTE_COMMAND = "builtin:command-palette";

/**
 * Overrides expressing a legacy palette shortcut choice. ⌘P keeps deferring
 * to the editor (the old `shouldDeferCommandPaletteShortcut` rule).
 */
export function paletteShortcutOverrides(shortcut: LegacyPaletteShortcut): KeybindingOverride[] {
  if (shortcut === "mod+k") return [];
  return [
    { command: PALETTE_COMMAND, key: "mod+k", remove: true },
    shortcut === "mod+p"
      ? { command: PALETTE_COMMAND, key: "mod+p", when: "!editor.focus" }
      : { command: PALETTE_COMMAND, key: "mod+shift+p" },
  ];
}

function isPaletteShortcutOverride(override: KeybindingOverride): boolean {
  if (override.command !== PALETTE_COMMAND || override.platform !== undefined) return false;
  if (override.remove) return override.key === "mod+k" && !override.when;
  return (override.key === "mod+p" && override.when === "!editor.focus") || (override.key === "mod+shift+p" && !override.when);
}

/** Replaces the palette-shortcut overrides (setShortcut shim); other overrides are kept. */
export function withPaletteShortcut(overrides: readonly KeybindingOverride[], shortcut: LegacyPaletteShortcut): KeybindingOverride[] {
  return [...overrides.filter((override) => !isPaletteShortcutOverride(override)), ...paletteShortcutOverrides(shortcut)];
}

/** The legacy shortcut the overrides express (for the settings select); mod+k otherwise. */
export function paletteShortcutFromOverrides(overrides: readonly KeybindingOverride[]): LegacyPaletteShortcut {
  const add = [...overrides].reverse().find((override) => isPaletteShortcutOverride(override) && !override.remove);
  if (add?.key === "mod+p") return "mod+p";
  if (add?.key === "mod+shift+p") return "mod+shift+p";
  return "mod+k";
}

/**
 * One-time migration of `app_command_palette_v1.shortcut`. Idempotent: the
 * `migrations.paletteShortcut` flag records that it ran, so a later change of
 * the legacy field (or a second hydrate) never re-applies it.
 */
export function migratePaletteShortcut(state: PersistedKeybindings, legacyShortcut: unknown): { state: PersistedKeybindings; changed: boolean } {
  if (state.migrations.paletteShortcut) return { state, changed: false };
  const shortcut: LegacyPaletteShortcut =
    legacyShortcut === "mod+p" || legacyShortcut === "mod+shift+p" ? legacyShortcut : "mod+k";
  const overrides = shortcut === "mod+k" ? state.overrides : withPaletteShortcut(state.overrides, shortcut);
  return {
    state: { ...state, overrides: overrides.slice(0, MAX_KEYBINDING_OVERRIDES), migrations: { ...state.migrations, paletteShortcut: true } },
    changed: true,
  };
}
