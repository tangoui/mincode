/**
 * Consumer of the `editorThemes` contribution point (declarative and process
 * plugins, always global). The Go host validates each theme and its contrast
 * floors; the runtime (editor-themes-runtime.ts, loaded only when a plugin
 * contributes themes) re-validates before a theme reaches Monaco
 * (services/editor-theme.ts). A stored selection whose plugin is missing
 * keeps its id and follows the UI theme. Registering data never starts a
 * plugin process and never loads Monaco.
 */
import { EDITOR_THEMES_CONTRIBUTION_POINT } from "@/domain/themes/editor-theme-id";
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";
import { useEditorThemesStore } from "@/stores/editor-themes";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function editorThemesSyncError(): string | undefined {
  return lastSyncError;
}

export function registerEditorThemeContributionConsumer(): () => void {
  let runtime: Promise<typeof import("@/services/contributions/editor-themes-runtime")> | undefined;
  const load = () => (runtime ??= import("@/services/contributions/editor-themes-runtime"));
  const onError = (message: string) => {
    lastSyncError = message;
  };
  const apply = lazyContributionApply(async () => (await load()).applyEditorThemeContributions, onError);
  return registerContributionConsumer({
    point: EDITOR_THEMES_CONTRIBUTION_POINT,
    apply: (contributions) => {
      // A snapshot arrived: a stored plugin theme missing from it is unavailable.
      useEditorThemesStore().markLoaded();
      return apply(contributions);
    },
    onError,
  });
}
