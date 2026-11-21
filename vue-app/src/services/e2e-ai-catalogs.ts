// Real WebKit flow, loaded only by the native -tags e2e smoke gate.
import { nextTick } from "vue";
import { messagesZh } from "@/i18n/locales/zh";
import { messagesEn } from "@/i18n/locales/en";
import type { MessageKey } from "@/i18n/messages";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import { installLocalPlugin, setPluginEnabled, uninstallPlugin } from "@/services/plugin-service";
import { requestOpenSettings } from "@/services/settings-navigation";
import { readSettingsSnapshot } from "@/services/settings-persistence";
import { usePluginAiCatalogsStore } from "@/stores/plugin-ai-catalogs";
import { activeSmokeDialog } from "@/services/e2e-dialogs";

const delay = () => new Promise<void>(resolve => setTimeout(resolve, 80));
async function until<T>(get: () => T | undefined | false | null, what: string): Promise<T> {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    const result = get();
    if (result) return result;
    await delay();
  }
  throw new Error(`AI catalog smoke timed out: ${what}`);
}
function labelled(root: ParentNode, selector: string, key: MessageKey) {
  return [...root.querySelectorAll<HTMLElement>(selector)].find(element =>
    [messagesZh[key], messagesEn[key]].includes(element.textContent?.trim() ?? ""),
  );
}
async function choose(root: ParentNode, selector: string, key: MessageKey) {
  const element = await until(() => {
    const candidate = labelled(root, selector, key);
    return candidate && !candidate.hasAttribute("disabled") && !candidate.classList.contains("is-loading") ? candidate : undefined;
  }, key);
  element.click();
  await nextTick();
}
async function card(root: ParentNode, itemId: string) {
  return until(() => [...root.querySelectorAll<HTMLElement>(".settings-ai-entry-card")].find(element =>
    element.textContent?.includes("plugin:contributions-process-showcase/") && element.textContent.includes(itemId),
  ), "plugin result card");
}
export async function aiCatalogsScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const pluginId = "contributions-process-showcase";
  const itemId = `showcase-${config.nonce}`;
  const store = usePluginAiCatalogsStore();
  await installLocalPlugin(config.aiCatalogs!.packagePath);
  try {
    await setPluginEnabled(pluginId, true);
    await until(() => store.catalogs.filter(catalog => catalog.pluginId === pluginId).length === 2, "two catalog consumers");
    log.done("installed and enabled real process; both catalogs registered");
    requestOpenSettings("ai");
    const dialog = await until(() => document.querySelector<HTMLElement>(".settings-dialog"), "settings dialog");
    await choose(dialog, ".el-segmented__item", "settings.ai.mcp");
    await choose(dialog, ".el-segmented__item", "settings.ai.subtabDiscover");
    const input = await until(() => dialog.querySelector<HTMLInputElement>(".settings-ai-search-line input"), "connector search input");
    input.value = "showcase";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await choose(dialog, ".settings-ai-search-line button", "common.search");
    const connectorCard = await card(dialog, itemId);
    await choose(connectorCard, "button", "common.add");
    let confirm = await until(() => activeSmokeDialog(document), "connector confirmation");
    if (!confirm.textContent?.includes("127.0.0.1:18765/mcp")) throw new Error("connector URL missing from review");
    await choose(confirm, "button", "common.cancel");
    await until(() => !activeSmokeDialog(document), "cancel dialog closed");
    if ((readSettingsSnapshot().mcpServers as Array<{ id: string }> | undefined)?.some(server => server.id === itemId)) throw new Error("cancelled add saved configuration");
    await choose(await card(dialog, itemId), "button", "common.add");
    confirm = await until(() => activeSmokeDialog(document), "connector confirmation again");
    await choose(confirm, "button", "common.add");
    await until(() => !activeSmokeDialog(document), "add dialog closed");
    await until(() => (readSettingsSnapshot().mcpServers as Array<{ id: string }> | undefined)?.some(server => server.id === itemId), "saved connector");
    log.done("Discover searched through real process; cancel saved nothing; confirmed connector added");
    await uninstallPlugin(pluginId, true);
    await until(() => !store.catalogs.some(catalog => catalog.pluginId === pluginId), "catalog removed after uninstall");
    await nextTick();
    if ([...dialog.querySelectorAll(".settings-ai-entry-card")].some(element => element.textContent?.includes(`plugin:${pluginId}/`))) throw new Error("uninstalled catalog result stayed visible");
    const saved = readSettingsSnapshot();
    if (!(saved.mcpServers as Array<{ id: string }>).some(server => server.id === itemId)) throw new Error("uninstall removed user configuration");
    log.done("uninstall immediately removed catalog results and retained the installed connector");
  } finally {
    await uninstallPlugin(pluginId, true).catch(() => undefined);
  }
}
