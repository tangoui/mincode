/**
 * Data widget requests (`dashboardWidgets`, kind "data"). The host resolves
 * the widget's declared `read` action by reference, runs it with a 10 s
 * timeout and a 16 KiB result cap, and returns plain-text items
 * (backend/plugin/dashboard_widget_data.go). The result is re-validated here.
 * Browser preview has no plugin host and reports the widget unavailable.
 */
import { parseDashboardWidgetData, type DashboardWidgetData } from "@/domain/plugins/dashboard-widgets";
import {
  cancelContributionRequest,
  newContributionRequestId,
  parseContributionError,
  type ContributionError,
} from "@/services/plugin-contribution-requests";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export interface DashboardWidgetDataRequest {
  promise: Promise<DashboardWidgetData>;
  cancel(): void;
}

export class DashboardWidgetDataError extends Error {
  constructor(readonly error: ContributionError) {
    super(error.detail || error.code);
  }
}

export function fetchDashboardWidgetData(pluginId: string, widgetId: string): DashboardWidgetDataRequest {
  if (!isDesktopRuntime()) {
    return {
      promise: Promise.reject(new DashboardWidgetDataError({ code: "unavailable", detail: "" })),
      cancel: () => undefined,
    };
  }
  const requestId = newContributionRequestId();
  let settled = false;
  const promise = invokeBackend<unknown>("plugin_host_dashboard_widget_data", { pluginId, widgetId, requestId }).then(
    (value) => {
      settled = true;
      const data = parseDashboardWidgetData(value);
      if (!data) throw new DashboardWidgetDataError({ code: "invalid_result", detail: "" });
      return data;
    },
    (error: unknown) => {
      settled = true;
      throw new DashboardWidgetDataError(parseContributionError(error));
    },
  );
  return {
    promise,
    cancel: () => {
      if (!settled) void cancelContributionRequest(requestId);
    },
  };
}
