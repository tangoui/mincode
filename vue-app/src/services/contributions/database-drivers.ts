/**
 * Consumer of the `databaseDrivers` contribution point (process plugins with
 * the `database.driver` capability). It stores the re-validated driver
 * descriptors for the connection dialog catalog (stores/database-drivers.ts).
 * Registering data never starts a plugin process and never routes a
 * connection: the Go host resolves each connect by `(pluginId, driverId)` and
 * releases credentials only under a recorded grant
 * (services/database-driver-service.ts).
 */
import { DATABASE_DRIVERS_CONTRIBUTION_POINT } from "@/domain/plugins/database-drivers";
import { registerContributionConsumer } from "@/services/plugin-contributions";
import { useDatabaseDriversStore } from "@/stores/database-drivers";

let applyGeneration = 0;

export function registerDatabaseDriverContributionConsumer(): () => void {
  return registerContributionConsumer({
    point: DATABASE_DRIVERS_CONTRIBUTION_POINT,
    apply: (contributions) => {
      const store = useDatabaseDriversStore();
      const token = ++applyGeneration;
      store.setContributions(contributions);
      return () => {
        // The framework disposes right before the next apply; clear only when
        // no apply follows so the catalog does not flash empty on refresh.
        queueMicrotask(() => {
          if (applyGeneration === token) store.clearContributions();
        });
      };
    },
    onError: (message) => {
      useDatabaseDriversStore().setSyncError(message);
    },
  });
}
