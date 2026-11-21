import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import NotificationCenterButton from "@/components/layout/NotificationCenterButton.vue";
import { NOTIFICATION_NAVIGATE_EVENT } from "@/services/notification-navigation";
import { useNotificationsStore } from "@/stores/notifications";
import type { NotificationTarget } from "@/domain/notifications/target";
import { notificationTimeLabel } from "@/domain/notifications/time-label";

function notificationButton(title: string) {
  return document.body.querySelector<HTMLButtonElement>(
    `.notification-list__content[aria-label="${title}"]`
  );
}

async function openDrawer(wrapper: ReturnType<typeof mount>) {
  await wrapper.find(".app-notification-button").trigger("click");
  await flushPromises();
}

describe("NotificationCenterButton", () => {
  it("shows the unread count and caps it at 99+", async () => {
    const notifications = useNotificationsStore();
    const wrapper = mount(NotificationCenterButton);
    expect(wrapper.find(".app-notification-button__badge").exists()).toBe(false);

    notifications.push({ title: "Backup finished", source: "db", level: "success" });
    await flushPromises();
    expect(wrapper.find(".app-notification-button__badge").text()).toBe("1");

    for (let index = 0; index < 100; index += 1) notifications.push({ title: `Event ${index}` });
    await flushPromises();
    expect(wrapper.find(".app-notification-button__badge").text()).toBe("99+");
  });

  it("renders only the new row when a notification arrives with the drawer open", async () => {
    const notifications = useNotificationsStore();
    for (let index = 0; index < 50; index += 1) notifications.push({ title: `Event ${index}` });
    const wrapper = mount(NotificationCenterButton, { attachTo: document.body });
    await openDrawer(wrapper);
    expect(document.body.querySelectorAll(".notification-list__item")).toHaveLength(50);

    // Every rendered row formats its timestamp once; an unchanged row must not render again.
    const toISOString = vi.spyOn(Date.prototype, "toISOString");
    try {
      notifications.push({ title: "Fresh event" });
      await flushPromises();
      expect(document.body.querySelectorAll(".notification-list__item")).toHaveLength(51);
      expect(notificationButton("Fresh event")).not.toBeNull();
      expect(toISOString.mock.calls.length).toBeLessThanOrEqual(2);
    } finally {
      toISOString.mockRestore();
      wrapper.unmount();
    }
  });

  it("formats times like toLocaleString", () => {
    const at = Date.UTC(2026, 10, 19, 8, 5, 9);
    for (const locale of ["zh-CN", "en-US"]) {
      expect(notificationTimeLabel(at, locale)).toBe(new Date(at).toLocaleString(locale));
    }
  });

  it("re-renders a row when it is marked read", async () => {
    const notifications = useNotificationsStore();
    const item = notifications.push({ title: "Read me" });
    const wrapper = mount(NotificationCenterButton, { attachTo: document.body });
    await openDrawer(wrapper);
    const card = () => notificationButton("Read me")?.closest(".notification-list__item");
    expect(card()?.classList.contains("is-unread")).toBe(true);
    notifications.markRead(item.id);
    await flushPromises();
    expect(card()?.classList.contains("is-unread")).toBe(false);
    wrapper.unmount();
  });

  it("marks a notification read and navigates to its source view", async () => {
    const notifications = useNotificationsStore();
    const item = notifications.push({ title: "Backup finished", source: "db", level: "success" });
    const targets: NotificationTarget[] = [];
    const listener = (event: Event) => targets.push((event as CustomEvent<NotificationTarget>).detail);
    window.addEventListener(NOTIFICATION_NAVIGATE_EVENT, listener);

    try {
      const wrapper = mount(NotificationCenterButton, { attachTo: document.body });
      await openDrawer(wrapper);

      notificationButton("Backup finished")?.click();
      await flushPromises();

      expect(notifications.items.find((entry) => entry.id === item.id)?.readAt).toBeTypeOf("number");
      expect(notifications.unreadCount).toBe(0);
      expect(targets).toEqual([{ kind: "view", view: "database" }]);
      expect(wrapper.find(".app-notification-button__badge").exists()).toBe(false);
      wrapper.unmount();
    } finally {
      window.removeEventListener(NOTIFICATION_NAVIGATE_EVENT, listener);
    }
  });

  it("marks a notification without a target read but does not navigate", async () => {
    const notifications = useNotificationsStore();
    notifications.push({ title: "Plugin ready", source: "system" });
    const targets: unknown[] = [];
    const listener = (event: Event) => targets.push((event as CustomEvent).detail);
    window.addEventListener(NOTIFICATION_NAVIGATE_EVENT, listener);

    try {
      const wrapper = mount(NotificationCenterButton, { attachTo: document.body });
      await openDrawer(wrapper);

      notificationButton("Plugin ready")?.click();
      await flushPromises();

      expect(notifications.unreadCount).toBe(0);
      expect(targets).toEqual([]);
      expect(notificationButton("Plugin ready")).not.toBeNull();
      wrapper.unmount();
    } finally {
      window.removeEventListener(NOTIFICATION_NAVIGATE_EVENT, listener);
    }
  });
});
