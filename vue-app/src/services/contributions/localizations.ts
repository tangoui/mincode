/**
 * Consumer of the `localizations` contribution point (declarative and process
 * plugins; global scope). The Go host validates entries, language tags,
 * characters and asset structure and serves canonical assets on demand;
 * `parseLocalePackContributions` re-checks the snapshot entries and the
 * runtime (services/locale-pack-loader.ts) fetches, validates and installs
 * only the pack the user selected. Registering data never starts a plugin
 * process and never switches the UI language on its own.
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh (plugin host unavailable), if any. */
export function localizationSyncError(): string | undefined {
  return lastSyncError;
}

/**
 * Registers the localizations consumer. Disposing drops the packs (the next
 * apply replaces them in the same microtask, so a refresh never flashes the
 * base language) and releases the cached pack assets; a failed refresh keeps
 * the packs already registered.
 */
export function registerLocalizationContributionConsumer(): () => void {
  const apply = lazyContributionApply(
    () => import("@/services/locale-pack-loader").then((module) => module.applyLocalePackContributions),
    (message) => { lastSyncError = message; },
  );
  return registerContributionConsumer({
    point: "localizations",
    apply: (contributions) => {
      lastSyncError = undefined;
      return apply(contributions);
    },
    onError: (message) => {
      lastSyncError = message;
    },
  });
}
