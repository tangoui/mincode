/**
 * Asks a plugin file decoration provider for the decorations of one
 * directory of the project tree through the host
 * (`plugin_host_file_decorations`,
 * backend/plugin/file_decorations_invoke.go). Only the project
 * page calls it, for directories the user expanded or refreshed. The request
 * names the provider, the project root, the directory ("" is the root) and
 * the names it shows; the answer is re-validated here
 * (domain/plugins/file-decorations.ts). Only provider failures (timeout,
 * error, bad answer, busy, unavailable) come back as "failed"; a request the
 * host refused (e.g. a symlinked folder leaving the project) is "rejected"
 * and never suspends the provider. Loaded on demand, only once a provider exists.
 */
import {
  decorationFailureCounts,
  parseFileDecorationsResponse,
  type PluginDecorationProvider,
  type TreeDecoration,
} from "@/domain/plugins/file-decorations";
import { useVueI18n } from "@/i18n/messages";
import { invokeProviderCommand } from "@/services/plugin-provider-invoke";

export async function requestFileDecorations(
  provider: PluginDecorationProvider,
  projectRoot: string,
  directory: string,
  names: string[],
  signal: AbortSignal,
): Promise<Map<string, Omit<TreeDecoration, "pluginName">> | "failed" | "rejected"> {
  const result = await invokeProviderCommand(
    "plugin_host_file_decorations",
    { pluginId: provider.pluginId, providerId: provider.id, projectRoot, directory, names, locale: useVueI18n().locale.value },
    (value) => parseFileDecorationsResponse(value, names),
    signal,
  );
  if (result.ok) return result.response;
  return decorationFailureCounts(result.error.code) ? "failed" : "rejected";
}
