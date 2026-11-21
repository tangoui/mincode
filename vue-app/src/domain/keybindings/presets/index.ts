/**
 * Built-in keymap presets. Only MinCode is built in; VS Code, IntelliJ IDEA,
 * Eclipse and Vim ship as official declarative plugins
 * (`official-plugins/keymaps`, registered through the `keymaps` contribution
 * point by services/contributions/keymaps.ts).
 * Built-ins go through the same validator as plugin-contributed presets
 * (`registerKeymapPreset`), so a malformed built-in fails loudly at startup
 * and in the presets test.
 */
import { registerKeymapPreset } from "@/domain/keybindings/preset";
import { MINCODE_PRESET } from "@/domain/keybindings/presets/mincode";

export { HOST_KEYBINDING_RULES, MINCODE_PRESET } from "@/domain/keybindings/presets/mincode";

export const BUILTIN_KEYMAP_PRESETS = Object.freeze([MINCODE_PRESET]);

let registered = false;

/** Idempotently registers the built-in presets. */
export function registerBuiltinKeymapPresets(): void {
  if (registered) return;
  registered = true;
  for (const preset of BUILTIN_KEYMAP_PRESETS) {
    registerKeymapPreset(preset, { source: "builtin" });
  }
}
