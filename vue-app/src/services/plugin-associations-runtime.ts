import { h, ref } from "vue";
import { ElMessage, ElMessageBox, ElSelect, ElOption } from "element-plus";
import PluginExternalOpenChoice from "@/components/plugins/PluginExternalOpenChoice.vue";
import { externalCandidate, type ExternalCandidate } from "@/domain/plugins/external-open";
import { useVueI18n } from "@/i18n/messages";
import { invokeBackend } from "@/services/desktop-bridge";
import { openPluginViewWithRequest } from "@/services/plugin-view-open-requests";

interface Resolved { requestId: string; name: string; type: string; candidates: ExternalCandidate[]; remembered: string }
async function resolve(requestId: string): Promise<Resolved> {
  const raw = await invokeBackend<Record<string, unknown>>("plugin_host_external_resolve", { requestId });
  if (raw.requestId !== requestId || typeof raw.name !== "string" || typeof raw.type !== "string" || !Array.isArray(raw.candidates)) throw new Error("invalid external request");
  return { requestId, name: raw.name, type: raw.type, candidates: raw.candidates.map(externalCandidate).filter((v): v is ExternalCandidate => Boolean(v)), remembered: typeof raw.remembered === "string" ? raw.remembered : "" };
}
export function notifyExternalOpenRejected() {
  ElMessage.warning(useVueI18n().t("pluginContrib.externalOpenHandlers.rejected"));
}
const waiting = new Set<string>();
let showing = false;
export async function offerExternalOpen(requestId: string) {
  if (!/^[a-f0-9]{32}$/.test(requestId) || waiting.size >= 8) return;
  waiting.add(requestId);
  if (showing) return;
  showing = true;
  try {
    for (const id of waiting) {
      try { await processExternal(id); }
      catch { ElMessage.warning(useVueI18n().t("pluginContrib.externalOpenHandlers.failed")); }
      finally { waiting.delete(id); }
    }
  } finally { showing = false; }
}
async function processExternal(id: string) {
  const { t } = useVueI18n();
  let first = await resolve(id);
  for (let attempt = 0; !first.candidates.length && attempt < 3; attempt++) {
    await new Promise<void>(done => setTimeout(done, 1000));
    first = await resolve(id);
  }
  if (!first.candidates.length) { ElMessage.warning(t("pluginContrib.externalOpenHandlers.none")); return; }
  const selected = ref(first.remembered || `${first.candidates[0].pluginId}/${first.candidates[0].handlerId}`);
  const remember = ref(false);
  // Remembering the handler does not waive consent to disclose local file data.
  if (!first.remembered || !["magnet", "ed2k", "thunder", "ftp", "ftps", "sftp", "ssh", "webcal", "webcals", "mailto"].includes(first.type)) {
    try {
      await ElMessageBox.confirm(() => h(PluginExternalOpenChoice, {
        candidates: first.candidates, name: first.name, selected: selected.value, remember: remember.value,
        "onUpdate:selected": (v: string) => { selected.value = v; }, "onUpdate:remember": (v: boolean) => { remember.value = v; },
      }), t("pluginContrib.externalOpenHandlers.choose"), { autofocus: false, closeOnClickModal: false, confirmButtonText: t("pluginContrib.externalOpenHandlers.choose"), cancelButtonText: t("common.cancel") });
    } catch { return; }
  }
  const c = first.candidates.find(v => `${v.pluginId}/${v.handlerId}` === selected.value);
  if (!c) return;
  const second = await resolve(id);
  if (!second.candidates.some(v => v.pluginId === c.pluginId && v.handlerId === c.handlerId && v.fingerprint === c.fingerprint && v.view === c.view)) throw new Error("handler changed");
  const delivered = await invokeBackend<{ pluginId: string; handlerId: string; view: string; params: Record<string, string> }>("plugin_host_external_deliver", { requestId: id, pluginId: c.pluginId, handlerId: c.handlerId, fingerprint:c.fingerprint, remember: remember.value });
  if (!await openPluginViewWithRequest(delivered.pluginId, delivered.view, { requestId: id, source: "external", entry: delivered.handlerId, params: delivered.params })) throw new Error("could not open plugin");
}
let configuring = false;
export async function openAssociationSettings(pluginId: string, handlerId: string) {
  if (configuring) return;
  configuring = true;
  const { t } = useVueI18n();
  try {
    const initial = await invokeBackend<{ candidate: unknown; name: string; enabled: boolean; system: { supported: boolean; default: boolean; application: string }; defaultPlugin: boolean }>("plugin_host_association_settings", { pluginId, handlerId });
    const candidate = externalCandidate(initial.candidate);
    if (!candidate) throw new Error("invalid handler");
    const selected = ref(initial.enabled ? "disable" : "enable");
    // Reuse a native Element Plus select in the trusted host, not plugin HTML.
    const options = [initial.enabled ? "disable" : "enable", "forget", ...(initial.system.supported ? ["default", "restore"] : [])];
    try {
      await ElMessageBox.confirm(() => h("div", [
        h("p", t("pluginContrib.externalOpenHandlers.settingsBody", { plugin: candidate.pluginName, type: initial.name, app: initial.system.application || t("pluginContrib.externalOpenHandlers.unknown") })),
        h(ElSelect, { modelValue: selected.value, "onUpdate:modelValue": (v: string) => { selected.value = v; }, style: "width:100%" }, () => options.map(value => h(ElOption, { value, label: t(`pluginContrib.externalOpenHandlers.${value}` as Parameters<typeof t>[0]) }))),
        h("p", t("pluginContrib.externalOpenHandlers.manual")),
      ]), t("pluginContrib.externalOpenHandlers.settings"), { autofocus: false, closeOnClickModal: false, confirmButtonText: t("common.confirm"), cancelButtonText: t("common.cancel") });
    } catch { return; }
    await invokeBackend("plugin_host_association_update", { candidate, operation: selected.value });
    // Query after the operation: Windows returns a selector, not default success.
    const current = await invokeBackend<{ system: { default: boolean } }>("plugin_host_association_settings", { pluginId, handlerId });
    ElMessage.info(t(selected.value === "default" && !current.system.default ? "pluginContrib.externalOpenHandlers.manual" : "pluginContrib.externalOpenHandlers.updated"));
  } catch { ElMessage.error(t("pluginContrib.externalOpenHandlers.failed")); }
  finally { configuring = false; }
}

export async function listAssociationHandlers(pluginId: string): Promise<ExternalCandidate[]> {
  const result = await invokeBackend<unknown>("plugin_host_association_handlers", { pluginId });
  if (!Array.isArray(result)) throw new Error("invalid association handlers");
  return result.map(externalCandidate).filter((c): c is ExternalCandidate => Boolean(c));
}
