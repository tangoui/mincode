import {
  normalizeSocketIOSavedEvents,
  type SocketIOSavedEvent,
} from "@/domain/http/socketio";
import {
  loadSecurePersistedValue,
  saveSecurePersistedValue,
} from "@/services/secure-persistent-store";

export const HTTP_SOCKETIO_EVENTS_STORAGE_KEY = "app_http_socketio_events_v1";

/** 事件参数可能含令牌，浏览器快照只保留事件名。 */
function browserSnapshot(items: readonly SocketIOSavedEvent[]) {
  return items.map((item) => ({ ...item, args: "" }));
}

export async function loadSocketIOSavedEvents() {
  return normalizeSocketIOSavedEvents(
    await loadSecurePersistedValue<unknown>(HTTP_SOCKETIO_EVENTS_STORAGE_KEY)
  );
}

export function saveSocketIOSavedEvents(items: readonly SocketIOSavedEvent[]) {
  return saveSecurePersistedValue(
    HTTP_SOCKETIO_EVENTS_STORAGE_KEY,
    items,
    browserSnapshot(items),
    { strict: true }
  );
}
