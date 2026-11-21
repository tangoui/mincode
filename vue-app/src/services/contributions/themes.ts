/**
 * Consumer of the `themes` contribution point (declarative and process
 * plugins). The Go host validates themes structurally and enforces the
 * contrast floors; `collectPluginThemes` re-validates each entry before it
 * reaches the theme registry in services/ui-theme.ts. Themes are always
 * global. A theme that fails is skipped and reported, never thrown.
 * Registering data never starts a plugin process and never loads Monaco.
 */
import { collectPluginThemes, type ThemePluginIssue } from "@/domain/themes/plugin-theme";
import { THEME_CONTRIBUTION_POINT } from "@/domain/themes/tokens";
import { definePluginMonacoThemes, installPluginMonacoThemes } from "@/services/monaco-themes";
import { registerContributionConsumer } from "@/services/plugin-contributions";
import { setAvailablePluginThemes } from "@/services/ui-theme";

export type { ThemePluginIssue } from "@/domain/themes/plugin-theme";

let lastIssues: ThemePluginIssue[] = [];
let lastSyncError: string | undefined;
let applyGeneration = 0;

/** Issues of the last applied snapshot (skipped themes), for diagnostics. */
export function themePluginIssues(): readonly ThemePluginIssue[] {
  return lastIssues;
}

/** The error of the last failed refresh (plugin host unavailable), if any. */
export function themePluginSyncError(): string | undefined {
  return lastSyncError;
}

/**
 * Registers the themes consumer. A failed refresh keeps the themes already
 * registered; a selected theme whose plugin disappears falls back by kind.
 */
export function registerThemeContributionConsumer(): () => void {
  const uninstallMonaco = installPluginMonacoThemes();
  const unregister = registerContributionConsumer({
    point: THEME_CONTRIBUTION_POINT,
    apply: (contributions) => {
      const result = collectPluginThemes(contributions);
      lastIssues = result.issues;
      lastSyncError = undefined;
      const token = ++applyGeneration;
      setAvailablePluginThemes(result.themes);
      definePluginMonacoThemes();
      // The framework disposes right before the next apply; clearing
      // synchronously would flash the fallback theme on every refresh, so
      // clear only when no apply follows (unregister or stopped sync).
      return () => {
        queueMicrotask(() => {
          if (applyGeneration === token) setAvailablePluginThemes([]);
        });
      };
    },
    onError: (message) => {
      lastSyncError = message;
    },
  });
  return () => {
    unregister();
    uninstallMonaco();
  };
}
