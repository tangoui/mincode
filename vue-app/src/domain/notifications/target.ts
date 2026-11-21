import { workspaceViews, type WorkspaceView } from "@/types/workspace";

export interface AiNotificationTarget {
  kind: "ai";
}

export interface AiConversationNotificationTarget {
  kind: "ai-conversation";
  conversationId: string;
  workspaceScopeId?: string;
}

export interface WorkspaceViewNotificationTarget {
  kind: "view";
  view: WorkspaceView;
}

/** Opens a plugin's page; without viewId its first view. */
export interface PluginNotificationTarget {
  kind: "plugin";
  pluginId: string;
  viewId?: string;
}

export type NotificationTarget =
  | AiNotificationTarget
  | AiConversationNotificationTarget
  | WorkspaceViewNotificationTarget
  | PluginNotificationTarget;

const PLUGIN_TARGET_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

function pluginTarget(pluginId: unknown, viewId: unknown): PluginNotificationTarget | undefined {
  if (typeof pluginId !== "string" || !PLUGIN_TARGET_ID.test(pluginId)) return undefined;
  if (viewId === undefined || viewId === "") return { kind: "plugin", pluginId };
  return typeof viewId === "string" && PLUGIN_TARGET_ID.test(viewId) ? { kind: "plugin", pluginId, viewId } : undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === "object" && !Array.isArray(value));
}

function readText(value: unknown) {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

/** Restores only allowlisted navigation targets from persisted or native notification data. */
export function readNotificationTarget(value: unknown): NotificationTarget | undefined {
  if (!isRecord(value)) return undefined;
  if (value.kind === "ai") return { kind: "ai" };
  if (value.kind === "ai-conversation") {
    const conversationId = readText(value.conversationId);
    if (!conversationId) return undefined;
    const workspaceScopeId = readText(value.workspaceScopeId);
    return workspaceScopeId
      ? { kind: "ai-conversation", conversationId, workspaceScopeId }
      : { kind: "ai-conversation", conversationId };
  }
  if (value.kind === "view") {
    const view = readText(value.view);
    return workspaceViews.includes(view as WorkspaceView)
      ? { kind: "view", view: view as WorkspaceView }
      : undefined;
  }
  if (value.kind === "plugin") return pluginTarget(value.pluginId, value.viewId);
  return undefined;
}

/** Reads the JSON value of a native "plugin" navigation target. */
export function parseDesktopPluginTarget(value: unknown): PluginNotificationTarget | undefined {
  if (typeof value !== "string" || !value.trim()) return undefined;
  try {
    const parsed = JSON.parse(value) as Record<string, unknown>;
    return pluginTarget(parsed?.pluginId, parsed?.viewId);
  } catch {
    return undefined;
  }
}

export function desktopNotificationTarget(target: NotificationTarget | undefined) {
  if (!target) return undefined;
  if (target.kind === "ai") return { kind: "ai" };
  if (target.kind === "ai-conversation") {
    return {
      kind: "ai-conversation",
      value: JSON.stringify({
        conversationId: target.conversationId,
        workspaceScopeId: target.workspaceScopeId,
      }),
    };
  }
  if (target.kind === "plugin") {
    return { kind: "plugin", value: JSON.stringify({ pluginId: target.pluginId, viewId: target.viewId }) };
  }
  return { kind: "view", value: target.view };
}

export function parseDesktopAiConversationTarget(value: unknown): AiConversationNotificationTarget | undefined {
  if (typeof value !== "string" || !value.trim()) return undefined;
  try {
    const parsed = JSON.parse(value) as Record<string, unknown>;
    return readNotificationTarget({ kind: "ai-conversation", ...parsed }) as
      | AiConversationNotificationTarget
      | undefined;
  } catch {
    return undefined;
  }
}
