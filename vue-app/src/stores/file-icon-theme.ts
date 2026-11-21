/**
 * File icon theme state (contributes.fileIcons): the validated themes from
 * the contribution consumer, the user's selection (settings key
 * `fileIconTheme`, "mincode" = built-in icons), the light/dark variant and a
 * reactive cache of blob: URLs for icon assets. An unavailable selection
 * keeps its stored value; components then fall back to the built-in icons.
 */
import { computed, ref, shallowRef, triggerRef } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import { DEFAULT_FILE_ICON_THEME, normalizeFileIconTheme } from "@/domain/plugins/contribution-settings";
import {
  resolveFileIconDefinition,
  type FileIconTarget,
  type FileIconThemeRecord,
} from "@/domain/projects/file-icon-theme";
import { contributionAssetObjectURL, releasePluginAssets } from "@/services/contribution-assets";
import { readSettingsSnapshot } from "@/services/settings-persistence";
import { isDesktopRuntime } from "@/services/desktop-bridge";

export interface ActiveFileIcon {
  pluginId: string;
  key: string;
  monochrome: boolean;
}

function currentLightMode(): boolean {
  return typeof document !== "undefined" && document.documentElement.dataset.themeKind === "light";
}

function cacheKey(pluginId: string, key: string) {
  return `${pluginId}\u0000${key}`;
}

export const useFileIconThemeStore = defineStore("file-icon-theme", () => {
  const themes = ref<FileIconThemeRecord[]>([]);
  /** True once the consumer applied a snapshot (so a missing theme means it went away). */
  const loaded = ref(false);
  const preference = ref(normalizeFileIconTheme(readSettingsSnapshot().fileIconTheme));
  const light = ref(currentLightMode());
  // blob: URL per (plugin, key); null = unavailable (fallback, no retry until the theme reloads).
  const urls = shallowRef(new Map<string, string | null>());
  const pending = new Set<string>();
  let urlGeneration = 0;

  const activeTheme = computed(() =>
    preference.value === DEFAULT_FILE_ICON_THEME ? undefined : themes.value.find((theme) => theme.id === preference.value)
  );
  /** The stored selection names a plugin theme that is not available here. */
  const unavailable = computed(
    () => preference.value !== DEFAULT_FILE_ICON_THEME && !activeTheme.value && (loaded.value || !isDesktopRuntime())
  );

  function setPreference(themeId: string) {
    preference.value = normalizeFileIconTheme(themeId);
  }

  function setLight(value: boolean) {
    light.value = value;
  }

  function setThemes(next: FileIconThemeRecord[]) {
    themes.value = next;
    loaded.value = true;
  }

  /** Drops every theme, cached URL and asset of the given plugins. */
  function clearThemes(pluginIds: Iterable<string>) {
    themes.value = [];
    urlGeneration += 1;
    pending.clear();
    urls.value = new Map();
    for (const pluginId of new Set(pluginIds)) releasePluginAssets(pluginId);
  }

  function resolve(target: FileIconTarget): ActiveFileIcon | undefined {
    const theme = activeTheme.value;
    if (!theme) return undefined;
    const icon = resolveFileIconDefinition(theme, target, light.value);
    return icon ? { pluginId: theme.pluginId, key: icon.key, monochrome: theme.monochrome } : undefined;
  }

  /** The cached blob: URL, null when unavailable, undefined while not loaded yet. */
  function iconURL(pluginId: string, key: string): string | null | undefined {
    return urls.value.get(cacheKey(pluginId, key));
  }

  /**
   * Loads one icon lazily. Calls from many rows within 16 ms coalesce into
   * one batched host request (services/contribution-assets.ts).
   */
  function ensureIcon(pluginId: string, key: string) {
    const id = cacheKey(pluginId, key);
    if (urls.value.has(id) || pending.has(id)) return;
    pending.add(id);
    const generation = urlGeneration;
    contributionAssetObjectURL(pluginId, "fileIcons", key)
      .catch(() => null)
      .then((url) => {
        if (generation !== urlGeneration) return;
        pending.delete(id);
        // Only blob: URLs reach <img src> or a CSS mask.
        urls.value.set(id, typeof url === "string" && url.startsWith("blob:") ? url : null);
        triggerRef(urls);
      });
  }

  return {
    themes,
    loaded,
    preference,
    light,
    activeTheme,
    unavailable,
    setPreference,
    setLight,
    setThemes,
    clearThemes,
    resolve,
    iconURL,
    ensureIcon,
  };
});

if (import.meta.hot) import.meta.hot.accept(acceptHMRUpdate(useFileIconThemeStore, import.meta.hot));
