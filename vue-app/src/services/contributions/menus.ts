/**
 * Consumer of the `menus` contribution point (process plugins, manifest API
 * v2). The Go host validated every item at install and load time
 * (backend/plugin/contrib_menus.go); the snapshot entries are still
 * re-checked here (domain/plugins/menus.ts) before they reach a menu.
 *
 * - Host context menus (project tree, workbench tab, terminal) render items
 *   through `usePluginMenuItems` + components/plugins/PluginMenuItems.vue.
 * - The Monaco editor and SQL editor menus are driven by
 *   services/monaco-menus.ts, installed lazily with Monaco.
 * - `commandPalette` entries become palette overrides in plugin-commands.ts.
 * - `tray` entries (system tray, host 0.3.0) render in no app menu
 *   (contextMenuItems); when a snapshot has any, services/plugin-tray.ts is
 *   loaded on demand and syncs the visible set to the native tray.
 *
 * An item shows only while its command is registered (plugin enabled and
 * effective in this workspace), the command's own `when` holds, and the item's
 * `when` holds against the menu context. Running an item reuses the plugin
 * command path; nothing here starts a plugin process by itself.
 */
import { computed, shallowRef, toValue, type ComputedRef, type MaybeRefOrGetter } from "vue";
import { ElMessage } from "element-plus";
import { evaluateWhen, type PluginContextValues } from "@/domain/commands/when";
import {
  MENU_CONTRIBUTION_POINT,
  MENU_LIMITS,
  MenuContextFieldTooLongError,
  MenuPayloadTooLargeError,
  MenuTargetUnavailableError,
  contextMenuItems,
  isPluginContextMenuId,
  menuContextValues,
  menuItemCommandKey,
  menuItemTitle,
  menuItemVisible,
  normalizeMenuContext,
  parseMenuContributions,
  sortMenuItems,
  splitMenuOverflow,
  type PluginContextMenuId,
  type PluginMenuIssue,
  type PluginMenuItem,
} from "@/domain/plugins/menus";
import { useVueI18n } from "@/i18n/messages";
import { currentKeyboardPlatform } from "@/services/keyboard-platform";
import { installMonacoPluginMenus } from "@/services/monaco-menus";
import {
  executePluginCommand,
  pluginCommandState,
  setPluginCommandPaletteOverrides,
  type PluginCommandPaletteOverride,
} from "@/services/plugin-commands";
import { registerContributionConsumer } from "@/services/plugin-contributions";
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";

export type { PluginMenuIssue, PluginMenuItem } from "@/domain/plugins/menus";

/** One plugin item ready to render in a host menu. */
export interface ResolvedPluginMenuItem {
  key: string;
  item: PluginMenuItem;
  title: string;
}

export interface PluginMenuItemsView {
  visible: ResolvedPluginMenuItem[];
  overflow: ResolvedPluginMenuItem[];
}

const items = shallowRef<readonly PluginMenuItem[]>([]);
let lastIssues: PluginMenuIssue[] = [];
let lastSyncError: string | undefined;

/** Validated menu items of the current snapshot (all menus). */
export const pluginMenuItems = items;

/** Entries dropped by the frontend check of the last snapshot (diagnostics). */
export function menuPluginIssues(): readonly PluginMenuIssue[] {
  return lastIssues;
}

export function menuPluginSyncError(): string | undefined {
  return lastSyncError;
}

function availableCommand(item: PluginMenuItem) {
  const command = pluginCommandState.value.commands.find(
    (candidate) => candidate.pluginId === item.pluginId && candidate.id === item.command,
  );
  if (!command) return undefined;
  if (command.when && !evaluateWhen(command.when, pluginCommandState.value.contexts[item.pluginId] ?? {})) return undefined;
  return command;
}

/** Items of one menu whose command is available and whose `when` holds, sorted and split for overflow. */
export function resolvePluginMenuItems(
  menu: PluginContextMenuId,
  host: PluginContextValues,
  locale: string | readonly string[],
): PluginMenuItemsView {
  const platform = currentKeyboardPlatform();
  const candidates = contextMenuItems(items.value, menu).filter(
    (item) =>
      availableCommand(item) !== undefined &&
      menuItemVisible(item, menuContextValues(menu, host, pluginCommandState.value.contexts[item.pluginId] ?? {}, platform)),
  );
  const resolved = candidates.map((item) => ({ key: menuItemCommandKey(item), item, title: menuItemTitle(item, locale) }));
  const sorted = sortMenuItems(
    resolved.map((entry) => ({ ...entry, group: entry.item.group, order: entry.item.order, pluginName: entry.item.pluginName })),
    (entry) => entry.title,
  ).map(({ key, item, title }) => ({ key, item, title }));
  return splitMenuOverflow(sorted);
}

