/**
 * Queries plugin AI catalogs for Settings → AI → Discover through the host
 * (`plugin_host_ai_catalog_search`, `plugin_host_ai_catalog_install`). The
 * host checks the catalog still exists, builds the fixed payloads, runs the
 * plugin's `read`-risk actions with a bounded timeout, payload and result,
 * and returns validated plain text, which is re-validated here. Install
 * content is fetched only when the user adds an item and is handed to the
 * regular MCP config / SKILL.md parsers; the host never fetches a URL the
 * plugin names. Never reachable from AI tools.
 */
import {
  parseAiCatalogInstallResponse,
  parseAiCatalogSearchResponse,
  pluginCatalogEntryId,
  type AiCatalogKind,
} from "@/domain/plugins/ai-catalogs";
import type { RegistryEntry } from "@/domain/ai/settings-integrations";
import { invokeProviderCommand, providerErrorText } from "@/services/plugin-provider-invoke";
import { usePluginAiCatalogsStore } from "@/stores/plugin-ai-catalogs";

/**
 * Searches every active catalog of `kind` in parallel. A catalog that fails,
 * times out or answers invalid data contributes nothing, like an unavailable
 * community registry.
 */
export async function searchPluginAiCatalogs(kind: AiCatalogKind, query: string, locale: string, signal?: AbortSignal): Promise<RegistryEntry[]> {
  const catalogs = usePluginAiCatalogsStore().catalogs.filter((catalog) => catalog.kind === kind);
  const results = await Promise.all(
    catalogs.map(async (catalog) => {
      const result = await invokeProviderCommand(
        "plugin_host_ai_catalog_search",
        { pluginId: catalog.pluginId, catalogId: catalog.id, query, locale },
        (value) => {
          if (!value || typeof value !== "object" || !("kind" in value) || value.kind !== kind) throw new Error("invalid_result: catalog kind changed");
          return parseAiCatalogSearchResponse(value);
        },
        signal,
      );
      if (!result.ok || !usePluginAiCatalogsStore().catalogs.includes(catalog)) return [];
      return result.response.items.map((item): RegistryEntry => {
        const id = pluginCatalogEntryId(catalog.pluginId, catalog.id, item.id);
        return {
          id,
          name: item.name,
          description: item.description ?? "",
          source: "plugin",
          installRef: id,
          ...(item.docsUrl ? { docsUrl: item.docsUrl } : {}),
          plugin: { pluginId: catalog.pluginId, pluginName: catalog.pluginName, catalogId: catalog.id, itemId: item.id, kind, fingerprint: catalog.fingerprint ?? "" },
        };
      });
    }),
  );
  return results.flat();
}

/** Returns the entry with its install content inline; throws a short message on failure. */
export async function resolvePluginRegistryEntry(entry: RegistryEntry, locale: string): Promise<RegistryEntry> {
  if (entry.source !== "plugin") return entry;
  if (!entry.plugin) throw new Error("unavailable: missing catalog reference");
  const catalog = usePluginAiCatalogsStore().catalogs.find((candidate) => candidate.pluginId === entry.plugin!.pluginId && candidate.id === entry.plugin!.catalogId);
  if (!catalog || catalog.kind !== entry.plugin.kind || (catalog.fingerprint ?? "") !== (entry.plugin.fingerprint ?? "")) {
    throw new Error("unavailable: catalog changed; search again");
  }
  const result = await invokeProviderCommand(
    "plugin_host_ai_catalog_install",
    { pluginId: entry.plugin.pluginId, catalogId: entry.plugin.catalogId, itemId: entry.plugin.itemId, locale },
    (value) => {
      if (!value || typeof value !== "object" || !("kind" in value) || value.kind !== catalog.kind) throw new Error("invalid_result: catalog kind changed");
      return parseAiCatalogInstallResponse(value);
    },
  );
  if (!usePluginAiCatalogsStore().catalogs.includes(catalog)) throw new Error("unavailable: catalog changed; search again");
  if (!result.ok) throw new Error(providerErrorText(result.error));
  return { ...entry, inlinePayload: result.response };
}
