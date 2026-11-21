/**
 * Consumer of the `httpCodeGenerators` contribution point (declarative and
 * process plugins). The Go host validates entries and templates and serves
 * every `.tmpl` by content hash; `buildPluginHttpCodeGeneratorRegistry`
 * re-checks the snapshot. The validation and store load only when a plugin
 * contributes generators, keeping them out of the entry chunk. Registering
 * data never starts a plugin process: templates are fetched, verified and
 * rendered in the WebView only when the user picks a generator in the
 * code-generation dialog, against a redacted request model
 * (services/http-code-generator-service.ts).
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function httpCodeGeneratorsSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the httpCodeGenerators consumer; a failed refresh keeps the generators already registered. */
export function registerHttpCodeGeneratorContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "httpCodeGenerators",
    apply: lazyContributionApply(async () => {
      const [{ buildPluginHttpCodeGeneratorRegistry }, { useHttpCodeGeneratorsStore }] = await Promise.all([
        import("@/domain/plugins/http-code-generators"),
        import("@/stores/http-code-generators"),
      ]);
      lastSyncError = undefined;
      return (contributions) => {
        const store = useHttpCodeGeneratorsStore();
        const registry = buildPluginHttpCodeGeneratorRegistry(contributions);
        store.setContributed(registry.generators, registry.issues);
        return () => store.setContributed([]);
      };
    }, onError),
    onError,
  });
}
