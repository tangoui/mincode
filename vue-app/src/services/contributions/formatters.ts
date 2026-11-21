/**
 * Consumer of the `formatters` contribution point (process plugins active in
 * this workspace). The runtime (re-validation, store, Monaco stage, picker
 * dialog and i18n wiring in formatters-runtime.ts) loads only when a plugin
 * contributes formatters. Built-in formatting stays the default: a plugin
 * formatter runs only after the user picks it, and format on save is a
 * separate per-language opt-in. Registering data never starts a plugin
 * process and never loads Monaco.
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function formattersSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the formatters consumer; a failed refresh keeps the formatters already registered. */
export function registerFormatterContributionConsumer(): () => void {
  let runtime: Promise<typeof import("@/services/contributions/formatters-runtime")> | undefined;
  const load = () => (runtime ??= import("@/services/contributions/formatters-runtime"));
  const onError = (message: string) => {
    lastSyncError = message;
  };
  const apply = lazyContributionApply(async () => {
    const module = await load();
    lastSyncError = undefined;
    return module.applyFormatterContributions;
  }, onError);
  const unregister = registerContributionConsumer({
    point: "formatters",
    apply: (contributions) => {
      // An empty snapshot is a successful refresh too.
      if (contributions.length === 0) lastSyncError = undefined;
      return apply(contributions);
    },
    onError,
  });
  return () => {
    unregister();
    void runtime?.then((module) => module.stopFormatterRuntime(), () => undefined);
  };
}
