/**
 * Runtime of the `aiContextProviders` consumer, loaded on demand by
 * services/contributions/ai-context-providers.ts only when a plugin
 * contributes providers. It stores the @mention registry the AI composer
 * offers; it never starts a plugin process and never sends anything to a
 * model.
 */
import { buildMentionRegistry } from "@/domain/ai/plugin-context";
import type { PointContribution } from "@/domain/plugins/contributions";
import { useAiContextProvidersStore } from "@/stores/ai-context-providers";

/** Applies one snapshot of the point; returns the cleanup of this snapshot. */
export function applyAiContextProviderContributions(contributions: PointContribution[]): () => void {
  const store = useAiContextProvidersStore();
  const registry = buildMentionRegistry(contributions);
  store.setContributions(registry.providers, registry.issues);
  return () => store.setContributions([]);
}
