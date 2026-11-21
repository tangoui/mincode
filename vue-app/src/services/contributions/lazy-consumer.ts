/**
 * Keeps a contribution consumer's runtime (validation, stores, editor wiring)
 * out of the entry chunk: the runtime module is imported only when a snapshot
 * actually contains entries of the point. An empty snapshot applies nothing,
 * because the previous snapshot's cleanup already ran.
 */
import type { PointContribution } from "@/domain/plugins/contributions";

export type LazyApply = (contributions: PointContribution[]) => () => void;

/**
 * Wraps a lazily loaded `apply`. A cleanup that runs before the module has
 * loaded cancels the pending apply, so snapshots never apply out of order.
 */
export function lazyContributionApply(load: () => Promise<LazyApply>, onError: (message: string) => void) {
  return (contributions: PointContribution[]): (() => void) | void => {
    if (contributions.length === 0) return;
    let disposed = false;
    let cleanup: (() => void) | undefined;
    void load().then(
      (apply) => {
        if (disposed) return;
        try {
          cleanup = apply(contributions);
        } catch (error) {
          onError(error instanceof Error ? error.message : String(error));
        }
      },
      (error: unknown) => onError(`load consumer: ${error instanceof Error ? error.message : String(error)}`),
    );
    return () => {
      disposed = true;
      const current = cleanup;
      cleanup = undefined;
      current?.();
    };
  };
}
