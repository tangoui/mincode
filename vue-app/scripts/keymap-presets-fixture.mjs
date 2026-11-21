import { BUILTIN_KEYMAP_PRESETS } from "../src/domain/keybindings/presets/index.ts";

export const ALL_KEYMAP_PRESETS = BUILTIN_KEYMAP_PRESETS;

export function keymapPresetChainOf(id) {
  const preset = BUILTIN_KEYMAP_PRESETS.find((entry) => entry.id === id);
  if (!preset) throw new Error("Unknown built-in preset " + id);
  return [preset];
}
