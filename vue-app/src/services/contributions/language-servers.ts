/**
 * Consumer of the `languageServers` contribution point (process plugins with
 * the shell capability, active in this workspace). It stores the re-validated
 * registry the project LSP runtime routes languages with. The validation and
 * store load only when a plugin contributes servers, keeping them out of the
 * entry chunk. Registering data never starts anything: a plugin server starts
 * only for a project where the user enabled and confirmed it, through
 * plugin_host_language_server_start (services/plugin-language-server-service.ts).
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function languageServersSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the languageServers consumer; a failed refresh keeps the servers already registered. */
export function registerLanguageServerContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "languageServers",
    apply: lazyContributionApply(async () => {
      const [{ collectLanguageServers }, { usePluginLanguageServersStore }] = await Promise.all([
        import("@/domain/plugins/language-servers"),
        import("@/stores/plugin-language-servers"),
      ]);
      lastSyncError = undefined;
      return (contributions) => {
        const store = usePluginLanguageServersStore();
        const result = collectLanguageServers(contributions);
        store.setContributions(result.servers, result.issues);
        return () => store.clearContributions();
      };
    }, onError),
    onError,
  });
}
