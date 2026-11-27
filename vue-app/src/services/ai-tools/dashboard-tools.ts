import type { AiCapability } from "@/domain/ai/capability-registry";
import { useVueI18n } from "@/i18n/messages";
import { tool } from "./shared";

export interface DashboardToolDependencies {
  overview(): Promise<unknown>;
  widgets(): Promise<unknown>;
  widgetData(id: string, signal: AbortSignal): Promise<unknown>;
}

const { t } = useVueI18n();

export function createDashboardCapabilities(deps: DashboardToolDependencies): AiCapability[] {
  const metadata = { module: "dashboard", target: "none", mode: "read", risk: "read" } as const;
  return [
    {
      ...metadata,
      tool: tool("dashboard.get_overview", "Read dashboard connections, recent sessions, quick actions and module counts."),
      execute: () => deps.overview(),
    },
    {
      ...metadata,
      tool: tool("dashboard.list_widgets", "List built-in and contributed dashboard widgets and their IDs."),
      execute: () => deps.widgets(),
    },
    {
      ...metadata,
      tool: tool("dashboard.get_widget_data", "Read a dashboard widget using its ID from dashboard.list_widgets.", {
        id: { type: "string" },
      }, ["id"]),
      execute: async ({ input, signal }) => {
        const id = typeof input.id === "string" ? input.id.trim() : "";
        if (!id) throw new Error(t("settings.agentControl.invalidText", { field: "id" }));
        return deps.widgetData(id, signal);
      },
    },
  ];
}
