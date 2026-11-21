/**
 * Pure model of UI language packs (the `localizations` contribution point).
 * The Go host (backend/plugin/contrib_localizations.go) validates
 * entry structure, language tags and characters and serves canonical asset
 * bytes; this module re-checks the snapshot entries, decides which pack
 * translations the host may use and resolves locale/title chains. Nothing
 * here touches Vue or IPC.
 *
 * A pack translation is kept only when:
 * - its key exists in the host catalogue (the English messages),
 * - the key is not protected (i18n/protected-keys.ts),
 * - its `{placeholder}` multiset equals the English source's,
 * - it adds no URL-like token and no line break the source lacks,
 * - it is at most 4 × the source length + 64 characters, and
 * - it contains no control, bidirectional or zero-width character.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { isProtectedMessageKey } from "@/i18n/protected-keys";

export const LOCALIZATIONS_CONTRIBUTION_POINT = "localizations";

/** Built-in UI languages. Packs only add other tags; `zh`/`en` are also the fallback bases. */
export type BaseLocale = "zh" | "en";

const BUILTIN_LOCALES = ["zh", "zh-cn", "zh-hans", "zh-hans-cn", "en", "en-us"];
const LOCALE_TAG_PATTERN = /^[a-z]{2,3}(-[A-Z][a-z]{3})?(-(?:[A-Z]{2}|[0-9]{3}))?$/;
const ASSET_KEY_PATTERN = /^[0-9a-f]{64}$/;
const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{1,127}$/;
const CONTRIBUTION_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const PLACEHOLDER_PATTERN = /\{[A-Za-z0-9_.-]+\}/g;
const URL_PATTERN = /:\/\/|www\.|mailto:/i;
const NAME_MAX_LENGTH = 64;
const TITLE_MAX_LENGTH = 200;
const MAX_PACKS = 256;

export interface LocalePackEntry {
  pluginId: string;
  pluginName: string;
  locale: string;
  languageName: string;
  localizedLanguageName: string;
  messages?: { asset: string; count: number };
  pluginTitles?: { asset: string; plugins: string[] };
}

export interface LocalePackIssue {
  pluginId: string;
  locale?: string;
  message: string;
}

export interface LocalePackSelection {
  tag: string;
  /** Plugin that provides the pack; undefined picks the first provider by plugin ID. */
  pluginId?: string;
}

export interface OverlayDropCounts {
  unknown: number;
  protected: number;
  placeholder: number;
  url: number;
  length: number;
  newline: number;
  hidden: number;
}

export interface BuiltOverlay {
  messages: Map<string, string>;
  dropped: OverlayDropCounts;
}

export interface PluginTitleGroups {
  commands: Readonly<Record<string, string>>;
  views: Readonly<Record<string, string>>;
}

/** Other plugins' command/view titles provided by the active pack, keyed by plugin ID. */
export type PackPluginTitles = ReadonlyMap<string, PluginTitleGroups>;

// Control (except handled line breaks), line/paragraph separators, bidi
// controls and zero-width characters, as code point ranges.
const HIDDEN_RANGES: ReadonlyArray<readonly [number, number]> = [
  [0x00, 0x1f],
  [0x7f, 0x9f],
  [0x200b, 0x200f],
  [0x2028, 0x202e],
  [0x2060, 0x2060],
  [0x2066, 0x2069],
  [0xfeff, 0xfeff],
];

/** True when `text` contains a hidden character; `\n` is allowed when `multiline`. */
export function hasHiddenCharacter(text: string, multiline: boolean): boolean {
  for (const character of text) {
    const code = character.codePointAt(0) ?? 0;
    if (multiline && code === 0x0a) continue;
    if (HIDDEN_RANGES.some(([start, end]) => code >= start && code <= end)) return true;
  }
  return false;
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function singleLine(value: unknown, max: number): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  if (!trimmed || [...trimmed].length > max || hasHiddenCharacter(trimmed, false)) return undefined;
  return trimmed;
}

/** True for a pack language tag (BCP 47 subset) that is not built in. */
export function isPackLocaleTag(tag: string): boolean {
  return LOCALE_TAG_PATTERN.test(tag) && !BUILTIN_LOCALES.includes(tag.toLowerCase());
}

