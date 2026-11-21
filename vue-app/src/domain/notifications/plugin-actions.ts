/**
 * Action buttons of a plugin notification (`host.notify` `actions`, host
 * 0.3.0). Go already validated them (backend/plugin/notify_clipboard.go);
 * the frontend re-checks the same rules on the event and drops the whole
 * group when any entry is off, so a malformed event never shows a partial
 * row. Every action belongs to the notifying plugin: `pluginId` is the
 * event's, never a field of the action. Actions live only in memory
 * (stores/notifications.ts strips them before persisting).
 */
export interface AppNotificationAction {
  id: string;
  title: string;
  pluginId: string;
  command: string;
}

/** Mirrors Go notifyActionsMax / notifyActionTitleMaxRunes (not part of the contribution contract). */
export const NOTIFICATION_ACTIONS_MAX = 2;
export const NOTIFICATION_ACTION_TITLE_MAX_RUNES = 24;

const ACTION_ID = /^[A-Za-z0-9._-]{1,32}$/;
// Mirrors Go contributionIDPattern (plugin command IDs may contain ":", e.g. "sync:retry").
const COMMAND_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
// Mirrors Go contextRuneRefused: control (Cc, incl. \n and \t), format (Cf: bidi overrides,
// zero-width) and U+2028 / U+2029 would let a button title span lines, reorder or hide text.
const REFUSED_RUNE = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u;

function parseAction(raw: unknown, pluginId: string): AppNotificationAction | undefined {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return undefined;
  const { id, title, command } = raw as Record<string, unknown>;
  if (typeof id !== "string" || !ACTION_ID.test(id)) return undefined;
  if (typeof command !== "string" || !COMMAND_ID.test(command)) return undefined;
  if (typeof title !== "string" || REFUSED_RUNE.test(title)) return undefined;
  const text = title.trim();
  if (!text || [...text].length > NOTIFICATION_ACTION_TITLE_MAX_RUNES) return undefined;
  return { id, title: text, pluginId, command };
}

/** The validated actions of a `plugin:notify` event, or undefined when there are none or any is invalid. */
export function parsePluginNotificationActions(raw: unknown, pluginId: string): AppNotificationAction[] | undefined {
  if (!pluginId || !Array.isArray(raw) || raw.length === 0 || raw.length > NOTIFICATION_ACTIONS_MAX) return undefined;
  const actions: AppNotificationAction[] = [];
  const ids = new Set<string>();
  for (const entry of raw) {
    const action = parseAction(entry, pluginId);
    if (!action || ids.has(action.id)) return undefined;
    ids.add(action.id);
    actions.push(action);
  }
  return actions;
}
