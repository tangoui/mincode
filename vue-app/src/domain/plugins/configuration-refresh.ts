/**
 * Pure refresh rules for Settings > Plugins. The page re-reads the plugin list
 * and the selected plugin's settings whenever the plugin host announces a
 * change (install/uninstall, enable/disable, development session attach,
 * detach or generation swap, workspace switch). These helpers keep that
 * refresh predictable:
 *
 * - selection never silently jumps to another plugin when the selected one
 *   disappears; the page shows a "no longer available" state instead;
 * - unsaved edits survive a refresh while their field still exists and the
 *   edited value is valid under the (possibly changed) schema;
 * - bursts of host events collapse into one refresh;
 * - the Settings navigation lists the page only while some installed plugin
 *   declares settings.
 *
 * Stale responses are discarded with domain/async/latest-request-gate.ts.
 */
import {
  isValidConfigurationValue,
  type PluginConfigurationField,
  type PluginConfigurationValue,
} from "./configuration";

export const PLUGIN_SETTINGS_REFRESH_DEBOUNCE_MS = 120;

export interface ConfigurationSelection {
  selectedId: string;
  /** The previously selected plugin is no longer configurable (kept selected, not replaced). */
  missing: boolean;
}

/**
 * Chooses the selected plugin after the list changed. `preferred` (an explicit
 * navigation target) wins when present. An existing selection that vanished
 * stays selected and is reported as missing; only an empty selection falls
 * back to the first plugin.
 */
export function reconcileConfigurationSelection(ids: readonly string[], current: string, preferred?: string): ConfigurationSelection {
  if (preferred && ids.includes(preferred)) return { selectedId: preferred, missing: false };
  if (current) return { selectedId: current, missing: !ids.includes(current) };
  return { selectedId: ids[0] ?? "", missing: false };
}

function sameValue(left: unknown, right: unknown) {
  return JSON.stringify(left) === JSON.stringify(right);
}

export interface MergedConfigurationDrafts {
  values: Record<string, PluginConfigurationValue>;
  /** Keys whose unsaved edit was kept. */
  preserved: string[];
}

/**
 * Merges refreshed values into an edit buffer. A draft entry is an unsaved
 * edit when it differs from the value the buffer was last synced to
 * (`previousBaseline`). Such edits are kept when the field still exists in
 * the refreshed schema, still has a non-secret control of the same type and
 * the value is valid for it; everything else takes the refreshed value.
 */
export function mergeConfigurationDrafts(
  previousBaseline: Readonly<Record<string, PluginConfigurationValue>>,
  draft: Readonly<Record<string, PluginConfigurationValue>>,
  nextBaseline: Readonly<Record<string, PluginConfigurationValue>>,
  previousFields: readonly PluginConfigurationField[],
  nextFields: readonly PluginConfigurationField[],
): MergedConfigurationDrafts {
  const values: Record<string, PluginConfigurationValue> = { ...nextBaseline };
  const preserved: string[] = [];
  const before = new Map(previousFields.map((field) => [field.key, field]));
  for (const field of nextFields) {
    const key = field.key;
    if (!Object.prototype.hasOwnProperty.call(nextBaseline, key)) continue;
    if (!Object.prototype.hasOwnProperty.call(draft, key) || !Object.prototype.hasOwnProperty.call(previousBaseline, key)) continue;
    const edited = draft[key];
    if (sameValue(edited, previousBaseline[key])) continue;
    const old = before.get(key);
    if (!old || old.control !== field.control || field.control === "secret") continue;
    if (!isValidConfigurationValue(field.property, edited)) continue;
    values[key] = edited!;
    preserved.push(key);
  }
  return { values, preserved };
}

export interface DebouncedTrigger {
  schedule(): void;
  cancel(): void;
}

export interface DebounceTimers {
  set: (callback: () => void, delay: number) => unknown;
  clear: (handle: unknown) => void;
}

const defaultTimers: DebounceTimers = {
  set: (callback, delay) => setTimeout(callback, delay),
  clear: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

/** Collapses bursts of `schedule()` calls into one `run()` after `delay` ms of quiet. */
export function createDebouncedTrigger(run: () => void, delay = PLUGIN_SETTINGS_REFRESH_DEBOUNCE_MS, timers: DebounceTimers = defaultTimers): DebouncedTrigger {
  let handle: unknown;
  let pending = false;
  return {
    schedule() {
      if (pending) timers.clear(handle);
      pending = true;
      handle = timers.set(() => {
        pending = false;
        handle = undefined;
        run();
      }, delay);
    },
    cancel() {
      if (pending) timers.clear(handle);
      pending = false;
      handle = undefined;
    },
  };
}

/**
 * What the plugin host last reported about configurable plugins: not asked yet
 * (always so outside the desktop runtime), none, at least one, or the list
 * could not be read.
 */
export type PluginSettingsAvailability = "unknown" | "none" | "some" | "error";

/**
 * Whether the Settings navigation lists the Plugins page. It stays hidden until
 * the host reports a plugin that declares settings; a failed read lists it so
 * the page can show the error, and it is never removed while it is the current
 * page, so a refresh does not pull the page away from the user.
 */
export function isPluginSettingsCategoryListed(availability: PluginSettingsAvailability, current: boolean): boolean {
  return current || availability === "some" || availability === "error";
}
