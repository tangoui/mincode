/**
 * Tracks whether any installed plugin declares settings
 * (`contributes.configuration`) or an enabled plugin has a settings page
 * (`contributes.settingsPages`, from the contribution snapshot), so Settings
 * can hide its Plugins page while there is nothing to configure. `refresh()` asks the host; while the component
 * is mounted, host events that can change the answer (install, uninstall,
 * enable, disable, development sessions, workspace switch) ask again, debounced.
 * Outside the desktop runtime the host is never asked and the answer stays
 * "unknown".
 */
import { computed, onBeforeUnmount, onMounted, shallowRef } from "vue";
import { createLatestRequestGate } from "@/domain/async/latest-request-gate";
import { createDebouncedTrigger, type PluginSettingsAvailability } from "@/domain/plugins/configuration-refresh";
import { listConfigurablePlugins, listenPluginSettingsInvalidated } from "@/services/plugin-configuration-service";
import { settingsPageContributions } from "@/services/contributions/settings-pages";
import { isDesktopRuntime } from "@/services/desktop-bridge";

export function usePluginSettingsAvailability() {
  const configurable = shallowRef<PluginSettingsAvailability>("unknown");
  // A plugin with only a settings page still needs the Plugins page.
  const availability = computed<PluginSettingsAvailability>(() => (settingsPageContributions.value.length > 0 ? "some" : configurable.value));
  const desktop = isDesktopRuntime();
  const gate = createLatestRequestGate(() => 0);
  const trigger = createDebouncedTrigger(() => void refresh());
  let stopInvalidation: (() => void) | undefined;
  let disposed = false;

  async function refresh() {
    if (!desktop || disposed) return;
    const token = gate.begin();
    try {
      const ids = await listConfigurablePlugins();
      if (!disposed && gate.isCurrent(token)) configurable.value = ids.length > 0 ? "some" : "none";
    } catch {
      if (!disposed && gate.isCurrent(token)) configurable.value = "error";
    }
  }

  onMounted(() => {
    if (desktop) stopInvalidation = listenPluginSettingsInvalidated(() => trigger.schedule());
  });
  onBeforeUnmount(() => {
    disposed = true;
    stopInvalidation?.();
    trigger.cancel();
    gate.invalidate();
  });

  return { availability, refresh };
}
