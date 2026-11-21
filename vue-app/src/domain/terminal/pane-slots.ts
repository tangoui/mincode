import type { InjectionKey } from "vue";
import { isTerminalLeafPane, type TerminalLeafPane, type TerminalPaneNode } from "@/domain/terminal/types";

/**
 * Where each terminal pane is shown. The split tree renders only these slots; the terminals are
 * rendered flat (one instance per pane id) and teleported into their slot, so a split that moves
 * a pane under a new split group moves its DOM instead of rebuilding its xterm.
 */
export interface TerminalPaneSlots {
  register(paneId: string, element: HTMLElement): void;
  unregister(paneId: string, element: HTMLElement): void;
}

export const TERMINAL_PANE_SLOTS: InjectionKey<TerminalPaneSlots> = Symbol("terminal-pane-slots");

/** The leaf panes of a split tree, depth first. */
export function terminalLeafPanes(node: TerminalPaneNode): TerminalLeafPane[] {
  if (isTerminalLeafPane(node)) return [node];
  return node.children.flatMap(terminalLeafPanes);
}
