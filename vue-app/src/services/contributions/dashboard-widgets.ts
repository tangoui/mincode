/**
 * Consumer of the `dashboardWidgets` contribution point (process plugins
 * only). It stores the re-validated widgets for the dashboard. Registering
 * data never starts a plugin: a view widget activates its plugin only while
 * its card is visible, and a data widget calls its `read` action only while
 * visible (components/dashboard/PluginWidgetCard.vue).
 */
import { DASHBOARD_WIDGETS_CONTRIBUTION_POINT } from "@/domain/plugins/dashboard-widgets";
import { registerContributionConsumer } from "@/services/plugin-contributions";
import { useDashboardWidgetsStore } from "@/stores/dashboard-widgets";

export function registerDashboardWidgetContributionConsumer(): () => void {
  return registerContributionConsumer({
    point: DASHBOARD_WIDGETS_CONTRIBUTION_POINT,
    apply: (contributions) => {
      const store = useDashboardWidgetsStore();
      store.setContributions(contributions);
      return () => store.clearContributions();
    },
  });
}
