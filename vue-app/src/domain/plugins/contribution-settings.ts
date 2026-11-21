/**
 * Syntax-only normalisers for settings owned by Phase D contribution points
 * (`localizations`, `fileIcons`). They never check that a plugin or entry
 * exists: an unavailable selection keeps its stored value and the consumer
 * falls back at apply time, like the keymap preset setting.
 */

/** Built-in file icon theme; the default. */
export const DEFAULT_FILE_ICON_THEME = "mincode";

const localeTagPattern = /^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8}){0,7}$/;
const pluginIdPattern = /^[a-z0-9][a-z0-9._-]{1,127}$/;
const themeIdPattern = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/;

/** A BCP 47-shaped tag (at most 64 characters), or undefined. */
export function normalizeUiLocale(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  return trimmed.length <= 64 && localeTagPattern.test(trimmed) ? trimmed : undefined;
}

/** A plugin ID (manifest syntax), or undefined. */
export function normalizeUiLocalePlugin(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  return pluginIdPattern.test(trimmed) ? trimmed : undefined;
}

/** "mincode" or a syntactically valid contributed theme ID. */
export function normalizeFileIconTheme(value: unknown): string {
  if (typeof value !== "string") return DEFAULT_FILE_ICON_THEME;
  const trimmed = value.trim();
  return themeIdPattern.test(trimmed) ? trimmed : DEFAULT_FILE_ICON_THEME;
}
