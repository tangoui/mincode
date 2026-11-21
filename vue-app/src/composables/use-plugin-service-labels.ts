/**
 * Labels the plugin manager shows for a service provider: its title in the
 * UI language, its risk tag (the same tones as the host service prompts in
 * PluginHostUiLayer.vue) and a one-line summary of its input or output shape.
 */
import { serviceEntryTitle, type PluginServiceEntry, type ServiceRisk, type ServiceShape } from "@/domain/plugins/services";
import { useVueI18n } from "@/i18n/messages";

export function usePluginServiceLabels() {
  const { t, localeChain } = useVueI18n();

  /** The entry's titleLocalized for the UI language, else its title. */
  function entryTitle(entry: PluginServiceEntry) {
    return serviceEntryTitle(entry, localeChain.value);
  }

  function riskType(risk: ServiceRisk) {
    return risk === "dangerous" ? "danger" : risk === "write" ? "warning" : "info";
  }

  function riskLabel(risk: ServiceRisk) {
    return t(`pluginHostUi.risk.${risk}`);
  }

  /** e.g. "Files (up to 8) · .png, image/*" or "File · image/png". */
  function shapeText(shape: ServiceShape) {
    const kind = t(`plugins.services.shape.${shape.kind}`);
    const label = shape.kind === "files" && shape.maxFiles ? `${kind} ${t("plugins.services.shape.maxFiles", { count: shape.maxFiles })}` : kind;
    const types = shape.accepts?.length ? shape.accepts.join(", ") : shape.mime ?? "";
    return types ? `${label} · ${types}` : label;
  }

  return { entryTitle, riskType, riskLabel, shapeText };
}
