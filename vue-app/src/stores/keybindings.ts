/**
 * Keybinding settings and the resolved keymap. Global by design: keymaps are
 * a user-level preference like the theme, not workspace runtime state, so the
 * store persists under the global key `app_keybindings_v1`.
 *
 * `preset` stays the stored id even when that preset is unavailable (e.g. a
 * keymap plugin is disabled); the store then resolves with MinCode and sets
 * `presetUnavailable`, and switches back automatically once it registers.
 * Overrides are independent of the preset and survive preset changes.
 */
import { computed, ref, shallowRef } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import { getEditorModeProvider, subscribeEditorModeProviders } from "@/domain/keybindings/editor-mode";
import { formatKeySequence, type KeyboardPlatform } from "@/domain/keybindings/keys";
import { monacoDefaultRules } from "@/domain/keybindings/monaco-defaults";
import {
  defaultPersistedKeybindings,
  KEYBINDINGS_STORAGE_KEY,
  MAX_KEYBINDING_OVERRIDES,
  migratePaletteShortcut,
  normalizeKeybindingOverride,
  normalizePersistedKeybindings,
  overridesToRules,
  presetPluginId,
  paletteShortcutFromOverrides,
  withPaletteShortcut,
  type KeybindingOptions,
  type KeybindingOverride,
  type LegacyPaletteShortcut,
  type PersistedKeybindings,
} from "@/domain/keybindings/persistence";
import {
  keymapPresetChain,
  MINCODE_PRESET_ID,
  presetRulesForPlatform,
  subscribeKeymapPresets,
  type KeymapEditorMode,
  type KeymapPreset,
} from "@/domain/keybindings/preset";
import { HOST_KEYBINDING_RULES, registerBuiltinKeymapPresets } from "@/domain/keybindings/presets";
import { primaryBinding, pruneReplacedDefaults, resolveKeybindings, type KeybindingResolution } from "@/domain/keybindings/resolver";
import { currentKeyboardPlatform } from "@/services/keyboard-platform";
import {
  loadPersistedValue,
  registerDesktopBrowserSnapshotKeys,
  savePersistedValue,
} from "@/services/persistent-store";

const LEGACY_PALETTE_STORAGE_KEY = "app_command_palette_v1";

// The dispatcher needs the keymap before the async native store answers, so a
// localStorage snapshot is kept in the desktop runtime too.
registerDesktopBrowserSnapshotKeys(KEYBINDINGS_STORAGE_KEY);

function readBrowserSnapshot(): PersistedKeybindings {
  try {
    const raw = window.localStorage.getItem(KEYBINDINGS_STORAGE_KEY);
    return normalizePersistedKeybindings(raw ? JSON.parse(raw) : null).state;
  } catch {
    return defaultPersistedKeybindings();
  }
}

function cloneState(state: PersistedKeybindings): PersistedKeybindings {
  return JSON.parse(JSON.stringify(state)) as PersistedKeybindings;
}