/** Re-checks the snapshot entries of every plugin; invalid entries become issues. */
export function parseLocalePackContributions(contributions: readonly PointContribution[]): {
  packs: LocalePackEntry[];
  issues: LocalePackIssue[];
} {
  const packs: LocalePackEntry[] = [];
  const issues: LocalePackIssue[] = [];
  const sorted = [...contributions].sort((a, b) => a.pluginId.localeCompare(b.pluginId));
  for (const contribution of sorted) {
    const seen = new Set<string>();
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, locale: issue.id, message: issue.message });
    }
    for (const raw of contribution.point.entries) {
      const entry = objectValue(raw);
      const locale = typeof entry?.locale === "string" ? entry.locale : "";
      const issue = (message: string) => issues.push({ pluginId: contribution.pluginId, ...(locale ? { locale } : {}), message });
      if (!entry || !isPackLocaleTag(locale)) {
        issue("invalid or built-in language tag");
        continue;
      }
      if (seen.has(locale)) {
        issue("duplicate language pack locale");
        continue;
      }
      const languageName = singleLine(entry.languageName, NAME_MAX_LENGTH);
      const localizedLanguageName = singleLine(entry.localizedLanguageName, NAME_MAX_LENGTH);
      if (!languageName || !localizedLanguageName) {
        issue("invalid language name");
        continue;
      }
      const messages = objectValue(entry.messages);
      const titles = objectValue(entry.pluginTitles);
      const pack: LocalePackEntry = {
        pluginId: contribution.pluginId,
        pluginName: singleLine(contribution.pluginName, 128) ?? contribution.pluginId,
        locale,
        languageName,
        localizedLanguageName,
      };
      if (messages && typeof messages.asset === "string" && ASSET_KEY_PATTERN.test(messages.asset)) {
        const count = typeof messages.count === "number" && Number.isSafeInteger(messages.count) && messages.count > 0 ? messages.count : 0;
        pack.messages = { asset: messages.asset, count };
      }
      if (titles && typeof titles.asset === "string" && ASSET_KEY_PATTERN.test(titles.asset)) {
        const plugins = Array.isArray(titles.plugins)
          ? titles.plugins.filter((id): id is string => typeof id === "string" && PLUGIN_ID_PATTERN.test(id))
          : [];
        pack.pluginTitles = { asset: titles.asset, plugins };
      }
      if (!pack.messages && !pack.pluginTitles) {
        issue("language pack has no usable assets");
        continue;
      }
      seen.add(locale);
      packs.push(pack);
      if (packs.length >= MAX_PACKS) return { packs, issues };
    }
  }
  return { packs, issues };
}

/** The pack for a stored selection: the named provider, else the first provider of the tag. */
export function findLocalePack(
  packs: readonly LocalePackEntry[],
  selection: LocalePackSelection | null | undefined,
): LocalePackEntry | undefined {
  if (!selection) return undefined;
  const candidates = packs.filter((pack) => pack.locale === selection.tag);
  if (selection.pluginId) return candidates.find((pack) => pack.pluginId === selection.pluginId);
  return candidates[0];
}

/** Sorted `{placeholder}` tokens of a message (a multiset). */
export function placeholderMultiset(text: string): string[] {
  return (text.match(PLACEHOLDER_PATTERN) ?? []).sort();
}

function sameMultiset(a: readonly string[], b: readonly string[]) {
  return a.length === b.length && a.every((value, index) => value === b[index]);
}

function emptyDropCounts(): OverlayDropCounts {
  return { unknown: 0, protected: 0, placeholder: 0, url: 0, length: 0, newline: 0, hidden: 0 };
}

/** Total number of dropped pack messages. */
export function droppedTotal(dropped: OverlayDropCounts): number {
  return Object.values(dropped).reduce((sum, value) => sum + value, 0);
}

/**
 * Builds the message overlay from a pack's messages asset. `catalogue` is the
 * host's English messages (the placeholder and length reference).
 */
