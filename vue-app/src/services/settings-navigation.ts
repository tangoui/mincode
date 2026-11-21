import { shallowRef } from "vue";

/**
 * App-wide request to open the Settings dialog at a category (and, for the
 * plugin settings page, a specific plugin). Views that are not children of
 * App.vue — the Plugin Manager and plugin iframes — use this instead of
 * reaching into the application shell. App.vue watches the request.
 */
export type SettingsNavigationCategory =
  | "ai"
  | "shortcuts"
  | "plugins"
  | "cloud";

export interface SettingsNavigationRequest {
  seq: number;
  category?: SettingsNavigationCategory;
  pluginId?: string;
}

export const settingsNavigationRequest = shallowRef<SettingsNavigationRequest>({ seq: 0 });

export function requestOpenSettings(category?: SettingsNavigationCategory, pluginId?: string) {
  settingsNavigationRequest.value = { seq: settingsNavigationRequest.value.seq + 1, category, pluginId };
}

/** Opens Settings > Plugins, focused on `pluginId` when given. */
export function openPluginSettings(pluginId?: string) {
  requestOpenSettings("plugins", pluginId);
}

/**
 * Opens Settings > Cloud (account tab) so the user can sign in. The plugin
 * marketplace download gate needs a member session, so the install entry point
 * sends users here instead of only reporting "sign in first".
 */
export function openCloudAccount() {
  requestOpenSettings("cloud");
}
