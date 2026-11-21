/**
 * Runtime of the `editorThemes` consumer, loaded only when a snapshot
 * contains themes (see editor-themes.ts). Re-validates every theme
 * (`collectEditorThemes`) before it reaches Monaco through
 * services/editor-theme.ts.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { collectEditorThemes } from "@/domain/themes/editor-themes";
import { setAvailablePluginEditorThemes } from "@/services/editor-theme";
import { useEditorThemesStore } from "@/stores/editor-themes";

let applyGeneration = 0;

export function applyEditorThemeContributions(contributions: PointContribution[]): () => void {
  const result = collectEditorThemes(contributions);
  const token = ++applyGeneration;
  setAvailablePluginEditorThemes(
    result.themes.map((theme) => ({ id: theme.id, data: { kind: theme.kind, colors: theme.colors, rules: theme.rules } })),
  );
  useEditorThemesStore().setThemes(result.themes, result.issues);
  // Cleared only when no apply follows (the lazy loader applies the next
  // snapshot a few microtasks later), so a refresh never flashes the UI theme.
  return () => {
    setTimeout(() => {
      if (applyGeneration !== token) return;
      setAvailablePluginEditorThemes([]);
      useEditorThemesStore().clearThemes();
    }, 0);
  };
}
