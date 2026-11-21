/**
 * Runtime of the `fileTemplates` contribution point consumer, loaded on
 * demand by services/contributions/file-templates.ts only when a plugin
 * contributes templates. `buildPluginFileTemplateRegistry` re-checks the
 * snapshot. Applying data never starts a plugin process and never writes a
 * file: files are created only from the explicit "New from Template" dialog
 * (services/file-template-service.ts).
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { buildPluginFileTemplateRegistry } from "@/domain/projects/file-templates";
import { releasePluginAssets } from "@/services/contribution-assets";
import { useFileTemplatesStore } from "@/stores/file-templates";

/** Applies one snapshot of the point. Returns the cleanup of this snapshot. */
export function applyFileTemplateContributions(contributions: PointContribution[]): () => void {
  const store = useFileTemplatesStore();
  const registry = buildPluginFileTemplateRegistry(contributions);
  store.setContributed(registry.templates, registry.issues);
  return () => {
    contributions.forEach(contribution => releasePluginAssets(contribution.pluginId));
    store.setContributed([]);
  };
}