export const useKeybindingsStore = defineStore("keybindings", () => {
  registerBuiltinKeymapPresets();
  const platform: KeyboardPlatform = currentKeyboardPlatform();
  const state = shallowRef<PersistedKeybindings>(readBrowserSnapshot());
  const hydrated = ref(false);
  const droppedOverrides = ref(0);
  /** Set while the settings key recorder captures keys; the dispatcher stands down. */
  const recording = ref(false);
  const presetRevision = ref(0);
  const editorModeRevision = ref(0);
  subscribeKeymapPresets(() => {
    presetRevision.value += 1;
  });
  subscribeEditorModeProviders(() => {
    editorModeRevision.value += 1;
  });
  let hydratePromise: Promise<void> | undefined;

  const requestedChain = computed<KeymapPreset[] | null>(() => {
    void presetRevision.value;
    return keymapPresetChain(state.value.preset);
  });
  /** The stored preset (or an ancestor) is not registered; MinCode is used meanwhile. */
  const presetUnavailable = computed(() => requestedChain.value === null);
  /** Plugin id that provides the unavailable preset (`<pluginId>.<preset>`), for the marketplace notice. */
  const presetPluginSuggestion = computed(() => (presetUnavailable.value ? presetPluginId(state.value.preset) : undefined));
  const presetChain = computed<KeymapPreset[]>(() => {
    void presetRevision.value;
    return requestedChain.value ?? keymapPresetChain(MINCODE_PRESET_ID) ?? [];
  });
  const presetId = computed(() => state.value.preset);
  const activePreset = computed(() => presetChain.value[presetChain.value.length - 1]);
  const overrides = computed(() => state.value.overrides);
  const options = computed(() => state.value.options);

  const resolution = computed<KeybindingResolution>(() =>
    resolveKeybindings({
      platform,
      presets: presetChain.value,
      defaults: pruneReplacedDefaults(
        monacoDefaultRules(platform),
        [...presetChain.value.flatMap((preset) => presetRulesForPlatform(preset.rules, platform)), ...presetRulesForPlatform(HOST_KEYBINDING_RULES, platform)],
        platform,
      ),
      host: HOST_KEYBINDING_RULES,
      user: presetRulesForPlatform(overridesToRules(state.value.overrides), platform),
    }),
  );

  const doubleModifier = computed(
    () => state.value.options.doubleModifier ?? presetChain.value.some((preset) => preset.doubleModifier === true),
  );

  /** Modal editor emulation requested by the active preset chain (child wins). */
  const editorMode = computed<KeymapEditorMode | undefined>(() => {
    for (let index = presetChain.value.length - 1; index >= 0; index--) {
      const mode = presetChain.value[index]!.editorMode;
      if (mode) return mode;
    }
    return undefined;
  });
  /** The provider implementing `editorMode`, once one is registered (phase 5+). */
  const editorModeProvider = computed(() => {
    void editorModeRevision.value;
    return getEditorModeProvider(editorMode.value);
  });

  const paletteShortcut = computed<LegacyPaletteShortcut>(() => paletteShortcutFromOverrides(state.value.overrides));

  /** Display label of a command's primary binding (e.g. "⌘K"); empty when unbound. */
  function label(command: string): string {
    const binding = primaryBinding(resolution.value, command);
    return binding ? formatKeySequence(binding.sequence, platform) : "";
  }

  async function persist() {
    await savePersistedValue(KEYBINDINGS_STORAGE_KEY, cloneState(state.value));
  }

  function commit(next: PersistedKeybindings) {
    state.value = next;
    if (hydrated.value) void persist();
  }

  async function hydrate(force = false) {
    if (hydrated.value && !force) return;
    if (hydratePromise && !force) return hydratePromise;
    hydratePromise = (async () => {
      const [stored, legacy] = await Promise.all([
        loadPersistedValue<unknown>(KEYBINDINGS_STORAGE_KEY),
        loadPersistedValue<{ shortcut?: unknown }>(LEGACY_PALETTE_STORAGE_KEY),
      ]);
      const normalized = normalizePersistedKeybindings(stored);
      droppedOverrides.value = normalized.dropped;
      const migrated = migratePaletteShortcut(normalized.state, legacy?.shortcut);
      state.value = migrated.state;
      hydrated.value = true;
      if (migrated.changed || normalized.dropped) await persist();
    })();
    try {
      await hydratePromise;
    } finally {
      hydratePromise = undefined;
    }
  }

  function setPreset(id: string) {
    const trimmed = id.trim();
    if (!trimmed || trimmed === state.value.preset) return;
    commit({ ...state.value, preset: trimmed });
  }

  /** Replaces all overrides; invalid entries are rejected (returns how many). */
  function setOverrides(next: readonly unknown[]): number {
    const valid = next.map(normalizeKeybindingOverride).filter((item): item is KeybindingOverride => item !== null);
    commit({ ...state.value, overrides: valid.slice(0, MAX_KEYBINDING_OVERRIDES) });
    return next.length - Math.min(valid.length, MAX_KEYBINDING_OVERRIDES);
  }

  function addOverride(override: KeybindingOverride): boolean {
    const valid = normalizeKeybindingOverride(override);
    if (!valid || state.value.overrides.length >= MAX_KEYBINDING_OVERRIDES) return false;
    commit({ ...state.value, overrides: [...state.value.overrides, valid] });
    return true;
  }

  function removeOverrideAt(index: number) {
    if (index < 0 || index >= state.value.overrides.length) return;
    commit({ ...state.value, overrides: state.value.overrides.filter((_, current) => current !== index) });
  }

  function resetOverrides() {
    commit({ ...state.value, overrides: [] });
  }

  function setOptions(patch: Partial<KeybindingOptions>) {
    const next: KeybindingOptions = { ...state.value.options };
    if ("doubleModifier" in patch) {
      if (typeof patch.doubleModifier === "boolean") next.doubleModifier = patch.doubleModifier;
      else delete next.doubleModifier;
    }
    if (typeof patch.sendKeybindingsToShell === "boolean") next.sendKeybindingsToShell = patch.sendKeybindingsToShell;
    commit({ ...state.value, options: next });
  }

  /** Legacy command-palette shortcut select (commandPalette.setShortcut shim). */
  function setPaletteShortcut(shortcut: LegacyPaletteShortcut) {
    commit({ ...state.value, overrides: withPaletteShortcut(state.value.overrides, shortcut).slice(0, MAX_KEYBINDING_OVERRIDES) });
  }

  function setRecording(active: boolean) {
    recording.value = active;
  }

  return {
    platform,
    hydrated,
    droppedOverrides,
    recording,
    presetId,
    presetUnavailable,
    presetPluginSuggestion,
    presetChain,
    activePreset,
    overrides,
    options,
    resolution,
    doubleModifier,
    editorMode,
    editorModeProvider,
    paletteShortcut,
    label,
    hydrate,
    setPreset,
    setOverrides,
    addOverride,
    removeOverrideAt,
    resetOverrides,
    setOptions,
    setPaletteShortcut,
    setRecording,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useKeybindingsStore, import.meta.hot));
}
