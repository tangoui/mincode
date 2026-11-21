import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h } from "vue";
import HttpWorkspaceView from "@/views/HttpWorkspaceView.vue";
import PluginMenuItems from "@/components/plugins/PluginMenuItems.vue";
import { parseWhenExpression } from "@/domain/commands/when";
import type { PluginMenuItem } from "@/domain/plugins/menus";
import { pluginMenuItems } from "@/services/contributions/menus";
import { useHttpResourcesStore } from "@/stores/http-resources";
import { useLinkedNotesDrawer } from "@/stores/linked-notes-drawer";

const plugin = vi.hoisted(() => ({ execute: vi.fn() }));
vi.mock("@/services/plugin-commands", async () => {
  const { shallowRef } = await import("vue");
  return {
    pluginCommandState: shallowRef({ commands: [{ pluginId: "demo.menus", id: "inspect" }], contexts: {} }),
    executePluginCommand: (...args: unknown[]) => plugin.execute(...args),
    setPluginCommandPaletteOverrides: () => {},
  };
});

const dialogs = vi.hoisted(() => ({ confirm: vi.fn(), success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }));
vi.mock("element-plus", async (importOriginal) => ({
  ...(await importOriginal<typeof import("element-plus")>()),
  ElMessage: { success: dialogs.success, error: dialogs.error, warning: dialogs.warning, info: dialogs.info },
  ElMessageBox: { confirm: dialogs.confirm },
}));

function savedRequest(id: string, name: string, method: string, url: string) {
  return {
    id,
    name,
    method,
    url,
    params: [{ id: `${id}-p`, key: "api_key", value: "query-secret", enabled: true }],
    headers: [{ id: `${id}-h`, key: "Authorization", value: "Bearer header-secret", enabled: true }],
    body: '{"password":"body-secret"}',
    bodyType: "raw",
    documentation: "",
    authType: "bearer",
    authToken: "token-secret",
    authPassword: "basic-secret",
    updatedAt: 0,
  };
}

const LONG_URL = `https://api.example.com/${"a/".repeat(1100)}`;
const resources = vi.hoisted(() => ({ value: null as unknown }));
vi.mock("@/services/http-state-persistence", () => ({
  loadOrMigrateHttpResources: async () => structuredClone(resources.value),
  saveHttpResources: async () => {},
  loadHttpState: async () => null,
  saveHttpState: async () => {},
}));

// AppContextMenu and the Element Plus items need a live popper; these stubs render the menu inline.
const ContextMenu = defineComponent({
  name: "AppContextMenuStub",
  props: { ariaLabel: { type: String, default: "" } },
  emits: ["close"],
  setup(props, { slots }) {
    return () => h("ul", { class: "context-menu-stub", "aria-label": props.ariaLabel }, slots.default?.());
  },
});
const DropdownItem = defineComponent({
  inheritAttrs: false,
  props: { icon: { type: [Object, Function], default: undefined } },
  setup(_, { attrs, slots }) {
    return () => h("li", { role: "menuitem", tabindex: -1, ...attrs }, slots.default?.());
  },
});
const stubs = {
  AppContextMenu: ContextMenu,
  ElDropdownItem: DropdownItem,
  "el-dropdown-item": DropdownItem,
  HttpBodyEditor: true,
  HttpPluginTemplatesPanel: true,
  HttpMockServerPanel: true,
};

const MENU_ITEM: PluginMenuItem = {
  pluginId: "demo.menus",
  pluginName: "Demo Menus",
  menu: "http/request/context",
  command: "inspect",
  title: "Inspect request",
  whenClauses: [],
  group: "",
  order: 0,
  passContext: true,
  hidden: false,
};
const SECRETS = ["query-secret", "header-secret", "body-secret", "token-secret", "basic-secret", "api_key", "Authorization", "pw-secret"];

let wrapper: VueWrapper | undefined;

beforeEach(() => {
  setActivePinia(createPinia());
  resources.value = {
    version: 2,
    environments: [],
    collections: [
      {
        id: "col-1",
        name: "Billing",
        requests: [
          savedRequest("req-1", "List invoices", "get", "https://user:pw-secret@api.example.com/invoices?api_key=query-secret#top"),
          savedRequest("req-2", "Create invoice", "POST", "{{base}}/invoices"),
          savedRequest("req-3", "Long", "GET", LONG_URL),
        ],
        children: [{ id: "col-2", name: "Nested", requests: [savedRequest("req-4", "Child", "DELETE", "http://localhost:8080/x")] }],
      },
    ],
  };
  plugin.execute.mockReset();
  dialogs.confirm.mockResolvedValue(undefined);
  pluginMenuItems.value = [MENU_ITEM];
});
afterEach(() => {
  // A pointer press disarms the guard against the native contextmenu that follows Shift+F10.
  window.dispatchEvent(new MouseEvent("mousedown"));
  wrapper?.unmount();
  wrapper = undefined;
  pluginMenuItems.value = [];
  vi.clearAllMocks();
});

