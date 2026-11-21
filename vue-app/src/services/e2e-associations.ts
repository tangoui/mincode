// Real WebView smoke, loaded only after the -tags e2e native gate.
// Never changes OS defaults and never submits a network download.
import { nextTick } from "vue";
import { activeSmokeDialog } from "@/services/e2e-dialogs";
import router from "@/router";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import type { ExternalCandidate } from "@/domain/plugins/external-open";
import { useWorkbenchStore } from "@/stores/workbench";
import { installLocalPlugin, invokePlugin, setPluginEnabled, uninstallPlugin } from "@/services/plugin-service";
import { invokeBackend, listenAppEvent, type BackendCommand } from "@/services/desktop-bridge";
import { offerExternalOpen, openAssociationSettings } from "@/services/plugin-associations-runtime";

const pluginId = "download-manager";
const wait = (ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms));
async function until<T>(read: () => T | undefined, label: string): Promise<T> {
  for (let i = 0; i < 200; i++) {
    const value = read();
    if (value !== undefined) return value;
    await wait(50);
  }
  throw new Error(`missing desktop UI: ${label}`);
}
function visibleBox() {
  return activeSmokeDialog(document);
}
async function clickBox(confirm: boolean) {
  const box = await until(visibleBox, "host confirmation dialog");
  const buttons = [...box.querySelectorAll<HTMLButtonElement>(".el-message-box__btns button")];
  const button = confirm ? buttons[buttons.length - 1] : buttons[0];
  if (!button || button.disabled) throw new Error("missing enabled confirmation button");
  button.click();
  await nextTick();
  await until(() => !box.isConnected || box.getBoundingClientRect().width === 0 ? true : undefined, "confirmation dialog closed");
}
interface State { candidate: ExternalCandidate; enabled: boolean; defaultPlugin: boolean; system: { default: boolean; application: string } }
const settings = (handlerId: string) => invokeBackend<State>("plugin_host_association_settings", { pluginId, handlerId });
const list = () => invokePlugin(pluginId, "list", "{}") as Promise<{ total: number }>;

