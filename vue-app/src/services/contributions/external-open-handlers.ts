import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";
export const externalHandlerIds = new Set<string>();
let lastError: string | undefined;
export const externalOpenSyncError = () => lastError;

// Discovery only. Dispatch always resolves again in the Go host.
export function registerExternalOpenHandlerConsumer(): () => void {
  return registerContributionConsumer({ point: "externalOpenHandlers", apply: lazyContributionApply(async () => {
    const { validExternalContributions } = await import("@/domain/plugins/external-open");
    return contributions => {
      externalHandlerIds.clear();
      for (const id of validExternalContributions(contributions)) externalHandlerIds.add(id);
      return () => externalHandlerIds.clear();
    };
  }, message => { lastError = message; }) });
}
