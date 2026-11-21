export const EDITOR_THEME_FOLLOW = "follow";
export const EDITOR_THEME_CUSTOM = "custom";

export function normalizeEditorThemePreference(value: unknown): string {
  return value === EDITOR_THEME_CUSTOM ? EDITOR_THEME_CUSTOM : EDITOR_THEME_FOLLOW;
}
