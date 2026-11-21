/**
 * Syntax of the editor theme preference: "follow" (the UI theme's editor
 * colors), "custom" (the user's own colors) or a contributed theme id
 * `<pluginId>.<name>` (contributes.editorThemes). Kept apart from the
 * validator so the Monaco theme resolution (services/ui-theme.ts, entry
 * chunk) does not pull in the contribution contract.
 */
export const EDITOR_THEMES_CONTRIBUTION_POINT = "editorThemes";

export const EDITOR_THEME_FOLLOW = "follow";
export const EDITOR_THEME_CUSTOM = "custom";

const PLUGIN_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const NAME_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;

/** True for a syntactically valid contributed theme id `<pluginId>.<name>` (split on the last dot). */
export function isPluginEditorThemeId(value: unknown): value is string {
  if (typeof value !== "string") return false;
  const index = value.lastIndexOf(".");
  if (index <= 0) return false;
  return PLUGIN_ID_PATTERN.test(value.slice(0, index)) && NAME_PATTERN.test(value.slice(index + 1));
}

/** "follow", "custom" or a syntactically valid plugin theme id; anything else follows the UI theme. */
export function normalizeEditorThemePreference(value: unknown): string {
  if (value === EDITOR_THEME_CUSTOM) return EDITOR_THEME_CUSTOM;
  return isPluginEditorThemeId(value) ? value : EDITOR_THEME_FOLLOW;
}
