/**
 * The host context sent to sandboxed plugin UI iframes (`mincode.host.connect`
 * and `mincode.host.context`). Contribution points that change what plugin
 * pages should render with (for example plugin themes) extend it through
 * `registerPluginUIContextProvider` and call `notifyPluginUIContextChanged`
 * so open frames receive the update. Only non-sensitive values belong here.
 */
import { effectScope, watch } from "vue";
import type { PluginUIContext } from "@/domain/plugin-ui/protocol";
import { useVueI18n } from "@/i18n/messages";
import { currentPluginThemeTokens, normalizeUiTheme, normalizeUiThemeKind, UI_THEME_CHANGED_EVENT } from "@/services/ui-theme";

export type PluginUIContextProvider = (context: PluginUIContext) => PluginUIContext;

const providers = new Set<PluginUIContextProvider>();
const listeners = new Set<() => void>();

/** The context for plugin UI right now. */
export function currentPluginUIContext(): PluginUIContext {
  const { locale, localeTag } = useVueI18n();
  const root = document.documentElement;
  const themeKind = normalizeUiThemeKind(root.dataset.themeKind) ?? "light";
  // A contributed theme is sent as its built-in kind (so every plugin page
  // stays readable) plus its validated `--app-*` tokens, which the shared
  // official page applies through CSSOM. See services/ui-theme.ts.
  const themeTokens = root.dataset.theme === "plugin" ? currentPluginThemeTokens() : undefined;
  let context: PluginUIContext = {
    locale: locale.value,
    ...(localeTag.value !== locale.value ? { localeTag: localeTag.value } : {}),
    theme: root.dataset.theme === "plugin" ? themeKind : normalizeUiTheme(root.dataset.theme),
    themeKind,
    ...(themeTokens ? { themeTokens } : {}),
  };
  for (const provider of providers) context = provider(context);
  return context;
}

/** Adds a context provider; returns a disposer. Call notifyPluginUIContextChanged when its output changes. */
export function registerPluginUIContextProvider(provider: PluginUIContextProvider): () => void {
  providers.add(provider);
  notifyPluginUIContextChanged();
  return () => {
    if (providers.delete(provider)) notifyPluginUIContextChanged();
  };
}

export function notifyPluginUIContextChanged(): void {
  for (const listener of [...listeners]) listener();
}

/**
 * Calls `listener` whenever the plugin UI context may have changed (locale,
 * language pack tag, UI theme, or a provider notification). Returns a disposer.
 */
export function watchPluginUIContext(listener: () => void): () => void {
  const { locale, localeTag } = useVueI18n();
  const scope = effectScope(true);
  scope.run(() => watch([locale, localeTag], listener));
  window.addEventListener(UI_THEME_CHANGED_EVENT, listener);
  listeners.add(listener);
  return () => {
    scope.stop();
    window.removeEventListener(UI_THEME_CHANGED_EVENT, listener);
    listeners.delete(listener);
  };
}
