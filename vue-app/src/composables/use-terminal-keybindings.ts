/**
 * Keymap-driven key handling for xterm terminals. Install the returned
 * handler with `xterm.attachCustomKeyEventHandler(handler)`; it replaces
 * per-terminal onKey / ad-hoc key hacks.
 *
 *  - `terminal:*` commands of the effective keymap (copy, paste, find, clear,
 *    select all) run here, with preventDefault so neither xterm nor the native
 *    Edit menu handle the key. Paste must be the terminal's guarded paste path
 *    (clipboard read → multi-line analysis → confirmation).
 *  - skip-shell application bindings and macOS ⌘ chords bubble (no
 *    preventDefault) to services/keybinding-dispatcher.ts.
 *  - everything else goes to the shell.
 *
 * The decision itself is pure (domain/terminal/keybindings.ts) so it is tested
 * without a DOM (scripts/terminal-keybindings.test.mjs).
 */
import { getKeybindingCommand, isPluginCommandId } from "@/domain/keybindings/catalog";
import { createTerminalKeyHandler, type TerminalKeybindingActions } from "@/domain/terminal/keybindings";

export type { TerminalKeybindingActions } from "@/domain/terminal/keybindings";
import type { PluginContextValues } from "@/domain/commands/when";
import { isRegisteredCommandEnabled } from "@/services/command-registry";
import { currentKeybindingContext } from "@/services/keybinding-dispatcher";
import { useKeybindingsStore } from "@/stores/keybindings";

function canExecuteAppCommand(command: string, context: PluginContextValues): boolean {
  if (isPluginCommandId(command)) return isRegisteredCommandEnabled(command, context);
  const entry = getKeybindingCommand(command);
  if (!entry || !entry.available || entry.handler !== "app") return false;
  return isRegisteredCommandEnabled(command, context);
}

/** Keymap handler for one terminal; pass the result to `xterm.attachCustomKeyEventHandler`. */
export function useTerminalKeybindings(actions: TerminalKeybindingActions): (event: KeyboardEvent) => boolean {
  const store = useKeybindingsStore();
  return createTerminalKeyHandler<KeyboardEvent>(actions, {
    platform: store.platform,
    resolution: () => store.resolution,
    sendKeybindingsToShell: () => store.options.sendKeybindingsToShell === true,
    canExecuteApp: canExecuteAppCommand,
    context: (event) => currentKeybindingContext(event.target),
  });
}

/**
 * Routes native paste events (Edit ▸ Paste menu, middle click) through the
 * terminal's guarded paste instead of xterm's direct paste. Capture phase on
 * the terminal host so xterm's textarea listener never sees the event.
 */
export function guardTerminalPasteEvents(host: HTMLElement, paste: (text: string) => void): () => void {
  const onPaste = (event: ClipboardEvent) => {
    event.preventDefault();
    event.stopPropagation();
    const text = event.clipboardData?.getData("text/plain") ?? "";
    if (text) paste(text);
  };
  host.addEventListener("paste", onPaste, true);
  return () => host.removeEventListener("paste", onPaste, true);
}
