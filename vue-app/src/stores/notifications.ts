import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import { useVueI18n } from "@/i18n/messages";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import { readNotificationTarget, type NotificationTarget } from "@/domain/notifications/target";
import type { AppNotificationAction } from "@/domain/notifications/plugin-actions";

export type NotificationSource = "db" | "git" | "ssh" | "update" | "ai" | "plugin" | "system";
export type NotificationLevel = "info" | "success" | "warning" | "error" | "progress";

export interface NotificationProgress {
  current: number;
  total?: number;
}

export interface AppNotification {
  id: string;
  source: NotificationSource;
  level: NotificationLevel;
  title: string;
  message?: string;
  detail?: string;
  createdAt: number;
  updatedAt: number;
  readAt?: number;
  progressKey?: string;
  progress?: NotificationProgress;
  target?: NotificationTarget;
  /**
   * Plugin notification buttons (host.notify actions). Memory only: never
   * persisted, so after a restart the notification is back without buttons.
   */
  actions?: AppNotificationAction[];
  /** The plugin's display name for the action buttons' labels; memory only, set together with `actions`. */
  pluginName?: string;
}

export interface AppNotificationInput {
  source?: NotificationSource;
  level?: NotificationLevel;
  title: string;
  message?: string;
  detail?: string;
  progressKey?: string;
  progress?: NotificationProgress;
  target?: NotificationTarget;
  actions?: AppNotificationAction[];
  pluginName?: string;
}

interface NativeNotificationPayload {
  source?: unknown;
  level?: unknown;
  code?: unknown;
  title?: unknown;
  message?: unknown;
  detail?: unknown;
  progressKey?: unknown;
  progress?: unknown;
  target?: unknown;
}

const storageKey = "app_notifications_v2";
const maxNotifications = 200;
const sources = new Set<NotificationSource>(["db", "git", "ssh", "update", "ai", "plugin", "system"]);
const levels = new Set<NotificationLevel>(["info", "success", "warning", "error", "progress"]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === "object" && !Array.isArray(value));
}

function isNotificationSource(value: unknown): value is NotificationSource {
  return typeof value === "string" && sources.has(value as NotificationSource);
}

function isNotificationLevel(value: unknown): value is NotificationLevel {
  return typeof value === "string" && levels.has(value as NotificationLevel);
}

