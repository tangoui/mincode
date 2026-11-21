/**
 * Test helper (not a test): loads the keymap presets of the official
 * declarative keymap plugins (every official-plugins/<id>/plugin.json whose
 * contributes.keymaps is non-empty; today only `keymaps`, inline entries or
 * {"path": ...}) and registers them through the same frontend stage the
 * `keymaps` contribution consumer uses at runtime
 * (domain/plugins/keymap-contributions.ts, wired by
 * services/contributions/keymaps.ts). Any rejected preset throws.
 */
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { registerKeymapContributions } from "../src/domain/plugins/keymap-contributions.ts";
import { BUILTIN_KEYMAP_PRESETS, registerBuiltinKeymapPresets } from "../src/domain/keybindings/presets/index.ts";

const pluginsRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../../official-plugins");

/** [{ pluginId, pluginName, manifest, presets }] for every official package that contributes keymaps. */
export function loadOfficialKeymapPlugins() {
  return readdirSync(pluginsRoot, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && existsSync(join(pluginsRoot, entry.name, "plugin.json")))
    .map((entry) => entry.name)
    .sort()
    .map((name) => ({ directory: join(pluginsRoot, name), manifest: JSON.parse(readFileSync(join(pluginsRoot, name, "plugin.json"), "utf8")) }))
    .filter(({ manifest }) => Array.isArray(manifest.contributes?.keymaps) && manifest.contributes.keymaps.length > 0)
    .map(({ directory, manifest }) => {
      const presets = manifest.contributes.keymaps.map((item) =>
        typeof item?.path === "string" ? JSON.parse(readFileSync(join(directory, item.path), "utf8")) : item,
      );
      return { pluginId: manifest.id, pluginName: manifest.name, manifest, presets };
    });
}

/** Every official plugin preset, flattened. */
export const PLUGIN_KEYMAP_PRESETS = Object.freeze(loadOfficialKeymapPlugins().flatMap((plugin) => plugin.presets));

/** Built-in + official plugin presets (the full set users can choose from). */
export const ALL_KEYMAP_PRESETS = Object.freeze([...BUILTIN_KEYMAP_PRESETS, ...PLUGIN_KEYMAP_PRESETS]);

export function keymapPresetById(id) {
  const preset = ALL_KEYMAP_PRESETS.find((candidate) => candidate.id === id);
  if (!preset) throw new Error(`unknown keymap preset ${id}`);
  return preset;
}

/** The `extends` chain of a preset (root first), resolved against ALL_KEYMAP_PRESETS. */
export function keymapPresetChainOf(id) {
  const chain = [];
  for (let current = keymapPresetById(id); current; current = current.extends ? keymapPresetById(current.extends) : undefined) {
    chain.unshift(current);
  }
  return chain;
}

let registered = false;

/** Idempotently registers the built-in and official plugin presets in the shared registry. */
export function registerAllKeymapPresets() {
  registerBuiltinKeymapPresets();
  if (registered) return;
  registered = true;
  // The same shape the host snapshot hands the consumer (entries already resolved).
  const contributions = loadOfficialKeymapPlugins().map((plugin) => ({
    pluginId: plugin.pluginId,
    pluginName: plugin.pluginName,
    version: String(plugin.manifest.version ?? ""),
    source: "official",
    runtime: "declarative",
    activeInWorkspace: true,
    point: { fingerprint: "", entries: plugin.presets, issues: [] },
  }));
  const { issues } = registerKeymapContributions(contributions);
  if (issues.length > 0) {
    throw new Error(`official keymap presets rejected: ${issues.map((issue) => `${issue.preset ?? issue.entry}: ${issue.message}`).join("; ")}`);
  }
}
