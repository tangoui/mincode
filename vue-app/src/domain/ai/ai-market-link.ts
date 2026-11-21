/** The official connector & skill market plugin and the view that holds its page. */
export const AI_MARKET_PLUGIN_ID = "ai-market";
export const AI_MARKET_VIEW_ID = "main";

/** "open" shows the market view; "install" sends the user to the Plugin Manager to install or enable it. */
export type AiMarketLinkAction = "open" | "install";

interface InstalledPluginState {
  id: string;
  enabled: boolean;
  activeInWorkspace: boolean;
}

/** The market view only opens when the plugin is installed, enabled and active in this workspace. */
export function aiMarketLinkAction(plugins: readonly InstalledPluginState[]): AiMarketLinkAction {
  const market = plugins.find((plugin) => plugin.id === AI_MARKET_PLUGIN_ID);
  return market?.enabled && market.activeInWorkspace ? "open" : "install";
}
