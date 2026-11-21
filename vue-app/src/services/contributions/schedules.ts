/**
 * Consumer of the `schedules` contribution point (process plugins only). It
 * only lists the schedules for the plugin manager: the Go host runs them
 * (backend/plugin/schedules_runner.go), so registering data never
 * starts a plugin.
 */
import { SCHEDULES_CONTRIBUTION_POINT } from "@/domain/plugins/schedules";
import { registerContributionConsumer } from "@/services/plugin-contributions";
import { usePluginSchedulesStore } from "@/stores/plugin-schedules";

export function registerScheduleContributionConsumer(): () => void {
  return registerContributionConsumer({
    point: SCHEDULES_CONTRIBUTION_POINT,
    apply: (contributions) => {
      const store = usePluginSchedulesStore();
      store.setContributions(contributions);
      return () => store.clearContributions();
    },
  });
}
