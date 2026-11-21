/**
 * Consumer of the `jsonValidation` contribution point (declarative and
 * process plugins; a process plugin's schemas follow its workspace scope).
 * The Go host validates entries and sanitizes every schema asset (same-
 * document `$ref` only, no nested `$id`, bounded size/depth/regexes). The
 * runtime (re-validation and the Monaco stage in json-validation-runtime.ts)
 * loads only when a plugin contributes schemas. Registering data never
 * starts a plugin process and never loads Monaco.
 */
import type { JsonValidationIssue } from "@/domain/plugins/json-validation";
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

export type { JsonValidationIssue } from "@/domain/plugins/json-validation";

type Runtime = typeof import("@/services/contributions/json-validation-runtime");

let loaded: Runtime | undefined;
let lastSyncError: string | undefined;

/** Issues of the last applied snapshot plus Monaco-stage issues, for diagnostics. */
export function jsonValidationIssues(): readonly JsonValidationIssue[] {
  return loaded?.jsonValidationRuntimeIssues() ?? [];
}

/** The error of the last failed refresh or apply, if any. */
export function jsonValidationSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the jsonValidation consumer; a failed refresh keeps the schemas already registered. */
export function registerJsonValidationContributionConsumer(): () => void {
  let runtime: Promise<Runtime> | undefined;
  const load = () =>
    (runtime ??= import("@/services/contributions/json-validation-runtime").then((module) => (loaded = module)));
  const onError = (message: string) => {
    lastSyncError = message;
  };
  const apply = lazyContributionApply(async () => {
    const module = await load();
    lastSyncError = undefined;
    return module.applyJsonValidationContributions;
  }, onError);
  return registerContributionConsumer({
    point: "jsonValidation",
    apply: (contributions) => {
      // An empty snapshot is a successful refresh too.
      if (contributions.length === 0) lastSyncError = undefined;
      return apply(contributions);
    },
    onError,
  });
}
