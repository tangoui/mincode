// Tiny shell facade. Dialogs, validation and external-open dispatch are lazy.
import { invokeBackend, listenAppEvent, isDesktopRuntime } from "@/services/desktop-bridge";
type Runtime = typeof import("@/services/plugin-associations-runtime");
let runtime: Promise<Runtime> | undefined;
const load = () => (runtime ??= import("@/services/plugin-associations-runtime"));
export function installPluginAssociations(): () => void {
  if (!isDesktopRuntime()) return () => undefined;
  let alive = true;
  const stops = [
    listenAppEvent<{ requestId?: string; rejected?: boolean }>("plugin:external-open", event => {
      if (event?.rejected) void load().then(r => { if (alive) r.notifyExternalOpenRejected(); });
      const id = event?.requestId;
      if (typeof id === "string") void load().then(r => { if (alive) void r.offerExternalOpen(id); });
    }),
    listenAppEvent<{ pluginId?: string; handlerId?: string }>("plugin:association-settings", event => {
      const id = event?.pluginId, handler = event?.handlerId;
      if (typeof id === "string" && typeof handler === "string") {
        void load().then(r => { if (alive) void r.openAssociationSettings(id, handler); });
      }
    }),
  ];
  void invokeBackend<unknown>("plugin_host_external_pending").then(ids => {
    if (alive && Array.isArray(ids) && ids.length) void load().then(r => { if (alive) for (const id of ids) if (typeof id === "string") void r.offerExternalOpen(id); });
  }).catch(() => undefined);
  return () => { alive = false; for (const stop of stops) stop(); };
}
