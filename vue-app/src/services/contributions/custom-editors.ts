/**
 * Consumer of the `customEditors` contribution point (process plugins). The
 * Go host validates entries and the bound-document commands; this consumer
 * only stores the re-validated editors for the project workspace (Open
 * With…, default resolution, and CustomEditorPanes). Registering data never
 * starts a plugin: the plugin activates when the user opens a document.
 */
import { CUSTOM_EDITORS_CONTRIBUTION_POINT } from "@/domain/plugins/custom-editors";
import { registerContributionConsumer } from "@/services/plugin-contributions";
import { useCustomEditorsStore } from "@/stores/custom-editors";

let lastSyncError: string | undefined;

/** The error of the last failed refresh (plugin host unavailable), if any. */
export function customEditorsSyncError(): string | undefined {
  return lastSyncError;
}

export function registerCustomEditorContributionConsumer(): () => void {
  return registerContributionConsumer({
    point: CUSTOM_EDITORS_CONTRIBUTION_POINT,
    apply: (contributions) => {
      const store = useCustomEditorsStore();
      store.setContributions(contributions);
      lastSyncError = undefined;
      return () => store.clearContributions();
    },
    onError: (message) => {
      lastSyncError = message;
    },
  });
}
