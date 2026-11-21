export interface LocalizedTitle {
  title: string;
  titleLocalized?: Record<string, string>;
}

function languageOf(tag: string): string {
  return tag.split("-")[0] ?? "";
}

function ownTitle(titles: Record<string, string> | undefined, keys: readonly string[]): string | undefined {
  if (!titles) return undefined;
  for (const key of keys) {
    const value: unknown = key && Object.prototype.hasOwnProperty.call(titles, key) ? titles[key] : undefined;
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  return undefined;
}

/**
 * Resolves a plugin-contributed title. `locale` is a tag or a locale chain
 * (`[packTag, base]`, see useVueI18n().localeChain). Order: the plugin's own
 * `titleLocalized` for the first tag, then `packTitle` (another plugin's
 * language pack translation), then the plugin's own `titleLocalized` for the
 * remaining tags, then `title`. A plugin's own translations therefore always
 * win over a language pack. A tag's bare language (`pt` for `pt-BR`) is tried
 * right after the tag unless it is the base language of a later tag, so
 * `zh-TW` never picks up a plugin's `zh` title before the pack's.
 */
export function resolveLocalizedTitle(
  contribution: LocalizedTitle,
  locale: string | readonly string[],
  packTitle?: string,
): string {
  const chain = typeof locale === "string" ? [locale] : locale;
  const titles = contribution.titleLocalized;
  const [first, ...rest] = chain;
  const laterLanguages = new Set(rest.map(languageOf));
  if (first) {
    const language = languageOf(first);
    const keys = language !== first && !laterLanguages.has(language) ? [first, language] : [first];
    const own = ownTitle(titles, keys);
    if (own) return own;
  }
  const pack = packTitle?.trim();
  if (pack) return pack;
  for (const tag of rest) {
    const own = ownTitle(titles, [tag, languageOf(tag)]);
    if (own) return own;
  }
  return contribution.title;
}

/**
 * Title of a host-resolved plugin process launch (ResolvedProcessLaunch). The
 * host sends titleLocalized as a per-language map; any other shape from the
 * bridge falls back to title, then entryKey, and never throws.
 */
export function processLaunchTitle(
  launch: { title?: unknown; titleLocalized?: unknown; entryKey: string },
  locale: string | readonly string[],
): string {
  const title = typeof launch.title === "string" ? launch.title.trim() : "";
  const localized = launch.titleLocalized;
  if (typeof localized === "string" && localized.trim()) return localized.trim();
  const titles = localized && typeof localized === "object" && !Array.isArray(localized)
    ? Object.fromEntries(Object.entries(localized).filter((entry): entry is [string, string] => typeof entry[1] === "string"))
    : undefined;
  return resolveLocalizedTitle({ title, titleLocalized: titles }, locale).trim() || launch.entryKey;
}
