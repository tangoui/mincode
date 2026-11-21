/**
 * Pure model of the `settingsPages` contribution point (process plugins,
 * host 0.3.0): one page per plugin that shows one of the plugin's own views
 * under Settings > Plugins. The Go host
 * (backend/plugin/contrib_settings_pages.go) validated the entries
 * (view exists and is not floating, title limits); the snapshot entries are
 * re-checked here before the settings panel uses them. Nothing here touches
 * Vue or IPC.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { resolvePluginTitle } from "@/domain/plugins/title";

export const SETTINGS_PAGES_CONTRIBUTION_POINT = "settingsPages";

// Mirrors `points.settingsPages` in plugins/api/contribution-points.json
// (vue-app/scripts/menus-contributions.test.mjs keeps them in sync).
export const SETTINGS_PAGE_MAX_ENTRIES = 1;
export const SETTINGS_PAGE_TITLE_MAX_RUNES = 48;
export const SETTINGS_PAGE_LOCALES_MAX = 16;

const ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
// eslint-disable-next-line no-control-regex -- host titles are single-line text without control or bidi characters
const UNSAFE_TEXT = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069]/;

export interface PluginSettingsPage {
  pluginId: string;
  pluginName: string;
  id: string;
  /** Local view id (contributes.views[].id of the same plugin). */
  view: string;
  /** Entry title (the host defaults it to the view title); "" means "use the view title". */
  title: string;
  titleLocalized?: Record<string, string>;
  activeInWorkspace: boolean;
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function validTitle(value: string): boolean {
  return [...value].length <= SETTINGS_PAGE_TITLE_MAX_RUNES && !UNSAFE_TEXT.test(value);
}

function localizedTitles(value: unknown): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const titles: Record<string, string> = {};
  for (const [locale, title] of Object.entries(record).slice(0, SETTINGS_PAGE_LOCALES_MAX)) {
    if (typeof title !== "string") continue;
    const trimmed = title.trim();
    if (trimmed && validTitle(trimmed)) titles[locale] = trimmed;
  }
  return Object.keys(titles).length ? titles : undefined;
}

/**
 * The settings page of one snapshot contribution (at most one; later entries
 * are ignored). Entries without a valid id or view, or with an invalid title,
 * are dropped; declarative plugins contribute nothing.
 */
export function parseSettingsPageEntries(contribution: PointContribution): PluginSettingsPage[] {
  if (contribution.runtime !== "process") return [];
  for (const raw of contribution.point.entries.slice(0, SETTINGS_PAGE_MAX_ENTRIES)) {
    const entry = objectValue(raw);
    if (!entry) continue;
    const id = typeof entry.id === "string" ? entry.id.trim() : "";
    const view = typeof entry.view === "string" ? entry.view.trim() : "";
    if (!ID_PATTERN.test(id) || !ID_PATTERN.test(view)) continue;
    const title = typeof entry.title === "string" ? entry.title.trim() : "";
    if (!validTitle(title)) continue;
    const titleLocalized = localizedTitles(entry.titleLocalized);
    return [
      {
        pluginId: contribution.pluginId,
        pluginName: contribution.pluginName,
        id,
        view,
        title,
        ...(titleLocalized ? { titleLocalized } : {}),
        activeInWorkspace: contribution.activeInWorkspace,
      },
    ];
  }
  return [];
}

// A title inherited from the view: unsafe characters removed, cut to 48 runes
// (the host's settingsPageInheritedTitle).
const UNSAFE_TEXT_GLOBAL = new RegExp(UNSAFE_TEXT.source, "g");
function inheritedTitle(value: unknown): string {
  return typeof value === "string" ? [...value.replace(UNSAFE_TEXT_GLOBAL, "")].slice(0, SETTINGS_PAGE_TITLE_MAX_RUNES).join("").trim() : "";
}

function inheritedTitles(value: unknown): Record<string, string> | undefined {
  const record = objectValue(value);
  return record ? localizedTitles(Object.fromEntries(Object.entries(record).map(([locale, title]) => [locale, inheritedTitle(title)]))) : undefined;
}

/**
 * Install preview: the title of the manifest's settings page (its own title,
 * else the referenced view's title, else the view id), or undefined when the
 * manifest declares none or its title is invalid. Titles pass the same checks
 * as the snapshot parser (single line, 48 runes, 16 locales); strict
 * validation stays the host's job at install.
 */
export function settingsPagePreviewTitle(contributes: unknown, locale: readonly string[]): string | undefined {
  const record = objectValue(contributes);
  const entries: unknown[] = Array.isArray(record?.settingsPages) ? record.settingsPages : [];
  for (const raw of entries.slice(0, SETTINGS_PAGE_MAX_ENTRIES)) {
    const entry = objectValue(raw);
    const view = typeof entry?.view === "string" ? entry.view.trim() : "";
    if (!entry || !view) continue;
    const own = typeof entry.title === "string" ? entry.title.trim() : "";
    if (!validTitle(own)) continue;
    if (own) return resolvePluginTitle({ title: own, titleLocalized: localizedTitles(entry.titleLocalized) }, locale);
    const views: unknown[] = Array.isArray(record?.views) ? record.views : [];
    const target = views.map(objectValue).find((candidate) => candidate?.id === view);
    const viewTitle = inheritedTitle(target?.title);
    return viewTitle ? resolvePluginTitle({ title: viewTitle, titleLocalized: inheritedTitles(target?.titleLocalized) }, locale) : view;
  }
  return undefined;
}
