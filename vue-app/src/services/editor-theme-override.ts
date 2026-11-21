/**
 * The Monaco theme chosen by the editor theme setting, when it overrides the
 * UI theme's editor colors (services/editor-theme.ts sets it once the theme
 * is defined with Monaco). Kept dependency-free so services/ui-theme.ts can
 * read it without importing the editor theme runtime.
 */
let override: string | undefined;

export function editorThemeOverride(): string | undefined {
  return override;
}

export function setEditorThemeOverride(name: string | undefined): void {
  override = name;
}
