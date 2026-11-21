/**
 * Consumer of the `settingsPages` contribution point (process plugins, host
 * 0.3.0). The entry chunk keeps only the raw snapshot contributions here;
 * parsing and the page list (settings-pages-runtime.ts) load with Settings.
 * The snapshot lists enabled plugins only, so a disabled plugin's page leaves
 * with the next snapshot. Registering data never starts a plugin: the page's
 * view session begins only while its section is shown
 * (components/settings/PluginSettingsPageSection.vue).
 */
import { shallowRef, type Ref } from "vue";
import type { PointContribution } from "@/domain/plugins/contributions";
import { registerContributionConsumer } from "@/services/plugin-contributions";

const contributions = shallowRef<readonly PointContribution[]>([]);

/** Raw `settingsPages` snapshot contributions; use `pluginSettingsPages` (settings-pages-runtime.ts) for pages. */
export const settingsPageContributions: Readonly<Ref<readonly PointContribution[]>> = contributions;

export function registerSettingsPageContributionConsumer(): () => void {
  return registerContributionConsumer({
    point: "settingsPages",
    apply: (next) => {
      contributions.value = next;
      return () => {
        contributions.value = [];
      };
    },
  });
}