async function mountView(options: { realMenu?: boolean } = {}) {
  // realMenu keeps the real AppContextMenu (focus return, Escape).
  const withoutMenu = Object.fromEntries(Object.entries(stubs).filter(([name]) => name !== "AppContextMenu"));
  wrapper = mount(HttpWorkspaceView, { attachTo: document.body, global: { stubs: options.realMenu ? withoutMenu : stubs } });
  await flushPromises();
  // Expand the nested collection.
  await wrapper.find(".http-collection .http-collection .http-collection__toggle").trigger("click");
  await flushPromises();
}

function row(name: string) {
  const found = wrapper!.findAll(".http-saved-request").find((item) => item.text().includes(name));
  expect(found, name).toBeDefined();
  return found!;
}

// The view also renders the tab context menu; the request menu is the one with the request actions label.
function menu() {
  return wrapper!.findAllComponents(ContextMenu).find((item) => item.props("ariaLabel")) ?? wrapper!.findComponent({ name: "Missing" });
}

function menuItems() {
  return menu().findAll("[role=menuitem]");
}

async function openMenu(name: string) {
  await row(name).trigger("contextmenu");
  await flushPromises();
  expect(menu().exists()).toBe(true);
}

function lastInvocation() {
  const [, , invocation] = plugin.execute.mock.calls.at(-1)! as [string, string, { menu: string; context: Record<string, unknown> }];
  expect(invocation.menu).toBe("http/request/context");
  const serialized = JSON.stringify(invocation.context);
  for (const secret of SECRETS) expect(serialized).not.toContain(secret);
  return invocation.context;
}

