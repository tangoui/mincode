/**
 * Runtime of the `terminalColorSchemes` consumer, loaded only when a snapshot
 * contains schemes (see terminal-color-schemes.ts). Re-validates every scheme
 * (`collectTerminalColorSchemes`) before its palette reaches the terminal
 * registry in services/terminal-theme.ts.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { collectTerminalColorSchemes, terminalColorSchemePalette } from "@/domain/themes/terminal-color-schemes";
import { setAvailablePluginTerminalColorSchemes } from "@/services/terminal-theme";
import { useTerminalColorSchemesStore } from "@/stores/terminal-color-schemes";

let applyGeneration = 0;

export function applyTerminalColorSchemeContributions(contributions: PointContribution[]): () => void {
  const result = collectTerminalColorSchemes(contributions);
  const token = ++applyGeneration;
  setAvailablePluginTerminalColorSchemes(
    result.schemes.map((scheme) => ({ id: scheme.id, palette: terminalColorSchemePalette(scheme) })),
  );
  useTerminalColorSchemesStore().setSchemes(result.schemes, result.issues);
  // Cleared only when no apply follows (the lazy loader applies the next
  // snapshot a few microtasks later), so a refresh never flashes the fallback.
  return () => {
    setTimeout(() => {
      if (applyGeneration !== token) return;
      setAvailablePluginTerminalColorSchemes([]);
      useTerminalColorSchemesStore().clearSchemes();
    }, 0);
  };
}
