import { hydrateSettingsSnapshot } from "@/services/settings-persistence";
import { editorThemeOverride } from "@/services/editor-theme-override";

export const UI_THEME_STORAGE_KEY = "app_settings_v2";
export const UI_THEME_CHANGED_EVENT = "app:ui-theme-changed";
export type BuiltinUiTheme = "system" | "dark" | "light" | "graphite" | "midnight" | "paper" | "cloud-blue" | "arco" | "arco-dark";
export type UiTheme = BuiltinUiTheme;
export type UiThemeKind = "light" | "dark";

const UI_THEMES: ReadonlySet<BuiltinUiTheme> = new Set([
  "system",
  "dark",
  "light",
  "graphite",
  "midnight",
  "paper",
  "cloud-blue",
  "arco",
  "arco-dark",
]);
const LIGHT_UI_THEMES = new Set<UiTheme>(["light", "paper", "cloud-blue", "arco"]);

export function listBuiltinUiThemes(): BuiltinUiTheme[] {
  return [...UI_THEMES];
}

export function isBuiltinUiTheme(theme: unknown): theme is BuiltinUiTheme {
  return typeof theme === "string" && UI_THEMES.has(theme as BuiltinUiTheme);
}

export function normalizeUiTheme(theme: unknown, kindHint?: unknown): UiTheme {
  if (isBuiltinUiTheme(theme)) return theme;
  return normalizeUiThemeKind(kindHint) ?? "light";
}

export function normalizeUiThemeKind(kind: unknown): UiThemeKind | undefined {
  return kind === "light" || kind === "dark" ? kind : undefined;
}

let currentPreference: UiTheme = "light";

export function currentUiThemePreference(): UiTheme {
  return currentPreference;
}

function resolvedBuiltinTheme(theme: BuiltinUiTheme): Exclude<BuiltinUiTheme, "system"> {
  if (theme !== "system") return theme;
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

export function resolveUiThemeKind(theme: unknown): UiThemeKind {
  return LIGHT_UI_THEMES.has(resolvedBuiltinTheme(normalizeUiTheme(theme))) ? "light" : "dark";
}

export function resolveMonacoEditorTheme(theme: unknown): string {
  return editorThemeOverride() ?? (resolveUiThemeKind(theme) === "light" ? "vs" : "vs-dark");
}

function readStoredSettings(): { uiTheme?: unknown; uiThemeKind?: unknown } | undefined {
  try {
    const raw = window.localStorage.getItem(UI_THEME_STORAGE_KEY);
    return raw ? (JSON.parse(raw) as { uiTheme?: unknown; uiThemeKind?: unknown }) : undefined;
  } catch {
    return undefined;
  }
}

export function readStoredUiTheme(): UiTheme {
  const stored = readStoredSettings();
  return normalizeUiTheme(stored?.uiTheme, stored?.uiThemeKind);
}

// ---------------------------------------------------------------------------
// Application

let stopSystemThemeListener: (() => void) | undefined;

export function applyUiTheme(theme: UiTheme): void {
  const preference = normalizeUiTheme(theme);
  currentPreference = preference;
  stopSystemThemeListener?.();
  stopSystemThemeListener = undefined;
  if (preference === "system" && window.matchMedia) {
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const listener = () => applyUiTheme("system");
    media.addEventListener("change", listener, { once: true });
    stopSystemThemeListener = () => media.removeEventListener("change", listener);
  }
  const root = document.documentElement;
  const detail = resolvedBuiltinTheme(preference);
  const kind = LIGHT_UI_THEMES.has(detail) ? "light" : "dark";
  root.dataset.theme = detail;
  root.dataset.themePreference = preference;
  root.dataset.themeKind = kind;
  root.style.colorScheme = kind;
  root.classList.toggle("dark", kind === "dark");
  window.dispatchEvent(new CustomEvent<UiTheme>(UI_THEME_CHANGED_EVENT, { detail }));
}

/** Restores the native desktop value as well as the browser-preview fallback. */
export async function hydrateUiTheme(): Promise<UiTheme> {
  const stored = await hydrateSettingsSnapshot<{ uiTheme?: unknown; uiThemeKind?: unknown } & Record<string, unknown>>();
  const theme = normalizeUiTheme(stored?.uiTheme ?? readStoredUiTheme(), stored?.uiThemeKind);
  applyUiTheme(theme);
  return theme;
}
