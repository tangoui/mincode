/**
 * Consumer of the `fileIcons` contribution point (declarative and process
 * plugins). The Go host validates themes and sanitizes every icon (SVG
 * through the svgsafe allowlist, PNG re-encoded); `collectFileIconThemes`
 * re-validates the snapshot entries, which carry asset keys only. Icons are
 * fetched lazily per visible row and rendered in image context only
 * (components/projects/ProjectFileIcon.vue). Themes are global; registering
 * data never starts a plugin process. The selection lives in the settings
 * key `fileIconTheme`; an unavailable selection falls back to the built-in
 * icons and keeps the stored value.
 */
import { watch } from "vue";
import { collectFileIconThemes, FILE_ICONS_CONTRIBUTION_POINT, type FileIconThemeIssue } from "@/domain/projects/file-icon-theme";
import { normalizeFileIconTheme } from "@/domain/plugins/contribution-settings";
import { registerContributionConsumer } from "@/services/plugin-contributions";
import { readSettingsSnapshot, settingsSnapshotVersion } from "@/services/settings-persistence";
import { useFileIconThemeStore } from "@/stores/file-icon-theme";

export type { FileIconThemeIssue } from "@/domain/projects/file-icon-theme";

let lastIssues: FileIconThemeIssue[] = [];
let lastSyncError: string | undefined;
let applyGeneration = 0;

/** Issues of the last applied snapshot (skipped themes), for diagnostics. */
export function fileIconThemeIssues(): readonly FileIconThemeIssue[] {
  return lastIssues;
}

/** The error of the last failed refresh (plugin host unavailable), if any. */
export function fileIconThemeSyncError(): string | undefined {
  return lastSyncError;
}

/** Applies the selected file icon theme ("mincode" is built in). */
export function applyFileIconThemePreference(themeId: string): void {
  useFileIconThemeStore().setPreference(themeId);
}

export function registerFileIconContributionConsumer(): () => void {
  const store = useFileIconThemeStore();
  // Light overrides follow the active MinCode theme kind (services/ui-theme.ts).
  const root = document.documentElement;
  store.setLight(root.dataset.themeKind === "light");
  const observer = new MutationObserver(() => store.setLight(root.dataset.themeKind === "light"));
  observer.observe(root, { attributes: true, attributeFilter: ["data-theme-kind"] });
  // Settings changed outside the settings view (config sync, import).
  const stopSettingsWatch = watch(settingsSnapshotVersion, () =>
    store.setPreference(normalizeFileIconTheme(readSettingsSnapshot().fileIconTheme))
  );
  const unregister = registerContributionConsumer({
    point: FILE_ICONS_CONTRIBUTION_POINT,
    apply: (contributions) => {
      const result = collectFileIconThemes(contributions);
      lastIssues = result.issues;
      lastSyncError = undefined;
      const token = ++applyGeneration;
      const pluginIds = contributions.map((contribution) => contribution.pluginId);
      // Asset keys are content hashes, but a changed snapshot may drop or
      // replace plugins: release their cached bytes and blob URLs first.
      store.clearThemes(pluginIds);
      store.setThemes(result.themes);
      return () => {
        // The framework disposes right before the next apply; release now
        // but clear the theme list only when no apply follows, so icons do
        // not flash to the built-in set on every refresh.
        queueMicrotask(() => {
          if (applyGeneration === token) store.clearThemes(pluginIds);
        });
      };
    },
    onError: (message) => {
      lastSyncError = message;
    },
  });
  return () => {
    unregister();
    stopSettingsWatch();
    observer.disconnect();
  };
}
