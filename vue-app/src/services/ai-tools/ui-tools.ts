import type { AiCapability, AiCapabilityRisk } from "@/domain/ai/capability-registry";
import { useVueI18n } from "@/i18n/messages";
import { safeAiText, tool } from "./shared";
import { workspaceViews, type WorkspaceView } from "@/types/workspace";

const { t } = useVueI18n();
const text = { type: "string" };
const resourceKinds = ["database", "ssh", "sftp", "http_request", "note", "terminal"] as const;
const notificationLevels = ["info", "success", "warning", "error"] as const;

export type UiResource = { kind: (typeof resourceKinds)[number]; id: string };

export interface UiNavigationDependencies {
  listTabs(): { activeView: WorkspaceView; tabs: ReadonlyArray<{ id: string; view: WorkspaceView }> };
  getActive(): Promise<unknown>;
  openView(view: WorkspaceView): Promise<{ view: WorkspaceView; tabId?: string }>;
  openSettings(): Promise<void>;
  openResource(resource: UiResource, signal: AbortSignal): Promise<unknown>;
  closeTab(id: string): Promise<void>;
  notify(input: { title: string; message: string; level: (typeof notificationLevels)[number] }): string;
}

type Input = Record<string, unknown>;

function stringValue(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

function requireText(input: Input, field: string) {
  const value = stringValue(input[field]);
  if (!value) throw new Error(t("settings.agentControl.invalidText", { field }));
  return value;
}

function navigationTool(
  name: string,
  description: string,
  risk: AiCapabilityRisk,
  properties: Input,
  required: string[],
  run: (input: Input, signal: AbortSignal) => unknown,
): AiCapability {
  return {
    module: "ui",
    target: "none",
    risk,
    mode: risk === "read" ? "read" : "write",
    tool: tool(`ui.${name}`, description, properties, required),
    execute: ({ input, signal }) => run(input, signal),
  };
}

export function createUiCapabilities(navigation: UiNavigationDependencies): AiCapability[] {
  return [
    navigationTool("list_views", "List workspace views, the active view, and open tabs.", "read", {}, [],
      () => ({ views: workspaceViews, ...navigation.listTabs() })),
    navigationTool("open_view", "Open a MinCode workspace view.", "write",
      { view: { type: "string", enum: workspaceViews } }, ["view"], async (input) => {
        const view = workspaceViews.find((candidate) => candidate === input.view);
        if (!view) throw new Error(t("ai.navigation.unknownView", { view: stringValue(input.view) }));
        if (view === "settings") {
          await navigation.openSettings();
          return { view };
        }
        return navigation.openView(view);
      }),
    navigationTool("open_resource", "Open a saved resource by ID.", "write", {
      kind: { type: "string", enum: resourceKinds }, id: text,
    }, ["kind"], (input, signal) => {
      const kind = resourceKinds.find((candidate) => candidate === input.kind);
      if (!kind) throw new Error(t("ai.navigation.unknownKind", { kind: stringValue(input.kind) }));
      return navigation.openResource({ kind, id: requireText(input, "id") }, signal);
    }),
    navigationTool("get_active", "Read the current workspace view, tab, and selected resource.", "read", {}, [],
      () => navigation.getActive()),
    navigationTool("close_tab", "Close a workspace tab by ID.", "write", { tabId: text }, ["tabId"], async (input) => {
      const tabId = requireText(input, "tabId");
      await navigation.closeTab(tabId);
      return { tabId, closed: true };
    }),
    navigationTool("notify", "Post a notification in MinCode.", "write", {
      title: text, message: text, level: { type: "string", enum: notificationLevels },
    }, ["title", "message", "level"], (input) => {
      const level = notificationLevels.find((candidate) => candidate === input.level);
      if (!level) throw new Error(t("ai.navigation.unknownLevel", { level: stringValue(input.level) }));
      const title = safeAiText(requireText(input, "title"), 200);
      const message = safeAiText(requireText(input, "message"));
      return { id: navigation.notify({ title, message, level }) };
    }),
  ];
}