/**
 * Reactive plugin items of one host menu. `host` returns the menu's host
 * context values (for example `resource.kind`); pass `null` while the menu is
 * closed to skip evaluation.
 */
export function usePluginMenuItems(
  menu: PluginContextMenuId,
  host: MaybeRefOrGetter<PluginContextValues | null>,
): ComputedRef<PluginMenuItemsView> {
  const { localeChain } = useVueI18n();
  return computed(() => {
    const values = toValue(host);
    if (!values) return { visible: [], overflow: [] };
    return resolvePluginMenuItems(menu, values, localeChain.value);
  });
}

/**
 * Runs a menu item. `context` is the menu's passContext object; it is sent
 * only when the item declares passContext. Field limits are checked before the
 * command runs (no activation or parameter form for a context that cannot be
 * sent). Failures are reported to the user.
 */
export async function runPluginMenuItem(item: PluginMenuItem, context: () => Record<string, unknown>): Promise<void> {
  const { t, localeChain } = useVueI18n();
  const name = menuItemTitle(item, localeChain.value);
  try {
    const invocation =
      item.passContext && isPluginContextMenuId(item.menu) ? { menu: item.menu, context: normalizeMenuContext(item.menu, context()) } : undefined;
    await executePluginCommand(item.pluginId, item.command, invocation);
  } catch (error) {
    if (error instanceof MenuPayloadTooLargeError) {
      ElMessage.error(t("pluginContrib.menus.contextTooLarge", { name, limit: MENU_LIMITS.passContextMaxBytes / 1024 }));
      return;
    }
    if (error instanceof MenuContextFieldTooLongError) {
      ElMessage.error(t("pluginContrib.menus.fieldTooLong", { name }));
      return;
    }
    if (error instanceof MenuTargetUnavailableError) {
      ElMessage.error(t("pluginContrib.menus.targetUnavailable", { name }));
      return;
    }
    ElMessage.error(t("pluginContrib.menus.runFailed", { name, message: error instanceof Error ? error.message : String(error) }));
  }
}

function paletteOverrides(menuItems: readonly PluginMenuItem[]) {
  const overrides = new Map<string, PluginCommandPaletteOverride>();
  for (const item of menuItems) {
    if (item.menu !== "commandPalette") continue;
    overrides.set(menuItemCommandKey(item), { hidden: item.hidden, group: item.group, order: item.order });
  }
  return overrides;
}

/** Registers the menus consumer and the Monaco editor menu bridge. */
export function registerMenuContributionConsumer(): () => void {
  const disposeMonaco = installMonacoPluginMenus({
    items,
    state: pluginCommandState,
    platform: currentKeyboardPlatform,
    run: runPluginMenuItem,
    available: (item) => availableCommand(item) !== undefined,
    title: (item) => menuItemTitle(item, useVueI18n().localeChain.value),
  });
  const unregister = registerContributionConsumer({
    point: MENU_CONTRIBUTION_POINT,
    apply: (contributions) => {
      const result = parseMenuContributions(contributions);
      items.value = result.items;
      lastIssues = result.issues;
      lastSyncError = undefined;
      setPluginCommandPaletteOverrides(paletteOverrides(result.items));
      const tray = result.items.filter((item) => item.menu === "tray").map(({ pluginId, command }) => ({ pluginId, command }));
      const stopTray = tray.length
        ? lazyContributionApply(
          () => import("@/services/plugin-tray").then((module) => () => module.startPluginTraySync(tray)),
          (message) => (lastSyncError = message),
        )(contributions)
        : undefined;
      return () => {
        stopTray?.();
        items.value = [];
        setPluginCommandPaletteOverrides(new Map());
      };
    },
    onError: (message) => {
      lastSyncError = message;
    },
  });
  return () => {
    unregister();
    disposeMonaco();
  };
}
