/**
 * Registers keybinding/palette commands for the lifetime of an *active* view:
 * on mount and on KeepAlive activation, disposed on deactivation and unmount.
 * Use it for commands whose target belongs to one workspace view, such as
 * `workbench:toggle-sidebar`, so an inactive (cached) view never answers.
 */
import { onActivated, onBeforeUnmount, onDeactivated, onMounted } from "vue";
import { useVueI18n } from "@/i18n/messages";
import { registerCommand } from "@/services/command-registry";

export function useActiveViewCommands(register: () => Array<() => void>): void {
  let disposers: Array<() => void> = [];
  const start = () => {
    if (disposers.length) return;
    disposers = register();
  };
  const stop = () => {
    for (const dispose of disposers) dispose();
    disposers = [];
  };
  onMounted(start);
  onActivated(start);
  onDeactivated(stop);
  onBeforeUnmount(stop);
}

/** `workbench:toggle-sidebar` for a view whose sidebar is a collapsible pane. */
export function useToggleSidebarCommand(collapsed: () => boolean, setCollapsed: (value: boolean) => void): void {
  const { t } = useVueI18n();
  useActiveViewCommands(() => [
    registerCommand({
      id: "workbench:toggle-sidebar",
      source: "builtin",
      title: () => t("keybindings.command.workbench:toggle-sidebar"),
      subtitle: () => t("commandPalette.source.builtin"),
      keywords: ["sidebar", "explorer", "侧栏"],
      execute: () => setCollapsed(!collapsed()),
    }),
  ]);
}
