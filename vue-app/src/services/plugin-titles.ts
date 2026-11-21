import { shallowRef } from "vue";
import type { PackPluginTitles } from "@/domain/i18n/locale-pack";
import { resolvePluginTitle, type PluginTitle } from "@/domain/plugins/title";
import { useVueI18n } from "@/i18n/messages";

interface PluginViewTitle extends PluginTitle {
  pluginId: string;
  id: string;
  target?: { type: string; viewId: string };
  /** Views only: allowlisted icon name and plugin display name. */
  icon?: string;
  pluginName?: string;
}

// Catalog metadata is global, like installed plugins; never persist translated labels.
const viewTitles = shallowRef<readonly PluginViewTitle[]>([]);
const commandTitles = shallowRef<readonly PluginViewTitle[]>([]);
// Other plugins' titles from the active language pack (contributes.localizations).
const packTitles = shallowRef<PackPluginTitles | null>(null);
const { localeChain } = useVueI18n();

export function setPluginViewTitles(views: readonly PluginViewTitle[]) {
  viewTitles.value = views;
}

export function setPluginCommandTitles(commands: readonly PluginViewTitle[]) {
  commandTitles.value = commands;
}

/** Installs (or with null removes) the active language pack's plugin titles. */
export function setPluginTitleOverlay(titles: PackPluginTitles | null) {
  packTitles.value = titles;
}

function packTitle(contribution: PluginTitle, kind: "command" | "view"): string | undefined {
  const reference = contribution as Partial<PluginViewTitle>;
  if (typeof reference.pluginId !== "string" || typeof reference.id !== "string") return undefined;
  const groups = packTitles.value?.get(reference.pluginId);
  const titles = kind === "command" ? groups?.commands : groups?.views;
  return titles && Object.prototype.hasOwnProperty.call(titles, reference.id) ? titles[reference.id] : undefined;
}

export function pluginCommandTitle(commandId: string): string | undefined {
  const command = commandTitles.value.find(command => `plugin:${command.pluginId}:${command.id}` === commandId);
  return command ? localizedPluginTitle(command, "command") : undefined;
}

/**
 * Display title of a plugin view (default) or command. A language pack may
 * translate it only when the plugin has no own title for the pack language.
 * Never use this for execution confirmations: they show the pack-free title.
 */
export function localizedPluginTitle(contribution: PluginTitle, kind: "command" | "view" = "view") {
  return resolvePluginTitle(contribution, localeChain.value, packTitle(contribution, kind));
}

/**
 * Cached display metadata of a contributed view (from the last view catalog
 * read), for host chrome such as the loading skeleton. No IPC; undefined when
 * the view is not in the cache yet.
 */
export function pluginViewMetadata(pluginId: string, viewId: string): { title: string; pluginName?: string; icon?: string } | undefined {
  const view = viewTitles.value.find(item => item.pluginId === pluginId && item.id === viewId);
  if (!view) return undefined;
  return {
    title: localizedPluginTitle(view),
    pluginName: typeof view.pluginName === "string" && view.pluginName.trim() ? view.pluginName.trim() : undefined,
    icon: typeof view.icon === "string" && view.icon.trim() ? view.icon.trim() : undefined,
  };
}

export function pluginTabTitle(tab: {
  view: string;
  title?: string;
  pluginView?: { pluginId: string; viewId: string };
}): string | undefined {
  if (tab.view !== "plugin-view" || !tab.pluginView) return tab.title;
  const target = tab.pluginView;
  const view = viewTitles.value.find(view => view.pluginId === target.pluginId && view.id === target.viewId);
  if (!view) return tab.title;
  // A user-authored tab name wins; only replace known contribution labels.
  const contributions = [view, ...commandTitles.value.filter(command =>
    command.pluginId === target.pluginId && command.target?.type === "view"
    && command.target.viewId === target.viewId
  )];
  if (tab.title && !contributions.some(contribution =>
    [contribution.title, ...Object.values(contribution.titleLocalized ?? {}),
      packTitle(contribution, contribution === view ? "view" : "command") ?? ""]
      .some(title => title.trim() && title.trim() === tab.title?.trim())
  )) return tab.title;
  return localizedPluginTitle(view);
}