function readText(value: unknown) {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function readTimestamp(value: unknown, fallback: number) {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : fallback;
}

function readProgress(value: unknown): NotificationProgress | undefined {
  if (!isRecord(value) || typeof value.current !== "number" || !Number.isFinite(value.current)) {
    return undefined;
  }
  const total = value.total;
  if (total !== undefined && (typeof total !== "number" || !Number.isFinite(total))) return undefined;
  return total === undefined ? { current: value.current } : { current: value.current, total };
}

function restoreNotification(value: unknown): AppNotification | undefined {
  if (!isRecord(value)) return undefined;
  const id = readText(value.id);
  const title = readText(value.title);
  if (!id || !title) return undefined;
  const createdAt = readTimestamp(value.createdAt, Date.now());
  return {
    id,
    source: isNotificationSource(value.source) ? value.source : "system",
    level: isNotificationLevel(value.level) ? value.level : "info",
    title,
    message: readText(value.message),
    detail: readText(value.detail),
    createdAt,
    updatedAt: readTimestamp(value.updatedAt, createdAt),
    readAt: readTimestamp(value.readAt, 0) || undefined,
    progressKey: readText(value.progressKey),
    progress: readProgress(value.progress),
    target: readNotificationTarget(value.target),
  };
}

function restoreNotifications(value: unknown) {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  return value
    .map(restoreNotification)
    .filter((item): item is AppNotification => Boolean(item && !seen.has(item.id) && seen.add(item.id)))
    .sort((left, right) => right.updatedAt - left.updatedAt)
    .slice(0, maxNotifications);
}


function nativeNotificationInput(payload: NativeNotificationPayload): AppNotificationInput | undefined {
  const { t } = useVueI18n();
  const source = isNotificationSource(payload.source) ? payload.source : "system";
  const level = isNotificationLevel(payload.level) ? payload.level : "info";
  const code = readText(payload.code);
  const message = readText(payload.message);
  const detail = readText(payload.detail);
  const progressKey = readText(payload.progressKey);
  const progress = readProgress(payload.progress);
  const target = readNotificationTarget(payload.target);

  switch (code) {
    case "terminal-session-interrupted":
      return {
        source: "ssh",
        level: "warning",
        title: t("notifications.terminal.interrupted"),
        message: t("notifications.terminal.reconnecting"),
      };
    case "terminal-session-ended":
      return {
        source: "ssh",
        level: "error",
        title: t("notifications.terminal.ended"),
        message: t("notifications.terminal.endedHint"),
      };
    case "terminal-session-recovered":
      return {
        source: "ssh",
        level: "success",
        title: t("notifications.terminal.recovered"),
      };
    case "ssh-legacy-algorithms-fallback":
      return {
        source: "ssh",
        level: "warning",
        title: t("notifications.ssh.legacyAlgorithmsFallback"),
        message: t("notifications.ssh.legacyAlgorithmsFallbackHint"),
      };
    default: {
      const title = readText(payload.title);
      return title ? { source, level, title, message, detail, progressKey, progress, target } : undefined;
    }
  }
}

export const useNotificationsStore = defineStore("notifications", () => {
  const items = ref<AppNotification[]>([]);
  const hydrated = ref(false);
  const listening = ref(false);
  const unreadCount = computed(() => items.value.filter((item) => !item.readAt).length);
  let stopListening: (() => void) | undefined;

  async function hydrate() {
    const stored = await loadPersistedValue<unknown>(storageKey);
    // Notifications pushed while loading win over stored copies (same id or progressKey).
    const pending = items.value;
    const pendingIds = new Set(pending.map((item) => item.id));
    const pendingProgressKeys = new Set(pending.flatMap((item) => (item.progressKey ? [item.progressKey] : [])));
    const restored = restoreNotifications(stored).filter(
      (item) => !pendingIds.has(item.id) && !(item.progressKey && pendingProgressKeys.has(item.progressKey)),
    );
    items.value = [...pending, ...restored]
      .sort((left, right) => right.updatedAt - left.updatedAt)
      .slice(0, maxNotifications);
    hydrated.value = true;
    if (pending.length) persist();
  }

  function persist() {
    if (!hydrated.value) return;
    // Action buttons and their plugin name stay in memory (the plugin may be updated or gone after a restart).
    const stored = items.value.slice(0, maxNotifications).map(({ actions: _actions, pluginName: _pluginName, ...rest }) => rest);
    void savePersistedValue(storageKey, stored);
  }

  function push(input: AppNotificationInput) {
    const now = Date.now();
    const existing = input.progressKey
      ? items.value.find((item) => item.progressKey === input.progressKey)
      : undefined;
    const item: AppNotification = {
      id: existing?.id ?? crypto.randomUUID(),
      source: input.source ?? "system",
      level: input.level ?? "info",
      title: input.title,
      message: input.message,
      detail: input.detail,
      createdAt: existing?.createdAt ?? now,
      updatedAt: now,
      readAt: existing?.readAt,
      progressKey: input.progressKey,
      progress: input.progress,
      target: input.target ?? existing?.target,
      ...(input.actions?.length ? { actions: input.actions, pluginName: input.pluginName } : {}),
    };
    items.value = [item, ...items.value.filter((entry) => entry.id !== item.id)].slice(0, maxNotifications);
    persist();
    return item;
  }

  function markRead(id: string) {
    const item = items.value.find((entry) => entry.id === id);
    if (!item || item.readAt) return;
    item.readAt = Date.now();
    item.updatedAt = item.readAt;
    persist();
  }

  function markAllRead() {
    const now = Date.now();
    let changed = false;
    for (const item of items.value) {
      if (!item.readAt) {
        item.readAt = now;
        item.updatedAt = now;
        changed = true;
      }
    }
    if (changed) persist();
  }

  function remove(id: string) {
    const next = items.value.filter((item) => item.id !== id);
    if (next.length === items.value.length) return;
    items.value = next;
    persist();
  }

  function clearRead() {
    const next = items.value.filter((item) => !item.readAt);
    if (next.length === items.value.length) return;
    items.value = next;
    persist();
  }

  function ensureListening() {
    if (listening.value || !isDesktopRuntime()) return;
    listening.value = true;
    stopListening = listenAppEvent<NativeNotificationPayload>("app-notification", (payload) => {
      const input = nativeNotificationInput(payload);
      if (input) push(input);
    });
  }

  function dispose() {
    stopListening?.();
    stopListening = undefined;
    listening.value = false;
  }

  return {
    items,
    hydrated,
    unreadCount,
    hydrate,
    push,
    markRead,
    markAllRead,
    remove,
    clearRead,
    ensureListening,
    dispose,
  };
});

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useNotificationsStore, import.meta.hot));
}
