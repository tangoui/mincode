/**
 * Registers contributed plugin themes with Monaco. Each theme becomes
 * `mincode-plugin-<hash>` (see resolveMonacoEditorTheme) based on vs or
 * vs-dark. Uses the Monaco contribution hook so Monaco is never loaded
 * eagerly; definitions are refreshed whenever the theme registry changes.
 */
import { monacoPluginThemeName, pluginMonacoTheme, type PluginThemeRecord } from "@/domain/themes/plugin-theme";
import { registerMonacoContributionHook, type MonacoApi } from "@/services/monaco-contributions";
import {
  currentUiThemePreference,
  findPluginTheme,
  isPluginUiTheme,
  listPluginThemes,
  resolveMonacoEditorTheme,
} from "@/services/ui-theme";

let monacoApi: MonacoApi | undefined;
const defined = new Map<string, string>();

function themesToDefine(): PluginThemeRecord[] {
  const themes = listPluginThemes();
  const current = currentUiThemePreference();
  if (isPluginUiTheme(current)) {
    const theme = findPluginTheme(current);
    if (theme && !themes.some((item) => item.id === theme.id)) themes.push(theme);
  }
  return themes;
}

/** Defines every known plugin theme with Monaco (no-op until Monaco has loaded). */
export function definePluginMonacoThemes(): void {
  const monaco = monacoApi;
  if (!monaco) return;
  for (const theme of themesToDefine()) {
    const name = monacoPluginThemeName(theme.id);
    const data = pluginMonacoTheme(theme);
    const signature = JSON.stringify(data);
    if (defined.get(name) === signature) continue;
    monaco.editor.defineTheme(name, data);
    defined.set(name, signature);
  }
  const current = currentUiThemePreference();
  // Editors created before the definition fell back to vs; re-apply the global theme.
  if (isPluginUiTheme(current)) monaco.editor.setTheme(resolveMonacoEditorTheme(current));
}

/** Installs the Monaco hook; returns its disposer. */
export function installPluginMonacoThemes(): () => void {
  return registerMonacoContributionHook((monaco) => {
    monacoApi = monaco;
    definePluginMonacoThemes();
    return () => {
      monacoApi = undefined;
    };
  });
}
