import { hydrateSettingsSnapshot } from "@/services/settings-persistence";
import { registerDesktopBrowserSnapshotKeys, writePersistedBrowserSnapshot } from "@/services/persistent-store";
import {
  isPluginThemeId,
  monacoPluginThemeName,
  pluginThemeCssProperties,
  validatePluginTheme,
  type PluginThemeKind,
  type PluginThemeRecord,
} from "@/domain/themes/plugin-theme";
import { THEME_CSS_PROPERTIES } from "@/domain/themes/tokens";
import { editorThemeOverride } from "@/services/editor-theme-override";

export const UI_THEME_STORAGE_KEY = "app_settings_v2";
export const UI_THEME_CHANGED_EVENT = "app:ui-theme-changed";
/** Fired when the set of available plugin themes or the unavailable-selection state changes. */
export const PLUGIN_THEMES_CHANGED_EVENT = "app:plugin-themes-changed";
/**
 * Last applied plugin theme, read synchronously at startup (main.ts) so a
 * plugin theme paints before the contribution snapshot arrives. Outside the
 * `app_` config-sync prefix on purpose: it is machine-local derived data.
 */
export const PLUGIN_THEME_CACHE_KEY = "mincode_plugin_theme_cache_v1";
registerDesktopBrowserSnapshotKeys(PLUGIN_THEME_CACHE_KEY);

export type BuiltinUiTheme = "system" | "dark" | "light" | "graphite" | "midnight" | "paper" | "cloud-blue" | "arco" | "arco-dark";
/** A contributed theme id `<pluginId>.<name>` (contributes.themes). */
export type PluginUiThemeId = `${string}.${string}`;
export type UiTheme = BuiltinUiTheme | PluginUiThemeId;
export type UiThemeKind = PluginThemeKind;

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

export function isBuiltinUiTheme(theme: unknown): theme is BuiltinUiTheme {
  return typeof theme === "string" && UI_THEMES.has(theme as BuiltinUiTheme);
}

export function isPluginUiTheme(theme: unknown): theme is PluginUiThemeId {
  return !isBuiltinUiTheme(theme) && isPluginThemeId(theme);
}

/** Built-in ids pass; a syntactically valid plugin theme id is kept even when its plugin is missing. */
export function normalizeUiTheme(theme: unknown): UiTheme {
  if (isBuiltinUiTheme(theme)) return theme;
  if (isPluginUiTheme(theme)) return theme;
  return "light";
}

export function normalizeUiThemeKind(kind: unknown): UiThemeKind | undefined {
  return kind === "light" || kind === "dark" ? kind : undefined;
}

// ---------------------------------------------------------------------------
// Plugin theme registry (fed by services/contributions/themes.ts)

let pluginThemes = new Map<string, PluginThemeRecord>();
let pluginThemesSynced = false;
let cachedTheme: PluginThemeRecord | undefined;
let cacheLoaded = false;
let appliedProperties: string[] = [];
let currentPreference: UiTheme = "light";
let unavailablePluginTheme: string | undefined;
let storedKindHint: UiThemeKind | undefined;

function loadCache(): PluginThemeRecord | undefined {
  if (cacheLoaded) return cachedTheme;
  cacheLoaded = true;
  try {
    const raw = window.localStorage.getItem(PLUGIN_THEME_CACHE_KEY);
    if (!raw) return undefined;
    const parsed = JSON.parse(raw) as { version?: unknown; theme?: Partial<PluginThemeRecord> };
    const theme = parsed?.theme;
    if (parsed?.version !== 1 || !theme || typeof theme.pluginId !== "string") return undefined;
    const { pluginId, pluginName, ...data } = theme;
    const result = validatePluginTheme(data, pluginId);
    if (result.ok) cachedTheme = { ...result.theme, pluginId, pluginName: typeof pluginName === "string" ? pluginName : pluginId };
  } catch {
    cachedTheme = undefined;
  }
  return cachedTheme;
}

function writeCache(theme: PluginThemeRecord) {
  cachedTheme = theme;
  cacheLoaded = true;
  writePersistedBrowserSnapshot(PLUGIN_THEME_CACHE_KEY, { version: 1, theme });
}

/** Validated plugin themes currently contributed by enabled plugins. */
export function listPluginThemes(): PluginThemeRecord[] {
  return [...pluginThemes.values()];
}

/**
 * The plugin theme for an id: the registry once the first snapshot arrived,
 * the startup cache before that.
 */
export function findPluginTheme(id: string): PluginThemeRecord | undefined {
  const registered = pluginThemes.get(id);
  if (registered) return registered;
  if (pluginThemesSynced) return undefined;
  const cache = loadCache();
  return cache?.id === id ? cache : undefined;
}

/** The theme preference last passed to applyUiTheme (plugin ids included). */
export function currentUiThemePreference(): UiTheme {
  return currentPreference;
}

/** The stored plugin theme id whose plugin is missing or disabled, if any. */
export function unavailablePluginThemeId(): string | undefined {
  return unavailablePluginTheme;
}

/** Called by the themes contribution consumer after each snapshot. */
export function setAvailablePluginThemes(themes: readonly PluginThemeRecord[]): void {
  pluginThemes = new Map(themes.map((theme) => [theme.id, theme]));
  pluginThemesSynced = true;
  if (isPluginUiTheme(currentPreference)) applyUiTheme(currentPreference);
  else window.dispatchEvent(new CustomEvent(PLUGIN_THEMES_CHANGED_EVENT));
}

/** Test seam: forget the registry and cache state. */
export function resetPluginThemeRegistryForTest(): void {
  pluginThemes = new Map();
  pluginThemesSynced = false;
  cachedTheme = undefined;
  cacheLoaded = false;
  appliedProperties = [];
  currentPreference = "light";
  unavailablePluginTheme = undefined;
  storedKindHint = undefined;
}

