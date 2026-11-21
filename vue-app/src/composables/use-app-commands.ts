/**
 * Application-level commands registered at startup (App.vue), so both the
 * keybinding dispatcher and the lazily loaded command palette can run them.
 * Key-only commands set `showInPalette: false`.
 */
import { onBeforeUnmount } from "vue";
import type { CommandPaletteOpenRequest } from "@/domain/commands/command-palette";
import { registerKeybindingContext } from "@/domain/keybindings/context";
import { useVueI18n } from "@/i18n/messages";
import { registerCommand } from "@/services/command-registry";
import { useAiAssistantShellStore } from "@/stores/ai-assistant-shell";
import { useAppLockStore } from "@/stores/app-lock";

export interface AppCommandsOptions {
  openPalette: (request: Omit<CommandPaletteOpenRequest, "seq">) => void;
  openSettings: (category?: "shortcuts") => void;
  /** Pop-out windows render no palette, settings or AI drawer: only the lock command applies. */
  popup?: boolean;
}

export function useAppCommands(options: AppCommandsOptions): void {
  const { t } = useVueI18n();
  const assistant = useAiAssistantShellStore();
  const appLock = useAppLockStore();

  const lockDisposers = [
    registerKeybindingContext("app.lock.available", () => appLock.configured && !appLock.locked),
    registerCommand({
      id: "builtin:lock-app",
      source: "builtin",
      title: () => t("commandPalette.lockApp"),
      subtitle: () => t("commandPalette.source.builtin"),
      keywords: ["lock", "security", "锁定"],
      priority: 2,
      when: "app.lock.available",
      execute: () => {
        appLock.lock();
      },
    }),
  ];
  const disposers = options.popup ? lockDisposers : [
    ...lockDisposers,
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
    // Palette entry points. The palette falls back to "all" where no project is open.
    registerCommand({
      id: "builtin:quick-open",
      source: "builtin",
      title: () => t("keybindings.command.builtin:quick-open"),
      showInPalette: false,
      execute: () => options.openPalette({ scope: "files" }),
    }),
    registerCommand({
      id: "builtin:find-in-files",
      source: "builtin",
      title: () => t("keybindings.command.builtin:find-in-files"),
      showInPalette: false,
      execute: () => options.openPalette({ scope: "content" }),
    }),
    registerCommand({
      id: "builtin:recent-files",
      source: "builtin",
      title: () => t("keybindings.command.builtin:recent-files"),
      showInPalette: false,
      execute: () => options.openPalette({ prefix: "@" }),
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
