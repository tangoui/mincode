/** Applies Monaco editor colors and keeps the same selection in every window. */
import {
  EDITOR_THEME_CUSTOM,
  EDITOR_THEME_FOLLOW,
  normalizeEditorThemePreference,
} from "@/domain/themes/editor-theme-id";
import {
  MONACO_CUSTOM_EDITOR_THEME,
  customEditorThemeData,
  editorMonacoTheme,
  normalizeCustomEditorTheme,
  type CustomEditorTheme,
  type EditorThemeData,
} from "@/domain/themes/editor-theme-data";
import { editorThemeOverride, setEditorThemeOverride } from "@/services/editor-theme-override";
import { registerMonacoContributionHook, type MonacoApi } from "@/services/monaco-contributions";
import { hydrateSettingsSnapshot } from "@/services/settings-persistence";
import { UI_THEME_STORAGE_KEY, currentUiThemePreference, resolveMonacoEditorTheme } from "@/services/ui-theme";
import { emitAppEvent, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

export const EDITOR_THEME_CHANGED_EVENT = "app:editor-theme-changed";

export interface EditorThemeSelection {
  theme: string;
  custom: CustomEditorTheme;
}

type StoredEditorThemeSettings = {
  editorColorTheme?: unknown;
  editorCustomTheme?: unknown;
};

let monacoApi: MonacoApi | undefined;
let selection: EditorThemeSelection = { theme: EDITOR_THEME_FOLLOW, custom: normalizeCustomEditorTheme(undefined) };
const defined = new Map<string, string>();

function normalizeSelection(value: StoredEditorThemeSettings | undefined): EditorThemeSelection {
  return {
    theme: normalizeEditorThemePreference(value?.editorColorTheme),
    custom: normalizeCustomEditorTheme(value?.editorCustomTheme),
  };
}

function define(monaco: MonacoApi, name: string, data: EditorThemeData) {
  const theme = editorMonacoTheme(data);
  const signature = JSON.stringify(theme);
  if (defined.get(name) === signature) return false;
  monaco.editor.defineTheme(name, theme);
  defined.set(name, signature);
  return true;
}

/** The Monaco theme the setting selects, or undefined to follow the UI theme. */
function selectedThemeData(): { name: string; data: EditorThemeData } | undefined {
  if (selection.theme === EDITOR_THEME_CUSTOM) {
    return { name: MONACO_CUSTOM_EDITOR_THEME, data: customEditorThemeData(selection.custom) };
  }
  return undefined;
}

/** Defines the selected theme with Monaco and applies it (no-op until Monaco has loaded). */
function sync() {
  const monaco = monacoApi;
  if (!monaco) {
    setEditorThemeOverride(undefined);
    return;
  }
  const selected = selectedThemeData();
  const redefined = selected ? define(monaco, selected.name, selected.data) : false;
  const previous = editorThemeOverride();
  setEditorThemeOverride(selected?.name);
  // Following the UI theme leaves Monaco alone; editors resolve it themselves.
  if (!redefined && previous === selected?.name) return;
  monaco.editor.setTheme(resolveMonacoEditorTheme(currentUiThemePreference()));
}

function notifyEditorTheme(value: unknown): EditorThemeSelection {
  const record = typeof value === "object" && value !== null ? (value as Partial<EditorThemeSelection>) : undefined;
  selection = normalizeSelection({ editorColorTheme: record?.theme, editorCustomTheme: record?.custom });
  sync();
  return selection;
}

/** The current selection (normalized). */
export function currentEditorThemeSelection(): EditorThemeSelection {
  return { theme: selection.theme, custom: { kind: selection.custom.kind, colors: { ...selection.custom.colors } } };
}

/** Applies the setting in this window and every other desktop window. */
export function applyEditorThemePreference(theme: unknown, custom: unknown): void {
  const next = notifyEditorTheme({ theme, custom });
  if (isDesktopRuntime()) void emitAppEvent(EDITOR_THEME_CHANGED_EVENT, next);
}

function readStoredSelection(): EditorThemeSelection {
  try {
    const raw = window.localStorage.getItem(UI_THEME_STORAGE_KEY);
    return normalizeSelection(raw ? (JSON.parse(raw) as StoredEditorThemeSettings) : undefined);
  } catch {
    return normalizeSelection(undefined);
  }
}

/** Restores the stored setting, installs the Monaco hook and keeps all desktop windows in step. */
export function initializeEditorTheme(): () => void {
  selection = readStoredSelection();
  // Desktop settings are sealed; the shared hydration decrypts them.
  void hydrateSettingsSnapshot<StoredEditorThemeSettings>().then((stored) => {
    selection = normalizeSelection(stored ?? undefined);
    sync();
  });
  const uninstall = registerMonacoContributionHook((monaco) => {
    monacoApi = monaco;
    defined.clear();
    setEditorThemeOverride(undefined);
    sync();
    return () => {
      monacoApi = undefined;
      setEditorThemeOverride(undefined);
    };
  });
  const stopListening = isDesktopRuntime() ? listenAppEvent<unknown>(EDITOR_THEME_CHANGED_EVENT, notifyEditorTheme) : () => {};
  return () => {
    uninstall();
    stopListening();
  };
}