export async function associationsScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const fixture = config.associations!;
  if (fixture.phase === "cold") {
    // Native argv was queued before the frontend mounted. Opt-in/remembered
    // choices must survive a real process restart, without downloading.
    await until(() => useWorkbenchStore().activeTab?.pluginView?.pluginId === pluginId ? true : undefined, "cold-start external plugin view");
    await until(() => document.querySelector("iframe.plugin-view-frame__iframe.is-ready") ?? undefined, "cold-start sandbox handshake");
    const magnet = await settings("magnet");
    if (!magnet.enabled || !magnet.defaultPlugin) throw new Error("internal choice did not survive restart");
    if ((await list()).total !== 0) throw new Error("cold launch downloaded automatically");
    log.done("native argv magnet drained after process restart into ready sandboxed plugin view");
    log.done("remembered internal choice persisted; no download task created");
    const secondInput = new Promise<string>(resolve => {
      const stop = listenAppEvent<{ requestId?: string }>("plugin:external-open", event => {
        if (event.requestId) { stop(); resolve(event.requestId); }
      });
    });
    await invokeBackend("e2e_smoke_external_ready" as BackendCommand);
    const secondId = await secondInput;
    await clickBox(true);
    for (let i = 0; i < 100; i++) {
      try {
        const preview = await invokePlugin(pluginId, "torrentPreview", JSON.stringify({ requestId: secondId })) as { name: string; size: number };
        if (preview.name !== "smoke.txt" || preview.size !== 1) throw new Error("second-launch seed mismatch");
        break;
      } catch (error) { if (i === 99) throw error; await wait(50); }
    }
    if ((await list()).total !== 0) throw new Error("second launch started a download");
    log.done("actual second process handed .torrent to running window; clicked disclosure; parsed seed; zero downloads");
    await uninstallPlugin(pluginId, true);
    log.done("isolated plugin removed after cold-start check");
    return;
  }

  await installLocalPlugin(fixture.packagePath);
  await setPluginEnabled(pluginId, true);
  const handlers = await invokeBackend<ExternalCandidate[]>("plugin_host_association_handlers", { pluginId });
  if (handlers.length !== 2) throw new Error("missing magnet/torrent handlers");
  const before = new Map<string, State>();
  for (const handler of handlers) {
    const initial = await settings(handler.handlerId);
    if (initial.enabled) throw new Error("installation opted in automatically");
    before.set(handler.handlerId, initial);
  }
  log.done("real .min install/enable; both handlers initially off; queried actual OS defaults");

  const magnet = handlers.find(h => h.type === "magnet")!;
  let dialog = openAssociationSettings(pluginId, magnet.handlerId);
  await clickBox(false);
  await dialog;
  if ((await settings(magnet.handlerId)).enabled) throw new Error("cancel changed settings");
  log.done("rendered host settings dialog; Cancel left opt-in unchanged");
  for (const handler of handlers) {
    dialog = openAssociationSettings(pluginId, handler.handlerId);
    await clickBox(true); // Initial selected operation is enable, never OS default.
    log.done(`clicked opt-in confirmation for ${handler.handlerId}`);
    await dialog;
    const state = await settings(handler.handlerId);
    const original = before.get(handler.handlerId)!;
    if (!state.enabled || JSON.stringify(state.system) !== JSON.stringify(original.system)) throw new Error("opt-in changed OS default or failed");
  }
  log.done("clicked host Confirm to opt in; OS default applications unchanged");

  let fileRequest = "";
  const stopFileEvent = listenAppEvent<{ requestId?: string }>("plugin:external-open", event => { if (event.requestId) fileRequest = event.requestId; });
  const remaining = await invokeBackend<string[]>("plugin_host_external_files", { files: [fixture.ordinaryPath, fixture.torrentPath] });
  if (remaining.length !== 1 || remaining[0] !== fixture.ordinaryPath) throw new Error("ordinary file routing was intercepted");
  await clickBox(true); // Real file disclosure selector, not a direct delivery call.
  await until(() => document.querySelector("iframe.plugin-view-frame__iframe.is-ready") ?? undefined, "sandboxed download page handshake");
  const pending = await invokeBackend<string[]>("plugin_host_external_pending");
  // Delivered IDs are intentionally absent from pending; the native queue ID is
  // captured from the selector's backend event by the normal shell facade.
  if (pending.length !== 0) throw new Error("file request remained undelivered");
  stopFileEvent();
  const initialPreview = await invokePlugin(pluginId, "torrentPreview", JSON.stringify({ requestId: fileRequest })) as { name: string; size: number };
  if (initialPreview.name !== "smoke.txt" || initialPreview.size !== 1) throw new Error("UI-authorized seed preview failed");
  if ((await list()).total !== 0) throw new Error("file disclosure started a download");
  log.done("file disclosure selector clicked; ordinary file preserved; sandbox page ready; no download");

  // Separate authorized file request: prove real Go plugin reverse RPC parses
  // the known seed, then reject a read after plugin disable/re-enable.
  await setPluginEnabled(pluginId, false);
  await setPluginEnabled(pluginId, true);
  // A remembered magnet choice is configured via an authorized view delivery
  // below; settings still must not change the operating system.
  await invokeBackend("plugin_host_external_files", { files: [fixture.torrentPath] });
  await clickBox(false);
  const ids = await invokeBackend<string[]>("plugin_host_external_pending");
  if (ids.length !== 1) throw new Error("cancelled file request lost its pending identity");
  let rejected = false;
  try { await invokePlugin(pluginId, "torrentPreview", JSON.stringify({ requestId: ids[0] })); } catch { rejected = true; }
  if (!rejected) throw new Error("plugin read file without disclosure consent");
  const offered = offerExternalOpen(ids[0]);
  await clickBox(true);
  await offered;
  const preview = await invokePlugin(pluginId, "torrentPreview", JSON.stringify({ requestId: ids[0] })) as { name: string; size: number };
  if (preview.name !== "smoke.txt" || preview.size !== 1) throw new Error("native seed preview mismatch");
  await setPluginEnabled(pluginId, false);
  await setPluginEnabled(pluginId, true);
  rejected = false;
  try { await invokePlugin(pluginId, "torrentPreview", JSON.stringify({ requestId: ids[0] })); } catch { rejected = true; }
  if (!rejected) throw new Error("file grant survived disable");
  log.done("real process SDK denied unapproved read, parsed authorized 1-byte seed, revoked grant on disable");

  await invokeBackend("plugin_host_association_update", { candidate: magnet, operation: "enable" });
  await invokeBackend("e2e_smoke_external_link" as BackendCommand, { url: "magnet:?xt=urn:btih:0123456789012345678901234567890123456789&dn=MinCodeSmoke" });
  const box = await until(visibleBox, "magnet selector");
  const checkbox = box.querySelector<HTMLInputElement>('input[type="checkbox"]');
  if (!checkbox) throw new Error("remember checkbox missing");
  checkbox.click();
  await clickBox(true);
  // Lifecycle revocation intentionally leaves old frames stopped. Exercise the
  // visible Retry control rather than treating this security boundary as auto-recovery.
  const retry = document.querySelector<HTMLButtonElement>(".plugin-view-frame .plugin-view-unavailable button");
  if (retry) retry.click();
  await until(() => document.querySelector("iframe.plugin-view-frame__iframe.is-ready") ?? undefined, "reopened plugin view");
  // Wait for selector completion before checking persisted preference.
  for (let i = 0; i < 100 && !(await settings(magnet.handlerId)).defaultPlugin; i++) await wait(50);
  if (!(await settings(magnet.handlerId)).defaultPlugin || (await list()).total !== 0) throw new Error("remembered selection failed or downloaded");
  for (const handler of handlers) {
    if (JSON.stringify((await settings(handler.handlerId)).system) !== JSON.stringify(before.get(handler.handlerId)!.system)) throw new Error("OS default changed during smoke");
  }
  log.done("magnet selector and remember checkbox clicked; internal default persisted; zero tasks; OS defaults unchanged");
  // Remove the view from persisted tabs: the next phase must be opened by the
  // native argv request, not falsely pass because workbench restored a tab.
  const workbench = useWorkbenchStore();
  for (const tab of [...workbench.tabs]) {
    if (tab.pluginView?.pluginId === pluginId) workbench.closeTab(tab.id, true);
  }
  workbench.openView("dashboard");
  await router.push({ name: "dashboard" });
  await workbench.flushPersistence();
  log.done("closed and persisted plugin tabs before restart to rule out restored-tab false positives");

}
