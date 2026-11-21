import {
  PLUGIN_UI_MAX_TASK_RESPONSE_BYTES,
  parsePluginUITaskSnapshot,
  pluginUIMessageByteLength,
  rememberPluginUITask,
  type PluginUITaskOwnership,
  type PluginUITaskSnapshot,
} from "./protocol.ts";

/**
 * backend event the backend emits whenever a UI task snapshot changes (a
 * plugin.task.changed notification or a status poll revealed it). It names the
 * owning UI session; only that session's page receives the snapshot.
 */
export const PLUGIN_UI_TASK_CHANGED_EVENT = "plugin:ui-task-changed";

export interface PluginUITaskChangedEvent {
  pluginId: string;
  sessionId: string;
  generation: number;
  taskId: string;
  snapshot: PluginUITaskSnapshot;
}

/** Port message sent to the page for a task it started (features.taskEvents). */
export interface PluginUITaskChangedMessage {
  type: "mincode.host.task.changed";
  taskId: string;
  snapshot: PluginUITaskSnapshot;
}

export interface PluginUITaskPushTarget {
  pluginId: string;
  sessionId: string;
  generation: number;
}

/**
 * Parses a task-changed event. The event is broadcast to every open view, so
 * when `target` is given an event for another plugin or session is rejected
 * before its snapshot is validated and measured.
 */
export function parsePluginUITaskChangedEvent(
  value: unknown,
  target?: Pick<PluginUITaskPushTarget, "pluginId" | "sessionId">,
): PluginUITaskChangedEvent | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const raw = value as Record<string, unknown>;
  if (target && (raw.pluginId !== target.pluginId || raw.sessionId !== target.sessionId)) return undefined;
  const generation = Number(raw.generation);
  if (typeof raw.pluginId !== "string" || typeof raw.sessionId !== "string" || !raw.sessionId
    || typeof raw.taskId !== "string" || !Number.isSafeInteger(generation) || generation < 1) return undefined;
  try {
    return { pluginId: raw.pluginId, sessionId: raw.sessionId, generation, taskId: raw.taskId, snapshot: parsePluginUITaskSnapshot(raw.snapshot, raw.taskId) };
  } catch {
    return undefined;
  }
}

/**
 * Returns the message to post to the current page, or undefined when the push
 * belongs to another plugin, session or generation, or to a task this page did
 * not start (or whose ownership expired). Accepted snapshots update the
 * ownership record like a task.get response would.
 */
export function pluginUITaskPushMessage(
  event: PluginUITaskChangedEvent,
  target: PluginUITaskPushTarget,
  tasks: Map<string, PluginUITaskOwnership>,
  now = Date.now(),
): PluginUITaskChangedMessage | undefined {
  if (event.pluginId !== target.pluginId || event.sessionId !== target.sessionId || event.generation !== target.generation) return undefined;
  const owned = tasks.get(event.taskId);
  if (!owned || owned.expires <= now) return undefined;
  const message: PluginUITaskChangedMessage = { type: "mincode.host.task.changed", taskId: event.taskId, snapshot: event.snapshot };
  if (pluginUIMessageByteLength(message) > PLUGIN_UI_MAX_TASK_RESPONSE_BYTES) return undefined;
  rememberPluginUITask(tasks, event.snapshot, now);
  return message;
}
