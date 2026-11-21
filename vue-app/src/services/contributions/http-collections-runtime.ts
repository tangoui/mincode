/**
 * Runtime of the `httpCollections` contribution point consumer, loaded on
 * demand by services/contributions/http-collections.ts only when a plugin
 * contributes collections. `buildPluginHttpCollectionRegistry` re-checks the
 * snapshot and the HTTP workspace lists the result as read-only templates.
 * Applying data never starts a plugin process and never sends a request.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { buildPluginHttpCollectionRegistry } from "@/domain/http/plugin-collections";
import { useHttpPluginTemplatesStore } from "@/stores/http-plugin-templates";

/** Applies one snapshot of the point. Returns the cleanup of this snapshot. */
export function applyHttpCollectionContributions(contributions: PointContribution[]): () => void {
  const store = useHttpPluginTemplatesStore();
  const registry = buildPluginHttpCollectionRegistry(contributions);
  store.setContributed(registry.collections, registry.issues);
  return () => store.setContributed([]);
}
