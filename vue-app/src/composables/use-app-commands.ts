/**
 * Application-level commands registered at startup (App.vue), so both the
 * keybinding dispatcher and the lazily loaded command palette can run them.
 * Key-only commands set `showInPalette: false`.
 */
import { onBeforeUnmount } from "vue";
import type { CommandPaletteOpenRequest } from "@/domain/commands/command-palette";
import { useVueI18n } from "@/i18n/messages";
import { registerCommand } from "@/services/command-registry";
import { useAiAssistantShellStore } from "@/stores/ai-assistant-shell";

export interface AppCommandsOptions {
  openPalette: (request: Omit<CommandPaletteOpenRequest, "seq">) => void;
  openSettings: (category?: "shortcuts") => void;
  /** Pop-out windows render no palette, settings or AI drawer. */
  popup?: boolean;
}

export function useAppCommands(options: AppCommandsOptions): void {
  const { t } = useVueI18n();
  const assistant = useAiAssistantShellStore();
  if (options.popup) return;

  const disposers = [
    registerCommand({
      id: "builtin:command-palette",
      source: "builtin",
      title: () => t("keybindings.command.builtin:command-palette"),
      showInPalette: false,
      execute: () => options.openPalette({}),
    }),
    registerCommand({
      id: "builtin:settings",
      source: "builtin",
      title: () => t("commandPalette.openSettings"),
      subtitle: () => t("commandPalette.source.builtin"),
      keywords: ["preferences", "config"],
      priority: 0,
      execute: () => options.openSettings(),
    }),
    registerCommand({
      id: "builtin:keybindings",
      source: "builtin",
      title: () => t("keybindings.command.builtin:keybindings"),
      subtitle: () => t("commandPalette.source.builtin"),
      keywords: ["keybindings", "shortcuts", "keymap", "快捷键"],
      execute: () => options.openSettings("shortcuts"),
    }),
    registerCommand({
      id: "builtin:search-everywhere",
      source: "builtin",
      title: () => t("keybindings.command.builtin:search-everywhere"),
      showInPalette: false,
      execute: () => options.openPalette({ scope: "all" }),
    }),
    registerCommand({
      id: "builtin:ai",
      source: "builtin",
      title: () => t("commandPalette.openAi"),
      subtitle: () => t("commandPalette.source.builtin"),
      keywords: ["agent", "acp", "mcp", "connector", "连接器"],
      priority: 1,
      // Toggle: the palette closes itself first, so selecting it there opens the drawer.
      execute: () => (assistant.open ? assistant.close() : assistant.show("agentic")),
    }),
  ];

  onBeforeUnmount(() => {
    for (const dispose of disposers) dispose();
  });
}
