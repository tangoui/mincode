/** Plain-text review before plugin catalog data becomes ordinary user configuration. */
import { h } from "vue";
import { ElMessageBox, ElScrollbar } from "element-plus";
import type { RegistryEntry } from "@/domain/ai/settings-integrations";
import type { MessageKey } from "@/i18n/messages";

export async function confirmPluginRegistryInstall(
  entry: RegistryEntry,
  details: string,
  t: (key: MessageKey, params?: Record<string, string | number>) => string,
): Promise<boolean> {
  if (entry.source !== "plugin") return true;
  if (!entry.plugin) return false;
  try {
    await ElMessageBox.confirm(
      h("div", { class: "settings-ai-plugin-confirm" }, [
        h("p", t("settings.registry.pluginConfirmMessage", { name: entry.name, plugin: entry.plugin.pluginName })),
        h(ElScrollbar, { maxHeight: "240px" }, () => h("pre", { class: "settings-ai-plugin-confirm__details" }, details)),
      ]),
      t("settings.registry.pluginConfirmTitle"),
      { type: "warning", confirmButtonText: t("common.add"), cancelButtonText: t("common.cancel") },
    );
    return true;
  } catch {
    return false;
  }
}
