import { isDesktopRuntime } from "@/services/desktop-bridge";
import { nativeCall } from "@/services/desktop-runtime";
import { type AppNotificationInput, useNotificationsStore } from "@/stores/notifications";
import { desktopNotificationTarget } from "@/domain/notifications/target";

function fallbackDesktopTarget(input: AppNotificationInput) {
  if (input.source === "ai") return { kind: "ai" };
  if (input.source === "update") return { kind: "view", value: "settings" };
  const views = { db: "database", git: "projects", ssh: "terminal" } as const;
  const value = input.source ? views[input.source as keyof typeof views] : undefined;
  return value ? { kind: "view", value } : undefined;
}

/**
 * Add an application notification from Vue feature code. It becomes an
 * operating-system notification while MinCode is in the background, or always
 * with `system` (for example a timer a plugin was asked to announce).
 */
export function notifyApp(input: AppNotificationInput, options: { system?: boolean } = {}) {
  const item = useNotificationsStore().push(input);
  if (isDesktopRuntime() && (options.system || document.visibilityState !== "visible") && input.level !== "progress") {
    void nativeCall("notify.send", {
      title: input.title,
      body: input.message,
      target: desktopNotificationTarget(input.target) ?? fallbackDesktopTarget(input),
    }).catch(() => undefined);
  }
  return item;
}

/**
 * Operating-system notification for a terminal that wants the user back; clicking
 * it focuses that pane. Not added to the notification center: the tab marker and
 * Dock badge already track it until the pane is looked at.
 */
export function notifyTerminalAttention(title: string, body: string, paneId: string) {
  if (!isDesktopRuntime()) return;
  void nativeCall("notify.send", { title, body, target: { kind: "terminal-pane", value: paneId } }).catch(() => undefined);
}
