/**
 * Consumer of the `terminalColorSchemes` contribution point (declarative and
 * process plugins, always global). The Go host validates each scheme and its
 * contrast floors; the runtime (terminal-color-schemes-runtime.ts, loaded only
 * when a plugin contributes schemes) re-validates before a palette reaches the
 * terminal registry (services/terminal-theme.ts). A stored selection whose
 * plugin is missing keeps its id and renders the default palette. Registering
 * data never starts a plugin process.
 */
import { TERMINAL_COLOR_SCHEMES_CONTRIBUTION_POINT } from "@/domain/themes/terminal-color-scheme-id";
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";
import { useTerminalColorSchemesStore } from "@/stores/terminal-color-schemes";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function terminalColorSchemesSyncError(): string | undefined {
  return lastSyncError;
}

export function registerTerminalColorSchemeContributionConsumer(): () => void {
  let runtime: Promise<typeof import("@/services/contributions/terminal-color-schemes-runtime")> | undefined;
  const load = () => (runtime ??= import("@/services/contributions/terminal-color-schemes-runtime"));
  const onError = (message: string) => {
    lastSyncError = message;
  };
  const apply = lazyContributionApply(async () => (await load()).applyTerminalColorSchemeContributions, onError);
  return registerContributionConsumer({
    point: TERMINAL_COLOR_SCHEMES_CONTRIBUTION_POINT,
    apply: (contributions) => {
      // A snapshot arrived: a stored plugin scheme missing from it is unavailable.
      useTerminalColorSchemesStore().markLoaded();
      return apply(contributions);
    },
    onError,
  });
}
