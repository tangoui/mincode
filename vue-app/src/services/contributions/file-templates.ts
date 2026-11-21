/**
 * Consumer of the `fileTemplates` contribution point (declarative and
 * process plugins). The Go host validates entries and serves every `.tmpl`
 * source by content hash; the runtime (re-validation and the templates store
 * in file-templates-runtime.ts) loads only when a plugin contributes
 * templates. Registering data never starts a plugin process and never writes
 * a file: files are created only from the explicit "New from Template"
 * dialog (services/file-template-service.ts).
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh, load or apply, if any. */
export function fileTemplatesSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the fileTemplates consumer; a failed refresh keeps the templates already registered. */
export function registerFileTemplateContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "fileTemplates",
    apply: lazyContributionApply(async () => {
      const module = await import("@/services/contributions/file-templates-runtime");
      lastSyncError = undefined;
      return module.applyFileTemplateContributions;
    }, onError),
    onError,
  });
}
