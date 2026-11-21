import { ref, shallowRef } from "vue";
import type { PluginPublishRecord } from "@/domain/plugins/publish-summary";
import { invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

// Intentionally global: publications belong to the MinCode window/account, not a workspace.
export const pluginPublishPending = ref(false);
export const pluginPublishEvents = shallowRef<PluginPublishRecord[]>([]);
let dismiss: (id: string) => void = () => undefined;
export function dismissPluginPublish(id: string) { dismiss(id); }
/** Subscribe synchronously before startup IPC. Promise callbacks retain events across lazy loading. */
export function installPluginPublishListener(): () => void {
  if (!isDesktopRuntime()) return () => undefined;
  let active = true;
  const state = import("@/services/plugin-publish-state").then(module => module.createPluginPublishState(records => {
    if (active) { pluginPublishEvents.value = records; pluginPublishPending.value = records.length > 0; }
  }));
  const stopPublish = listenAppEvent<unknown>("plugin:development-publish", value => {
    pluginPublishPending.value = true;
    void state.then(s => { if (active) s.receive(value); }).catch(() => undefined);
  });
  const stopCloud = listenAppEvent<unknown>("cloud:status", value => { void state.then(s => { if (active) s.cloud(value); }).catch(() => undefined); });
  void invokeBackend<unknown>("plugin_host_development_publish_list", {}).then(values => state.then(s => { if (active) s.hydrate(values); })).catch(() => undefined);
  dismiss = id => { void state.then(s => { if (active) s.dismiss(id); }).catch(() => undefined); };
  return () => { active = false; stopPublish(); stopCloud(); void state.then(s => s.dispose()).catch(() => undefined); pluginPublishEvents.value = []; pluginPublishPending.value = false; };
}
