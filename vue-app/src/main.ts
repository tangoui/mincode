import { createApp, nextTick } from "vue";
import { createPinia } from "pinia";
import App from "./App.vue";
import router from "./router";
import { initializeTerminalColorScheme } from "./services/terminal-theme";
import { initializeEditorTheme } from "./services/editor-theme";
import { applyUiTheme, hydrateUiTheme, readStoredUiTheme } from "./services/ui-theme";
import { hydrateSettingsSnapshot, readSettingsSnapshot } from "./services/settings-persistence";
import { applySavedLocale } from "./i18n/messages";
import { finishDevPerformance, installDevPerformanceInspector, startDevPerformance } from "./services/dev-performance";
import { pluginFloatingWindowTarget } from "./services/plugin-service";
import { applyElementPlusDefaults } from "./services/element-plus-defaults";
import { installElementPlusLocale } from "./services/element-plus-locale";
import { isDesktopRuntime } from "./services/desktop-bridge";
import { installDesktopPageBehaviors } from "./services/desktop-page-behaviors";
import "./styles.css";

applyElementPlusDefaults();

type LocaleSettings = {
  locale?: unknown;
  uiLocale?: unknown;
  uiLocalePlugin?: unknown;
};

async function applyLocalePack(settings: LocaleSettings) {
  try {
    const { applySavedLocaleOverlay } = await import("./services/locale-pack-loader");
    await applySavedLocaleOverlay(settings);
  } catch {
    // The base language stays; Settings reports the pack as unavailable.
  }
}

async function bootstrap() {
  const startedAt = startDevPerformance();
  installDevPerformanceInspector();
  // 文件拖入高亮、默认右键菜单策略（主窗口和插件浮窗都需要）。
  if (isDesktopRuntime()) installDesktopPageBehaviors(window, { devContextMenu: import.meta.env.DEV });

  // Apply local snapshots before Vue mounts, then reconcile with desktop storage in the background.
  applyUiTheme(readStoredUiTheme());
  void hydrateUiTheme();
  initializeTerminalColorScheme();
  initializeEditorTheme();
  const savedSettings = readSettingsSnapshot<LocaleSettings>();
  await applySavedLocale(savedSettings.locale);
  // A selected language pack (contributes.localizations) waits at most 800 ms;
  // the loader is only fetched when a pack is selected.
  if (savedSettings.uiLocale) await applyLocalePack(savedSettings);
  void hydrateSettingsSnapshot<LocaleSettings>().then(async (settings) => {
    await applySavedLocale(settings.locale);
    if (settings.uiLocale || savedSettings.uiLocale) {
      const { localePackSelectionFromSettings, setLocalePackSelection } = await import("./services/locale-pack-loader");
      setLocalePackSelection(localePackSelectionFromSettings(settings));
    }
  });

  // views[].floating windows render only the plugin view, without the workbench shell.
  const floating = pluginFloatingWindowTarget(window.location.search);
  if (floating) {
    document.documentElement.classList.add("app-floating-window");
    const { default: PluginFloatingWindow } = await import("./components/plugins/PluginFloatingWindow.vue");
    installElementPlusLocale(createApp(PluginFloatingWindow, floating)).use(createPinia()).mount("#app");
    return;
  }

  // Browser local mode: register the browser handlers before any view calls invokeBackend/canInvoke.
  // Desktop never loads this chunk.
  // A failed chunk load must not block the shell: features stay "desktop-only" and the badge still shows.
  if (!isDesktopRuntime()) {
    try {
      await import("./services/browser-runtime");
    } catch (error) {
      // eslint-disable-next-line no-console -- Startup must continue; keep the chunk-load failure diagnosable.
      console.error("Failed to load browser-local handlers", error);
    }
  }

  installElementPlusLocale(createApp(App)).use(createPinia()).use(router).mount("#app");
  await router.isReady();
  await nextTick();
  finishDevPerformance("startup:app-ready", startedAt);
  // Only `-tags e2e` desktop builds launched by tools/e2e/desktop-smoke.mjs answer
  // with a scenario config; every other build skips the harness after one call.
  const { startDesktopSmokeIfRequested } = await import("./services/e2e-smoke");
  void startDesktopSmokeIfRequested();
}

void bootstrap();
