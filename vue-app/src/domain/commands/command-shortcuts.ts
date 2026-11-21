/**
 * Legacy command-palette shortcut choices (settings select). Matching now goes
 * through the keybinding dispatcher; the choice is stored as keymap overrides
 * (domain/keybindings/persistence.ts `withPaletteShortcut`).
 */
export type CommandPaletteShortcutId = "mod+k" | "mod+p" | "mod+shift+p";

export interface CommandPaletteShortcutOption {
  id: CommandPaletteShortcutId;
  label: string;
}

export const DEFAULT_COMMAND_PALETTE_SHORTCUT: CommandPaletteShortcutId = "mod+k";

export const COMMAND_PALETTE_SHORTCUTS: readonly CommandPaletteShortcutOption[] = [
  { id: "mod+k", label: "⌘ / Ctrl + K" },
  { id: "mod+p", label: "⌘ / Ctrl + P" },
  { id: "mod+shift+p", label: "⌘ / Ctrl + Shift + P" },
];

export function normalizeCommandPaletteShortcut(value: unknown): CommandPaletteShortcutId {
  return COMMAND_PALETTE_SHORTCUTS.some((option) => option.id === value)
    ? (value as CommandPaletteShortcutId)
    : DEFAULT_COMMAND_PALETTE_SHORTCUT;
}

export function commandPaletteShortcutLabel(value: unknown): string {
  const shortcut = normalizeCommandPaletteShortcut(value);
  return (
    COMMAND_PALETTE_SHORTCUTS.find((option) => option.id === shortcut)?.label ??
    COMMAND_PALETTE_SHORTCUTS[0].label
  );
}
