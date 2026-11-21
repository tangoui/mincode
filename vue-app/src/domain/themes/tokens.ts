/**
 * Contract of the `themes` contribution point: the MinCode semantic tokens a
 * plugin theme may set, and the Monaco color ids / xterm keys it may use.
 * Mirrors `points.themes` in plugins/api/contribution-points.json
 * (scripts/theme-contributions.test.mjs asserts parity). Pure data.
 */

export const THEME_CONTRIBUTION_POINT = "themes";

export const THEME_REQUIRED_TOKENS = [
  "canvas",
  "surface",
  "sidebar",
  "border",
  "border-strong",
  "muted",
  "hover",
  "accent",
  "accent-hover",
  "accent-soft",
  "text",
  "text-regular",
  "text-muted",
  "text-disabled",
  "text-on-accent",
] as const;

export const THEME_OPTIONAL_TOKENS = [
  "success",
  "warning",
  "danger",
  "code-surface",
  "code-surface-strong",
  "code-border",
  "code-text",
  "code-text-muted",
] as const;

export type ThemeRequiredToken = (typeof THEME_REQUIRED_TOKENS)[number];
export type ThemeOptionalToken = (typeof THEME_OPTIONAL_TOKENS)[number];
export type ThemeToken = ThemeRequiredToken | ThemeOptionalToken;

export const THEME_TOKENS: readonly ThemeToken[] = [...THEME_REQUIRED_TOKENS, ...THEME_OPTIONAL_TOKENS];

/**
 * Every CSS custom property a plugin theme may write on the document root:
 * the semantic tokens plus the derived `--app-accent-rgb`. Switching themes
 * removes exactly these.
 */
export const THEME_CSS_PROPERTIES: readonly string[] = [
  ...THEME_TOKENS.map((token) => `--app-${token}`),
  "--app-accent-rgb",
];

export const THEME_MONACO_COLORS = [
  "editor.background",
  "editor.foreground",
  "editor.lineHighlightBackground",
  "editor.lineHighlightBorder",
  "editor.selectionBackground",
  "editor.inactiveSelectionBackground",
  "editor.selectionHighlightBackground",
  "editor.wordHighlightBackground",
  "editor.wordHighlightStrongBackground",
  "editor.findMatchBackground",
  "editor.findMatchHighlightBackground",
  "editor.rangeHighlightBackground",
  "editorCursor.foreground",
  "editorWhitespace.foreground",
  "editorIndentGuide.background1",
  "editorIndentGuide.activeBackground1",
  "editorLineNumber.foreground",
  "editorLineNumber.activeForeground",
  "editorGutter.background",
  "editorBracketMatch.background",
  "editorBracketMatch.border",
  "editorError.foreground",
  "editorWarning.foreground",
  "editorInfo.foreground",
  "editorWidget.background",
  "editorWidget.border",
  "editorSuggestWidget.background",
  "editorSuggestWidget.border",
  "editorSuggestWidget.foreground",
  "editorSuggestWidget.selectedBackground",
  "editorHoverWidget.background",
  "editorHoverWidget.border",
  "scrollbarSlider.background",
  "scrollbarSlider.hoverBackground",
  "scrollbarSlider.activeBackground",
  "minimap.background",
  "diffEditor.insertedTextBackground",
  "diffEditor.removedTextBackground",
  "input.background",
  "input.foreground",
  "input.border",
  "focusBorder",
] as const;

export const THEME_TERMINAL_KEYS = [
  "foreground",
  "background",
  "cursor",
  "cursorAccent",
  "selectionBackground",
  "selectionForeground",
  "selectionInactiveBackground",
  "black",
  "red",
  "green",
  "yellow",
  "blue",
  "magenta",
  "cyan",
  "white",
  "brightBlack",
  "brightRed",
  "brightGreen",
  "brightYellow",
  "brightBlue",
  "brightMagenta",
  "brightCyan",
  "brightWhite",
] as const;

export type ThemeTerminalKey = (typeof THEME_TERMINAL_KEYS)[number];

export const THEME_LIMITS = Object.freeze({
  maxThemesPerPlugin: 16,
  tokenRulesMax: 500,
  titleMaxRunes: 200,
  ruleTokenMaxLength: 128,
});

/** Contrast floors (WCAG ratio) a theme must meet; mirrors contrib_themes.go. */
export const THEME_CONTRAST_FLOORS: ReadonlyArray<{ foreground: ThemeToken; background: ThemeToken; min: number }> = [
  { foreground: "text", background: "surface", min: 4.5 },
  { foreground: "text-muted", background: "surface", min: 3 },
  { foreground: "text-on-accent", background: "accent", min: 3 },
  { foreground: "danger", background: "surface", min: 3 },
];

/** Built-in `--app-danger` (foundation.css), used for the danger floor when a theme omits it. */
export const THEME_DEFAULT_DANGER = "#d14343";