// ---------------------------------------------------------------------------
// Resolution

function resolvedBuiltinTheme(theme: BuiltinUiTheme): Exclude<BuiltinUiTheme, "system"> {
  if (theme !== "system") return theme;
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function fallbackKind(id: string): UiThemeKind {
  if (storedKindHint) return storedKindHint;
  const cache = loadCache();
  return cache?.id === id ? cache.kind : "light";
}

/** Light/dark kind of any theme preference (plugin themes: their kind or the stored fallback kind). */
export function resolveUiThemeKind(theme: unknown): UiThemeKind {
  const normalized = normalizeUiTheme(theme);
  if (isPluginUiTheme(normalized)) return findPluginTheme(normalized)?.kind ?? fallbackKind(normalized);
  return LIGHT_UI_THEMES.has(resolvedBuiltinTheme(normalized)) ? "light" : "dark";
}

export function resolveMonacoEditorTheme(theme: unknown): string {
  // The editor theme setting (a plugin or custom editor theme) wins over the UI theme.
  const override = editorThemeOverride();
  if (override) return override;
  const normalized = normalizeUiTheme(theme);
  if (isPluginUiTheme(normalized)) {
    const plugin = findPluginTheme(normalized);
    // Defined by services/monaco-themes.ts; without the plugin, use the kind's built-in theme.
    if (plugin) return monacoPluginThemeName(plugin.id);
    return fallbackKind(normalized) === "light" ? "vs" : "vs-dark";
  }
  return resolveUiThemeKind(normalized) === "light" ? "vs" : "vs-dark";
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
  storedKindHint = normalizeUiThemeKind(stored?.uiThemeKind) ?? storedKindHint;
  return normalizeUiTheme(stored?.uiTheme);
}

// ---------------------------------------------------------------------------
// Application

let stopSystemThemeListener: (() => void) | undefined;

function clearPluginProperties(root: HTMLElement) {
  for (const property of appliedProperties) root.style.removeProperty(property);
  appliedProperties = [];
}

function applyPluginTheme(root: HTMLElement, theme: PluginThemeRecord) {
  const properties = pluginThemeCssProperties(theme);
  const next = Object.keys(properties);
  for (const property of appliedProperties) {
    if (!(property in properties)) root.style.removeProperty(property);
  }
  // CSSOM only: validated hex values for allowlisted token names, never CSS text.
  for (const [property, value] of Object.entries(properties)) {
    if (THEME_CSS_PROPERTIES.includes(property)) root.style.setProperty(property, value);
  }
  appliedProperties = next;
}

/**
 * Applies a theme preference. Built-in themes use foundation.css selectors
 * (`data-theme="<id>"`); a plugin theme sets `data-theme="plugin"` and
 * writes its tokens as inline custom properties on the root element. A
 * plugin theme whose plugin is missing falls back to the built-in light or
 * dark theme of its stored kind while the stored id is kept.
 * `data-theme-kind` is set for every theme; kind-dependent CSS outside
 * foundation.css must select on it.
 */
export function applyUiTheme(theme: UiTheme, kindHint?: unknown): void {
  const preference = normalizeUiTheme(theme);
  storedKindHint = normalizeUiThemeKind(kindHint) ?? storedKindHint;
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
  const previousUnavailable = unavailablePluginTheme;
  let detail: UiTheme;
  let kind: UiThemeKind;
  const plugin = isPluginUiTheme(preference) ? findPluginTheme(preference) : undefined;
  if (plugin) {
    unavailablePluginTheme = undefined;
    kind = plugin.kind;
    detail = preference;
    applyPluginTheme(root, plugin);
    root.dataset.theme = "plugin";
    if (pluginThemes.has(plugin.id)) writeCache(plugin);
  } else {
    clearPluginProperties(root);
    const fallback: BuiltinUiTheme = isPluginUiTheme(preference) ? fallbackKind(preference) : preference;
    unavailablePluginTheme = isPluginUiTheme(preference) ? preference : undefined;
    const normalized = resolvedBuiltinTheme(fallback);
    kind = LIGHT_UI_THEMES.has(normalized) ? "light" : "dark";
    detail = isPluginUiTheme(preference) ? preference : normalized;
    root.dataset.theme = normalized;
  }
  root.dataset.themePreference = preference;
  root.dataset.themeKind = kind;
  root.style.colorScheme = kind;
  root.classList.toggle("dark", kind === "dark");
  window.dispatchEvent(new CustomEvent<UiTheme>(UI_THEME_CHANGED_EVENT, { detail }));
  if (previousUnavailable !== unavailablePluginTheme || plugin) {
    window.dispatchEvent(new CustomEvent(PLUGIN_THEMES_CHANGED_EVENT));
  }
}

/** Restores the native desktop value as well as the browser-preview fallback. */
export async function hydrateUiTheme(): Promise<UiTheme> {
  const stored = await hydrateSettingsSnapshot<{ uiTheme?: unknown; uiThemeKind?: unknown } & Record<string, unknown>>();
  const theme = normalizeUiTheme(stored?.uiTheme ?? readStoredUiTheme());
  applyUiTheme(theme, stored?.uiThemeKind);
  return theme;
}

/** Current `--app-*` inline tokens of an applied plugin theme (for plugin iframes). */
export function currentPluginThemeTokens(): Record<string, string> | undefined {
  if (!isPluginUiTheme(currentPreference)) return undefined;
  const plugin = findPluginTheme(currentPreference);
  return plugin ? pluginThemeCssProperties(plugin) : undefined;
}
