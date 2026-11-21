/**
 * Consumer of the `linters` contribution point (process plugins active in
 * this workspace). The runtime (re-validation, store, Monaco stage and i18n
 * wiring in linters-runtime.ts) loads only when a plugin contributes linters.
 * Plugin linters are off by default: document text reaches a linter only for
 * languages where the user selected it in Settings, and while typing only
 * after the separate per-language opt-in. Registering data never starts a
 * plugin process and never loads Monaco.
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function lintersSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the linters consumer; a failed refresh keeps the linters already registered. */
export function registerLinterContributionConsumer(): () => void {
  let runtime: Promise<typeof import("@/services/contributions/linters-runtime")> | undefined;
  const load = () => (runtime ??= import("@/services/contributions/linters-runtime"));
  const onError = (message: string) => {
    lastSyncError = message;
  };
  const unregister = registerContributionConsumer({
    point: "linters",
    apply: lazyContributionApply(async () => {
      const module = await load();
      lastSyncError = undefined;
      return module.applyLinterContributions;
    }, onError),
    onError,
  });
  return () => {
    unregister();
    void runtime?.then((module) => module.stopLinterRuntime(), () => undefined);
  };
}
