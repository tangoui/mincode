import { normalizePluginPublishRecord, type PluginPublishRecord } from "@/domain/plugins/publish-summary";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export async function listPluginPublishes(): Promise<PluginPublishRecord[]> {
  if (!isDesktopRuntime()) return [];
  const raw = await invokeBackend<unknown>("plugin_host_development_publish_list", {});
  if (!Array.isArray(raw)) throw new Error("Invalid publish list");
  return raw.flatMap(value => { const record = normalizePluginPublishRecord(value); return record ? [record] : []; });
}
function requireDesktop() { if (!isDesktopRuntime()) throw new Error("Publish requires MinCode desktop"); }
export function confirmPluginPublish(id: string) { requireDesktop(); return invokeBackend("plugin_host_development_publish_confirm", { id }); }
export function rejectPluginPublish(id: string) { requireDesktop(); return invokeBackend("plugin_host_development_publish_reject", { id }); }
export function cancelPluginPublish(id: string) { requireDesktop(); return invokeBackend("plugin_host_development_publish_cancel", { id }); }
export function refreshPluginPublish(id: string) { requireDesktop(); return invokeBackend("plugin_host_development_publish_refresh", { id }); }
