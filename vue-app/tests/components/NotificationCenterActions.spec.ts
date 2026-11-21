import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import NotificationCenterButton from "@/components/layout/NotificationCenterButton.vue";
import { useNotificationsStore } from "@/stores/notifications";

const runShellCommand = vi.hoisted(() => vi.fn(async () => undefined));
vi.mock("@/services/plugin-shell-commands", () => ({ runShellCommand }));

async function openDrawer(wrapper: ReturnType<typeof mount>) {
  await wrapper.find(".app-notification-button").trigger("click");
  await flushPromises();
}

// The button group is an async component (kept out of the entry chunk).
async function actionGroup() {
  return vi.waitFor(() => {
    const group = document.body.querySelector<HTMLElement>('[role="group"].notification-list__actions');
    if (!group) throw new Error("action group not rendered yet");
    return group;
  });
}

function pushPluginNotification() {
  return useNotificationsStore().push({
    source: "plugin",
    level: "error",
    title: "Sync · 同步失败",
    pluginName: "Sync",
    target: { kind: "plugin", pluginId: "p" },
    actions: [
      { id: "retry", title: "重试", pluginId: "p", command: "sync.retry" },
      { id: "log", title: "查看日志", pluginId: "p", command: "sync.log" },
    ],
  });
}

describe("NotificationCenterButton plugin actions", () => {
  afterEach(() => runShellCommand.mockClear());

  it("renders a labelled group of action buttons outside the notification button", async () => {
    pushPluginNotification();
    const wrapper = mount(NotificationCenterButton, { attachTo: document.body });
    await openDrawer(wrapper);

    const group = await actionGroup();
    expect(group.getAttribute("aria-label")).toBe("Sync 的操作");
    const buttons = [...group.querySelectorAll<HTMLButtonElement>("button")];
    expect(buttons.map((button) => button.textContent?.trim())).toEqual(["重试", "查看日志"]);
    expect(buttons[0].getAttribute("aria-label")).toBe("重试（来自插件 Sync）");
    expect(buttons[0].getAttribute("title")).toBe("重试（来自插件 Sync）");
    expect(buttons[0].closest(".notification-list__content")).toBeNull();
    wrapper.unmount();
  });

  it("marks read, closes the drawer and runs the command from the notification origin", async () => {
    const notifications = useNotificationsStore();
    const item = pushPluginNotification();
    const wrapper = mount(NotificationCenterButton, { attachTo: document.body });
    await openDrawer(wrapper);
    expect(wrapper.findComponent({ name: "ElDrawer" }).props("modelValue")).toBe(true);

    (await actionGroup()).querySelector<HTMLButtonElement>('button[aria-label="重试（来自插件 Sync）"]')?.click();
    await flushPromises();

    expect(notifications.items.find((entry) => entry.id === item.id)?.readAt).toBeTypeOf("number");
    expect(wrapper.findComponent({ name: "ElDrawer" }).props("modelValue")).toBe(false);
    await vi.waitFor(() => expect(runShellCommand).toHaveBeenCalledTimes(1));
    expect(runShellCommand).toHaveBeenCalledWith("notification", "p", "sync.retry");
    wrapper.unmount();
  });

  it("a fast double-click starts only one run until the first settles", async () => {
    let finish: () => void = () => undefined;
    runShellCommand.mockImplementationOnce(() => new Promise<undefined>((resolve) => { finish = () => resolve(undefined); }));
    pushPluginNotification();
    const wrapper = mount(NotificationCenterButton, { attachTo: document.body });
    await openDrawer(wrapper);
    const group = await actionGroup();
    const retry = group.querySelector<HTMLButtonElement>('button[aria-label="重试（来自插件 Sync）"]');
    const log = group.querySelector<HTMLButtonElement>('button[aria-label="查看日志（来自插件 Sync）"]');
    retry?.click();
    retry?.click();
    log?.click();
    await vi.waitFor(() => expect(runShellCommand).toHaveBeenCalledTimes(1));
    await flushPromises();
    expect(runShellCommand).toHaveBeenCalledTimes(1);

    finish();
    await flushPromises();
    retry?.click();
    await vi.waitFor(() => expect(runShellCommand).toHaveBeenCalledTimes(2));
    wrapper.unmount();
  });

  it("renders no action group for a notification without actions", async () => {
    useNotificationsStore().push({ source: "plugin", title: "Plain" });
    const wrapper = mount(NotificationCenterButton, { attachTo: document.body });
    await openDrawer(wrapper);
    expect(document.body.querySelector(".notification-list__content")).not.toBeNull();
    expect(document.body.querySelector('[role="group"]')).toBeNull();
    wrapper.unmount();
  });
});