describe("HTTP saved request context menu", () => {
  it("opens with Open, Rename, linked notes, Delete and the plugin items after them", async () => {
    await mountView();
    await openMenu("List invoices");
    expect(menu().attributes("aria-label")).toMatch(/请求操作|Request actions/);
    const items = menuItems();
    expect(items.map((item) => item.text())).toEqual([
      expect.stringMatching(/打开请求|Open request/),
      expect.stringMatching(/重命名请求|Rename request/),
      expect.stringMatching(/新建关联笔记|New linked note/),
      expect.stringMatching(/查看关联笔记（0）|View linked notes \(0\)/),
      expect.stringMatching(/删除请求|Delete request/),
      "Inspect request",
    ]);
    expect(items[5]!.attributes("divided")).toBeDefined();
    expect(wrapper!.findComponent(PluginMenuItems).props("host")).toEqual({ "http.method": "GET", "http.scheme": "https" });
  });

  it("passes a sanitized URL and no headers, auth or body", async () => {
    await mountView();
    await openMenu("List invoices");
    await menuItems().at(-1)!.trigger("click");
    await flushPromises();
    expect(lastInvocation()).toEqual({
      collection: { id: "col-1", name: "Billing" },
      request: { id: "req-1", name: "List invoices", method: "GET", url: "https://api.example.com/invoices", redacted: true },
    });
    expect(menu().exists()).toBe(false);
  });

  it("opens for nested requests and from the keyboard", async () => {
    await mountView();
    await row("Child").trigger("keydown", { key: "F10" });
    await flushPromises();
    expect(menu().exists()).toBe(false);
    await row("Child").trigger("keydown", { key: "F10", shiftKey: true });
    await flushPromises();
    expect(menu().exists()).toBe(true);
    await menuItems().at(-1)!.trigger("click");
    await flushPromises();
    expect(lastInvocation()).toEqual({
      collection: { id: "col-2", name: "Nested" },
      request: { id: "req-4", name: "Child", method: "DELETE", url: "http://localhost:8080/x" },
    });

    await row("Create invoice").trigger("keydown", { key: "ContextMenu" });
    await flushPromises();
    expect(wrapper!.findComponent(PluginMenuItems).props("host")).toEqual({ "http.method": "POST", "http.scheme": "" });
  });

  it("applies http.method when clauses and shows only the host items without plugin items", async () => {
    pluginMenuItems.value = [{ ...MENU_ITEM, whenClauses: parseWhenExpression('http.method == "POST"') }];
    await mountView();
    await openMenu("List invoices");
    expect(menuItems()).toHaveLength(5);
    expect(wrapper!.findAll(".app-plugin-menu-item")).toHaveLength(0);
    menu().vm.$emit("close");
    await flushPromises();
    await openMenu("Create invoice");
    expect(menuItems()).toHaveLength(6);
  });

  it("refuses to send a URL over urlMaxLength", async () => {
    await mountView();
    await openMenu("Long");
    await menuItems().at(-1)!.trigger("click");
    await flushPromises();
    expect(plugin.execute).not.toHaveBeenCalled();
    expect(dialogs.error).toHaveBeenCalledWith(expect.stringMatching(/所选对象过长|too long/));
  });

  it("host items behave like the inline buttons", async () => {
    await mountView();
    await openMenu("Create invoice");
    await menuItems()[0]!.trigger("click");
    await flushPromises();
    expect(menu().exists()).toBe(false);
    expect(wrapper!.find(".http-request-tabs").text()).toContain("Create invoice");

    await openMenu("Create invoice");
    await menuItems()[1]!.trigger("click");
    await flushPromises();
    expect(wrapper!.find("[data-http-request-rename='req-2']").exists()).toBe(true);
    await wrapper!.find("[data-http-request-rename='req-2']").trigger("keydown", { key: "Escape" });
    await flushPromises();

    await openMenu("List invoices");
    await menuItems()[4]!.trigger("click");
    await flushPromises();
    expect(dialogs.confirm).toHaveBeenCalledTimes(1);
    expect(wrapper!.findAll(".http-saved-request").some((item) => item.text().includes("List invoices"))).toBe(false);
  });

  it("opens the linked notes drawer for the request with its collection as parent", async () => {
    await mountView();
    const drawer = useLinkedNotesDrawer();
    const open = vi.spyOn(drawer, "open").mockResolvedValue();
    await openMenu("Child");
    await menuItems()[2]!.trigger("click");
    await flushPromises();
    expect(menu().exists()).toBe(false);
    expect(open).toHaveBeenCalledWith(
      { objectType: "http", objectId: "req-4", parentId: "col-2", nameSnapshot: "Child" },
      { createNew: true },
    );

    await openMenu("List invoices");
    await menuItems()[3]!.trigger("click");
    await flushPromises();
    expect(open).toHaveBeenLastCalledWith(
      { objectType: "http", objectId: "req-1", parentId: "col-1", nameSnapshot: "List invoices" },
      { createNew: false },
    );
  });

  it("closes the menu when its request disappears", async () => {
    await mountView();
    await openMenu("List invoices");
    const store = useHttpResourcesStore();
    store.collections[0]!.requests = store.collections[0]!.requests.filter((item) => item.id !== "req-1");
    await flushPromises();
    expect(menu().exists()).toBe(false);
    expect(plugin.execute).not.toHaveBeenCalled();
  });

  it("closes the menu when search or collapse hides its row", async () => {
    await mountView();
    await openMenu("Child");
    await wrapper!.find(".http-collection .http-collection .http-collection__toggle").trigger("click");
    await flushPromises();
    expect(menu().exists()).toBe(false);

    await openMenu("List invoices");
    await wrapper!.findComponent({ name: "AppSearchField" }).vm.$emit("update:modelValue", "create");
    await flushPromises();
    expect(menu().exists()).toBe(false);
  });

  it("does not reopen at the pointer when the browser follows Shift+F10 with a contextmenu event", async () => {
    await mountView();
    const target = row("Create invoice");
    const button = target.find(".http-saved-request__open").element as HTMLElement;
    button.dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true }));
    await flushPromises();
    const anchor = { x: menu().attributes("x"), y: menu().attributes("y") };
    const native = new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 640, clientY: 480 });
    button.dispatchEvent(native);
    await flushPromises();
    expect(native.defaultPrevented).toBe(true);
    expect({ x: menu().attributes("x"), y: menu().attributes("y") }).toEqual(anchor);

    button.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 2 }));
    button.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 300, clientY: 200 }));
    await flushPromises();
    expect({ x: menu().attributes("x"), y: menu().attributes("y") }).toEqual({ x: "300", y: "200" });
  });

  it("announces the keyboard shortcuts and returns focus to the row on Escape", async () => {
    await mountView({ realMenu: true });
    const button = row("Create invoice").find(".http-saved-request__open").element as HTMLElement;
    expect(button.getAttribute("aria-keyshortcuts")).toBe("ContextMenu Shift+F10");
    button.focus();
    button.dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true }));
    await flushPromises();
    await new Promise((resolve) => setTimeout(resolve, 20));
    const opened = [...document.querySelectorAll<HTMLElement>(".app-context-menu__popper .el-dropdown-menu")].find((item) =>
      /请求操作|Request actions/.test(item.getAttribute("aria-label") ?? ""),
    );
    expect(opened).toBeDefined();
    // Element Plus moves focus into the opened menu.
    opened!.focus();
    opened!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await flushPromises();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(opened!.isConnected).toBe(false);
    expect(document.activeElement).toBe(button);
  });
});
