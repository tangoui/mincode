/**
 * Settings pages of the enabled plugins (contributes.settingsPages), parsed
 * from the snapshot contributions that services/contributions/settings-pages.ts
 * keeps. Imported only by Settings code, so the parser stays out of the entry
 * chunk. Entries are re-validated (domain/plugins/settings-pages.ts); at most
 * one page per plugin.
 */
import { computed, type Ref } from "vue";
import { parseSettingsPageEntries, type PluginSettingsPage } from "@/domain/plugins/settings-pages";
import { settingsPageContributions } from "@/services/contributions/settings-pages";

export const pluginSettingsPages: Readonly<Ref<readonly PluginSettingsPage[]>> = computed(() =>
  settingsPageContributions.value.flatMap(parseSettingsPageEntries),
);
