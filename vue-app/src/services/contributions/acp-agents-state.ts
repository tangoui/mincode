/**
 * The list of plugin ACP agents the acpAgents consumer publishes, and its
 * readiness. Kept apart from the consumer (which pulls in the desktop bridge)
 * so it stays small in the entry chunk and testable on its own.
 *
 * A refresh disposes the previous apply and applies the next snapshot in the
 * same tick, while the validation loads asynchronously. The list therefore
 * keeps the previous agents until the new snapshot lands, and is emptied only
 * by a snapshot without agents or when the sync stops. Until the first
 * snapshot applied (and again after the sync stopped, or always in the
 * browser preview, which has no plugin host) `pluginAcpAgentsReady` is false:
 * an empty list then means "not known yet", never "uninstalled".
 */
import { shallowRef, type Ref } from "vue";
import type { PointContribution } from "@/domain/plugins/contributions";
import type { PluginAcpAgent } from "@/domain/plugins/acp-agents";

/** Agents of enabled plugins active in this workspace that run on this platform. Written by the consumer only. */
export const pluginAcpAgents: Ref<PluginAcpAgent[]> = shallowRef([]);
/** True once a contribution snapshot applied; stays true across refreshes. */
export const pluginAcpAgentsReady: Ref<boolean> = shallowRef(false);

export interface AcpAgentsTarget {
  agents: { value: PluginAcpAgent[] };
  ready: { value: boolean };
}

export type AcpAgentsCollect = (contributions: PointContribution[]) => PluginAcpAgent[];

/**
 * The consumer's apply. load resolves the (lazily imported) collector; it is
 * not called for a snapshot without agents. A superseded snapshot never
 * applies.
 */
export function createAcpAgentsApply(
  load: () => Promise<AcpAgentsCollect>,
  onError: (message: string) => void,
  target: AcpAgentsTarget = { agents: pluginAcpAgents, ready: pluginAcpAgentsReady },
) {
  let generation = 0;
  return (contributions: PointContribution[]): (() => void) => {
    const current = ++generation;
    if (contributions.length === 0) {
      target.agents.value = [];
      target.ready.value = true;
    } else {
      void load().then(
        (collect) => {
          if (current !== generation) return;
          try {
            target.agents.value = collect(contributions);
            target.ready.value = true;
          } catch (error) {
            onError(error instanceof Error ? error.message : String(error));
          }
        },
        (error: unknown) => {
          if (current === generation) onError(`load consumer: ${error instanceof Error ? error.message : String(error)}`);
        },
      );
    }
    return () => {
      if (current !== generation) return;
      const disposed = ++generation;
      // The sync applies the next snapshot right after this dispose, in the
      // same tick. Clear only when none followed (sync stopped, unregistered).
      queueMicrotask(() => {
        if (generation !== disposed) return;
        target.agents.value = [];
        target.ready.value = false;
      });
    };
  };
}