export function buildOverlay(catalogue: Readonly<Record<string, string>>, pack: unknown): BuiltOverlay {
  const messages = new Map<string, string>();
  const dropped = emptyDropCounts();
  const source = objectValue(pack);
  if (!source) return { messages, dropped };
  for (const [key, value] of Object.entries(source)) {
    const reference = Object.prototype.hasOwnProperty.call(catalogue, key) ? catalogue[key] : undefined;
    if (typeof reference !== "string" || typeof value !== "string" || !value.trim()) {
      dropped.unknown++;
    } else if (isProtectedMessageKey(key)) {
      dropped.protected++;
    } else if (hasHiddenCharacter(value, true)) {
      dropped.hidden++;
    } else if (!sameMultiset(placeholderMultiset(value), placeholderMultiset(reference))) {
      dropped.placeholder++;
    } else if (URL_PATTERN.test(value) && !URL_PATTERN.test(reference)) {
      dropped.url++;
    } else if (value.includes("\n") && !reference.includes("\n")) {
      dropped.newline++;
    } else if (value.length > reference.length * 4 + 64) {
      dropped.length++;
    } else {
      messages.set(key, value);
    }
  }
  return { messages, dropped };
}

/** Number of catalogue keys a pack may translate (every non-protected key). */
export function translatableKeyCount(catalogue: Readonly<Record<string, string>>): number {
  return Object.keys(catalogue).filter((key) => !isProtectedMessageKey(key)).length;
}

/** Whole-percent coverage of the translatable catalogue. */
export function localeCoverage(translated: number, translatable: number): number {
  if (translatable <= 0 || translated <= 0) return 0;
  return Math.min(100, Math.floor((translated / translatable) * 100));
}

/**
 * Parses a pluginTitles asset. The pack's own plugin is skipped: a plugin
 * localises its own titles through `titleLocalized` only.
 */
export function parsePluginTitlesAsset(value: unknown, packPluginId: string): Map<string, PluginTitleGroups> {
  const result = new Map<string, PluginTitleGroups>();
  const source = objectValue(value);
  if (!source) return result;
  for (const [pluginId, rawGroups] of Object.entries(source)) {
    if (pluginId === packPluginId || !PLUGIN_ID_PATTERN.test(pluginId)) continue;
    const groups = objectValue(rawGroups);
    if (!groups) continue;
    const parsed: PluginTitleGroups = { commands: parseTitleGroup(groups.commands), views: parseTitleGroup(groups.views) };
    if (Object.keys(parsed.commands).length || Object.keys(parsed.views).length) result.set(pluginId, parsed);
  }
  return result;
}

function parseTitleGroup(value: unknown): Record<string, string> {
  const group = objectValue(value);
  const titles: Record<string, string> = Object.create(null) as Record<string, string>;
  if (!group) return titles;
  for (const [id, title] of Object.entries(group)) {
    const text = CONTRIBUTION_ID_PATTERN.test(id) ? singleLine(title, TITLE_MAX_LENGTH) : undefined;
    if (text) titles[id] = text;
  }
  return titles;
}

/** `[packTag, base]` when a pack is active, else `[base]`. */
export function resolveLocaleChain(tag: string | undefined, base: BaseLocale): string[] {
  return tag && tag !== base ? [tag, base] : [base];
}

/** The tag for Intl formatters and `<html lang>`. */
export function intlLocaleFor(tag: string | undefined, base: BaseLocale): string {
  if (tag) return tag;
  return base === "zh" ? "zh-CN" : "en-US";
}

/**
 * Install preview: the UI languages a manifest declares, as
 * `localizedLanguageName (languageName, tag)` (manifest text, rendered as
 * text only). Empty when the manifest declares no localizations.
 */
export function localizationPreviewSummary(contributes: unknown): string[] {
  if (!contributes || typeof contributes !== "object" || Array.isArray(contributes)) return [];
  const entries = (contributes as Record<string, unknown>)[LOCALIZATIONS_CONTRIBUTION_POINT];
  if (!Array.isArray(entries)) return [];
  const labels: string[] = [];
  for (const entry of entries) {
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) continue;
    const record = entry as Record<string, unknown>;
    const tag = typeof record.locale === "string" ? record.locale.trim() : "";
    if (!tag) continue;
    const name = typeof record.languageName === "string" ? record.languageName.trim() : "";
    const localized = typeof record.localizedLanguageName === "string" ? record.localizedLanguageName.trim() : "";
    const detail = name ? `${name}, ${tag}` : tag;
    labels.push(localized ? `${localized} (${detail})` : detail);
  }
  return labels;
}
