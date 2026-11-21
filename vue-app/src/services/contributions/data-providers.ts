/**
 * Consumers of the read-only data provider points (process plugins, host
 * 0.3.0). This is the only data provider module in the entry chunk; the
 * runtime (data-providers-runtime.ts: validation, store, one applier per
 * point) loads only once a snapshot has entries. Registering data never
 * starts a plugin.
 *
 * A new snapshot replaces the previous one without an empty step in
 * between: the host disposes the old apply and applies the new one in the
 * same tick, so the disposer only clears when no newer apply followed
 * (sync stopped). Clearing at once would empty the store until the runtime
 * import resolves and collapse the plugin sources open on the sessions page.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { registerContributionConsumer } from "@/services/plugin-contributions";

const POINTS = ["sessionSources", "scmCommitMessage", "fileDecorations"];

export function registerDataProviderContributionConsumers(): () => void {
  let runtime: Promise<typeof import("@/services/contributions/data-providers-runtime")> | undefined;
  const generations: Record<string, number> = {};
  const update = (point: string, contributions: readonly PointContribution[]) => {
    const generation = (generations[point] = (generations[point] ?? 0) + 1);
    // Nothing to clear before the runtime ever loaded.
    if (!contributions.length && !runtime) return;
    const loading = (runtime ??= import("@/services/contributions/data-providers-runtime"));
    void loading.then(
      (module) => generations[point] === generation && module.dataProviderAppliers[point]?.(contributions),
      (error: unknown) => {
        // A failed chunk load is not cached: the next snapshot retries.
        if (runtime === loading) {
          runtime = undefined;
          // eslint-disable-next-line no-console -- A failed lazy chunk is retried on the next snapshot; keep it diagnosable.
          console.warn("[plugins] data provider runtime failed to load", error);
        }
      },
    );
  };
  const disposers = POINTS.map((point) =>
    registerContributionConsumer({
      point,
      apply: (contributions) => {
        update(point, contributions);
        return () => {
          const generation = generations[point];
          queueMicrotask(() => generations[point] === generation && update(point, []));
        };
      },
    }),
  );
  return () => {
    for (const dispose of disposers) dispose();
  };
}
