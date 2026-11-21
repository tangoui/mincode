import type { ITheme, Terminal } from "@xterm/xterm";
import { hydrateSettingsSnapshot } from "@/services/settings-persistence";
import {
  normalizeUiTheme,
  readStoredUiTheme,
  resolveUiThemeKind,
  UI_THEME_STORAGE_KEY,
} from "@/services/ui-theme";
import { emitAppEvent, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

export const TERMINAL_COLOR_SCHEME_CHANGED_EVENT = "app:terminal-color-scheme-changed";

export type BuiltInTerminalColorScheme = "default" | "dark" | "light" | "solarized";
export type TerminalColorScheme = BuiltInTerminalColorScheme;

const TERMINAL_COLOR_SCHEMES = new Set<string>([
  "default",
  "dark",
  "light",
  "solarized",
]);
let activeTerminalColorScheme: TerminalColorScheme | undefined;
const LIGHT_ANSI_THEME: ITheme = {
  black: "#1f2329",
  brightBlack: "#6b7280",
  red: "#c9362b",
  brightRed: "#e5484d",
  green: "#2f7d32",
  brightGreen: "#3f9142",
  yellow: "#9a6700",
  brightYellow: "#b7791f",
  blue: "#245dcc",
  brightBlue: "#3b73e8",
  magenta: "#8a3fa0",
  brightMagenta: "#a855b8",
  cyan: "#087f8c",
  brightCyan: "#0e98a8",
  white: "#d8dce3",
  brightWhite: "#ffffff",
};

const DARK_ANSI_THEME: ITheme = {
  black: "#0d1018",
  brightBlack: "#667085",
  red: "#ff7b87",
  brightRed: "#ff9aa3",
  green: "#89d185",
  brightGreen: "#a9e6a5",
  yellow: "#e5c07b",
  brightYellow: "#f1d69c",
  blue: "#7aa2f7",
  brightBlue: "#a3c1ff",
  magenta: "#f38ba8",
  brightMagenta: "#ffb5c8",
  cyan: "#7fdbca",
  brightCyan: "#a7eee0",
  white: "#d9dde7",
  brightWhite: "#ffffff",
};

const SOLARIZED_THEME: ITheme = {
  background: "#002b36",
  foreground: "#839496",
  cursor: "#93a1a1",
  cursorAccent: "#002b36",
  selectionBackground: "#073642",
  selectionForeground: "#eee8d5",
  black: "#073642",
  brightBlack: "#586e75",
  red: "#dc322f",
  brightRed: "#cb4b16",
  green: "#859900",
  brightGreen: "#586e75",
  yellow: "#b58900",
  brightYellow: "#657b83",
  blue: "#268bd2",
  brightBlue: "#839496",
  magenta: "#d33682",
  brightMagenta: "#6c71c4",
  cyan: "#2aa198",
  brightCyan: "#93a1a1",
  white: "#eee8d5",
  brightWhite: "#fdf6e3",
};

function readThemeToken(name: string, fallback: string): string {
  if (typeof window === "undefined") return fallback;
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
}

export function normalizeTerminalColorScheme(value: unknown): TerminalColorScheme {
  return isBuiltInTerminalColorScheme(value) ? value : "default";
}

export function isBuiltInTerminalColorScheme(value: unknown): value is BuiltInTerminalColorScheme {
  return typeof value === "string" && TERMINAL_COLOR_SCHEMES.has(value);
}

export function readStoredTerminalColorScheme(): TerminalColorScheme {
  if (activeTerminalColorScheme) return activeTerminalColorScheme;
  try {
    const raw = window.localStorage.getItem(UI_THEME_STORAGE_KEY);
    if (!raw) return "default";
    return normalizeTerminalColorScheme(
      (JSON.parse(raw) as { terminalColorScheme?: unknown }).terminalColorScheme
    );
  } catch {
    return "default";
  }
}

function notifyTerminalColorScheme(scheme: unknown): TerminalColorScheme {
  const normalized = normalizeTerminalColorScheme(scheme);
  activeTerminalColorScheme = normalized;
  window.dispatchEvent(
    new CustomEvent<TerminalColorScheme>(TERMINAL_COLOR_SCHEME_CHANGED_EVENT, {
      detail: normalized,
    })
  );
  return normalized;
}

export function applyTerminalColorScheme(scheme: unknown): void {
  const normalized = notifyTerminalColorScheme(scheme);
  if (isDesktopRuntime()) void emitAppEvent(TERMINAL_COLOR_SCHEME_CHANGED_EVENT, normalized);
}

/** Restores desktop persistence and keeps all desktop windows on the same terminal palette. */
export function initializeTerminalColorScheme(): () => void {
  notifyTerminalColorScheme(readStoredTerminalColorScheme());

  // Desktop settings are sealed; the shared hydration decrypts them and reuses the read main.ts already started.
  void hydrateSettingsSnapshot<{ terminalColorScheme?: unknown }>().then((stored) =>
    notifyTerminalColorScheme(stored?.terminalColorScheme)
  );

  if (!isDesktopRuntime()) return () => {};
  return listenAppEvent<unknown>(TERMINAL_COLOR_SCHEME_CHANGED_EVENT, notifyTerminalColorScheme);
}

export function resolveTerminalTheme(
  theme: unknown = readStoredUiTheme(),
  scheme: unknown = readStoredTerminalColorScheme()
): ITheme {
  const normalizedScheme = normalizeTerminalColorScheme(scheme);
  if (normalizedScheme === "solarized") return { ...SOLARIZED_THEME };
  const normalizedTheme = normalizeUiTheme(theme);
  const light =
    normalizedScheme === "light" ||
    (normalizedScheme === "default" && resolveUiThemeKind(normalizedTheme) === "light");
  const ansiTheme = light ? LIGHT_ANSI_THEME : DARK_ANSI_THEME;

  if (normalizedScheme !== "default") {
    const background = light ? "#ffffff" : "#20232b";
    const foreground = light ? "#1d2129" : "#edf0f7";
    return {
      ...ansiTheme,
      background,
      foreground,
      cursor: light ? "#245dcc" : "#7aa2f7",
      cursorAccent: background,
      selectionBackground: light ? "#dbe7ff" : "#2a3554",
      selectionForeground: foreground,
    };
  }

  const background = readThemeToken("--app-surface", light ? "#ffffff" : "#20232b");
  const foreground = readThemeToken("--app-text", light ? "#1d2129" : "#edf0f7");
  const accent = readThemeToken("--app-accent", light ? "#5d87ff" : "#7c9eff");
  const selection = readThemeToken("--app-accent-soft", light ? "#eef2ff" : "#2a3554");

  return {
    ...ansiTheme,
    background,
    foreground,
    cursor: accent,
    cursorAccent: background,
    selectionBackground: selection,
    selectionForeground: foreground,
  };
}

/**
 * xterm paints its background only inside its own element, so the padding of
 * the host it was opened in would show the pane behind it. Call after `open()`.
 */
export function setTerminalTheme(terminal: Terminal, theme: ITheme): void {
  terminal.options.theme = theme;
  const host = terminal.element?.parentElement;
  if (host) host.style.backgroundColor = theme.background ?? "";
}
