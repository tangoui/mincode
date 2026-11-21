/**
 * Runtime of the `aiPrompts` contribution point consumer, loaded on demand by
 * services/contributions/ai-prompts.ts only when a plugin contributes
 * prompts. `buildPluginAiPromptRegistry` re-checks the entries and the AI
 * composer reads the result from the ai-plugin-prompts store. Applying data
 * never starts a plugin process and never sends anything to a model.
 */
import { buildPluginAiPromptRegistry } from "@/domain/ai/plugin-prompts";
import type { PointContribution } from "@/domain/plugins/contributions";
import { useAiPluginPromptsStore } from "@/stores/ai-plugin-prompts";

/** Applies one snapshot of the point. Returns the cleanup of this snapshot. */
export function applyAiPromptContributions(contributions: PointContribution[]): () => void {
  const store = useAiPluginPromptsStore();
  const registry = buildPluginAiPromptRegistry(contributions);
  store.setContributed(registry.prompts, registry.issues);
  return () => store.setContributed([]);
}
