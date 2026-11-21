import {
  desktopNotificationTarget,
  type NotificationTarget,
} from "@/domain/notifications/target";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export const NOTIFICATION_NAVIGATE_EVENT = "mincode:notification-navigate";

export function requestNotificationNavigation(target: NotificationTarget) {
  if (isDesktopRuntime()) {
    const nativeTarget = desktopNotificationTarget(target);
    if (nativeTarget) void invokeBackend("desktop_navigate", nativeTarget).catch(() => undefined);
    return;
  }
  window.dispatchEvent(new CustomEvent<NotificationTarget>(NOTIFICATION_NAVIGATE_EVENT, {
    detail: target,
  }));
}

/** Browser-preview fallback; the desktop uses desktop:navigate so popup windows focus the main window. */
export function listenNotificationNavigation(
  listener: (target: NotificationTarget) => void | Promise<void>,
) {
  const handler = (event: Event) => {
    const target = (event as CustomEvent<NotificationTarget>).detail;
    if (target) void listener(target);
  };
  window.addEventListener(NOTIFICATION_NAVIGATE_EVENT, handler);
  return () => window.removeEventListener(NOTIFICATION_NAVIGATE_EVENT, handler);
}
