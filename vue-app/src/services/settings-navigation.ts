import { shallowRef } from "vue";

/** An app-wide request to open a settings category. */
export type SettingsNavigationCategory = "ai" | "shortcuts" | "about";

export interface SettingsNavigationRequest {
  seq: number;
  category?: SettingsNavigationCategory;
}

export const settingsNavigationRequest = shallowRef<SettingsNavigationRequest>({ seq: 0 });

export function requestOpenSettings(category?: SettingsNavigationCategory) {
  settingsNavigationRequest.value = { seq: settingsNavigationRequest.value.seq + 1, category };
}
