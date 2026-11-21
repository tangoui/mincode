import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, reactive } from "vue";
import PluginMenuItems from "@/components/plugins/PluginMenuItems.vue";
import { parseWhenExpression } from "@/domain/commands/when";
import type { PluginMenuItem } from "@/domain/plugins/menus";
import { pluginMenuItems } from "@/services/contributions/menus";

type KeyInfo = { type: string; ttl: number; value: unknown; binary?: boolean };

// A store double with the real getRedisKey sequence: select the key and clear its info at once, fill it in later.
const store = vi.hoisted(() => ({ value: undefined as unknown as ReturnType<typeof createStore> }));
const pending = new Map<string, { resolve: (info: KeyInfo) => void; reject: (error: Error) => void }>();

vi.mock("@/stores/database-workspace", () => ({ useDatabaseWorkspaceStore: () => store.value }));
vi.mock("@/services/ai-workspace-actions", () => ({ registerAiWorkspaceActionHandler: () => () => {} }));

const plugin = vi.hoisted(() => ({ execute: vi.fn() }));
vi.mock("@/services/plugin-commands", async () => {
  const { shallowRef } = await import("vue");
  return {
    pluginCommandState: shallowRef({ commands: [{ pluginId: "demo.menus", id: "inspect" }], contexts: {} }),
    executePluginCommand: (...args: unknown[]) => plugin.execute(...args),
    setPluginCommandPaletteOverrides: () => {},
  };
});
const bridge = vi.hoisted(() => ({ copyText: vi.fn() }));
vi.mock("@/services/desktop-bridge", () => ({ isDesktopRuntime: () => true, copyText: bridge.copyText }));
const messages = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() }));
vi.mock("element-plus", async (importOriginal) => ({
  ...(await importOriginal<typeof import("element-plus")>()),
  ElMessage: messages,
}));

import RedisWorkspacePanel from "@/components/database/RedisWorkspacePanel.vue";

function createStore() {
  const state = reactive({
    activeWorkspaceScopeId: "w",
    activeConnectionId: "redis-1",
    activeConnection: { id: "redis-1", name: "Cache", dbType: "redis", host: "redis.hidden", port: 6380, username: "acluser", password: "redis-secret-pass" },
    activeSessionId: "",
    activeSchema: "",
    isRedis: true,
    redisDbIndex: 0,
    redisKeys: { keys: [{ key: "a", type: "string", ttl: -1 }, { key: "b", type: "string", ttl: -1 }], nextCursor: "0", total: 2 },
    selectedRedisKey: "",
    redisKeyInfo: undefined as KeyInfo | undefined,
    listRedisKeys: vi.fn(async () => {}),
    saveRedisKey: vi.fn(async () => {}),
    setRedisKeyTtl: vi.fn(async () => {}),
    deleteRedisKey: vi.fn(async () => {}),
    getRedisKey: vi.fn((key: string) => {
      state.selectedRedisKey = key;
      state.redisKeyInfo = undefined;
      return new Promise<void>((resolve, reject) => {
        pending.set(key, {
          resolve: (info) => {
            if (state.selectedRedisKey === key) state.redisKeyInfo = info;
            resolve();
          },
          reject,
        });
      });
    }),
  });
  return state;
}

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
const stubs = { AppContextMenu: ContextMenu, ElDropdownItem: DropdownItem, "el-dropdown-item": DropdownItem };

function mountPanel(options: { realMenu?: boolean } = {}) {
  store.value = createStore();
  pending.clear();
  // realMenu keeps the real AppContextMenu (focus return, Escape) and attaches to the document for focus.
  return options.realMenu
    ? mount(RedisWorkspacePanel, { attachTo: document.body, global: { directives: { loading: {} } } })
    : mount(RedisWorkspacePanel, { global: { directives: { loading: {} }, stubs } });
}

const valuePane = (wrapper: ReturnType<typeof mountPanel>) => wrapper.get(".redis-value-pane");
const select = async (wrapper: ReturnType<typeof mountPanel>, key: string) => {
  await wrapper.findAll(".redis-key-row").find((item) => item.find(".redis-key-row__name").text() === key)!.trigger("click");
  await flushPromises();
};

describe("redis key switching", () => {
  it("keeps the previous key on screen, locked, instead of flashing the placeholder", async () => {
    const wrapper = mountPanel();
    await select(wrapper, "a");
    pending.get("a")!.resolve({ type: "string", ttl: -1, value: "first" });
    await flushPromises();
    expect(valuePane(wrapper).text()).toContain("a");
    expect((valuePane(wrapper).get("textarea").element as HTMLTextAreaElement).value).toBe("first");

    await select(wrapper, "b");
    // Still showing key a while b loads, never the "select a key" placeholder.
    expect(valuePane(wrapper).find(".el-empty").exists()).toBe(false);
    expect(valuePane(wrapper).get("strong").text()).toBe("a");
    const textarea = valuePane(wrapper).get("textarea").element as HTMLTextAreaElement;
    expect(textarea.value).toBe("first");
    expect(textarea.readOnly).toBe(true);
    const saveButton = valuePane(wrapper).get("footer button");
    expect(saveButton.attributes("disabled")).toBeDefined();
    await saveButton.trigger("click");
    expect(store.value.saveRedisKey).not.toHaveBeenCalled();

    pending.get("b")!.resolve({ type: "string", ttl: 30, value: "second" });
    await flushPromises();
    expect(valuePane(wrapper).get("strong").text()).toBe("b");
    expect((valuePane(wrapper).get("textarea").element as HTMLTextAreaElement).value).toBe("second");
    expect(valuePane(wrapper).get("footer button").attributes("disabled")).toBeUndefined();
    wrapper.unmount();
  });

  it("shows neither the placeholder nor an editor while the first key loads", async () => {
    const wrapper = mountPanel();
    expect(valuePane(wrapper).find(".el-empty").exists()).toBe(true);
    await select(wrapper, "a");
    expect(valuePane(wrapper).find(".el-empty").exists()).toBe(false);
    expect(valuePane(wrapper).find("textarea").exists()).toBe(false);
    expect(valuePane(wrapper).find(".redis-value-pending").exists()).toBe(true);
    wrapper.unmount();
  });

  it("falls back to the placeholder when the new key fails to load", async () => {
    const wrapper = mountPanel();
    await select(wrapper, "a");
    pending.get("a")!.resolve({ type: "string", ttl: -1, value: "first" });
    await flushPromises();
    await select(wrapper, "b");
    pending.get("b")!.reject(new Error("gone"));
    await flushPromises();
    expect(valuePane(wrapper).find(".el-empty").exists()).toBe(true);
    expect(valuePane(wrapper).find("textarea").exists()).toBe(false);
    wrapper.unmount();
  });
});

const MENU_ITEM: PluginMenuItem = {
  pluginId: "demo.menus",
  pluginName: "Demo Menus",
  menu: "redis/key/context",
  command: "inspect",
  title: "Inspect key",
  whenClauses: [],
  group: "",
  order: 0,
  passContext: true,
  hidden: false,
};
const SECRETS = ["secret-value", "redis.hidden", "6380", "acluser", "redis-secret-pass"];
const LONG_KEY = "k".repeat(4097);

describe("redis key context menu", () => {
  afterEach(() => {
    window.dispatchEvent(new MouseEvent("mousedown"));
    pluginMenuItems.value = [];
    vi.clearAllMocks();
  });

  function mountWithKeys() {
    const wrapper = mountPanel();
    store.value.redisDbIndex = 3;
    store.value.redisKeys.keys.push({ key: "session:42", type: "HASH", ttl: 120 }, { key: LONG_KEY, type: "string", ttl: -1 });
    store.value.selectedRedisKey = "a";
    store.value.redisKeyInfo = { type: "string", ttl: -1, value: "secret-value" };
    return wrapper;
  }

  const row = (wrapper: ReturnType<typeof mountPanel>, key: string) =>
    wrapper.findAll(".redis-key-row").find((item) => item.find(".redis-key-row__name").text() === key)!;
  const menu = (wrapper: ReturnType<typeof mountPanel>) => wrapper.findComponent(ContextMenu);
  const menuItems = (wrapper: ReturnType<typeof mountPanel>) => menu(wrapper).findAll("[role=menuitem]");

  function lastInvocation() {
    const [, , invocation] = plugin.execute.mock.calls.at(-1)! as [string, string, { menu: string; context: Record<string, unknown> }];
    expect(invocation.menu).toBe("redis/key/context");
    const serialized = JSON.stringify(invocation.context);
    for (const secret of SECRETS) expect(serialized).not.toContain(secret);
    return invocation.context;
  }

  it("opens on right-click, selects the key and puts plugin items after Copy key name", async () => {
    pluginMenuItems.value = [MENU_ITEM];
    const wrapper = mountWithKeys();
    await flushPromises();
    await row(wrapper, "session:42").trigger("contextmenu");
    await flushPromises();
    expect(store.value.getRedisKey).toHaveBeenCalledWith("session:42");
    expect(menu(wrapper).exists()).toBe(true);
    expect(menu(wrapper).attributes("aria-label")).toMatch(/键操作|Key actions/);
    const items = menuItems(wrapper);
    expect(items).toHaveLength(2);
    expect(items[0]!.text()).toMatch(/复制键名|Copy key name/);
    expect(items[1]!.text()).toBe("Inspect key");
    expect(items[1]!.attributes("divided")).toBeDefined();
    expect(wrapper.findComponent(PluginMenuItems).props("host")).toEqual({ "redis.type": "hash", "redis.hasTtl": true });

    await items[1]!.trigger("click");
    await flushPromises();
    expect(lastInvocation()).toEqual({ connection: { id: "redis-1", name: "Cache" }, dbIndex: 3, key: "session:42", type: "hash", ttl: 120 });
    expect(menu(wrapper).exists()).toBe(false);
    wrapper.unmount();
  });

  it("copies the key name and shows only the host item without plugin items", async () => {
    const wrapper = mountWithKeys();
    await flushPromises();
    await row(wrapper, "session:42").trigger("contextmenu");
    await flushPromises();
    const items = menuItems(wrapper);
    expect(items).toHaveLength(1);
    expect(wrapper.findAll(".app-plugin-menu-item")).toHaveLength(0);
    await items[0]!.trigger("click");
    await flushPromises();
    expect(bridge.copyText).toHaveBeenCalledWith("session:42");
    expect(messages.success).toHaveBeenCalled();
    expect(menu(wrapper).exists()).toBe(false);
    wrapper.unmount();
  });

  it("opens from the keyboard with ContextMenu and Shift+F10", async () => {
    pluginMenuItems.value = [MENU_ITEM];
    const wrapper = mountWithKeys();
    await flushPromises();
    await row(wrapper, "session:42").trigger("keydown", { key: "ContextMenu" });
    await flushPromises();
    expect(menu(wrapper).exists()).toBe(true);
    menu(wrapper).vm.$emit("close");
    await flushPromises();
    expect(menu(wrapper).exists()).toBe(false);

    await row(wrapper, "b").trigger("keydown", { key: "F10" });
    await flushPromises();
    expect(menu(wrapper).exists()).toBe(false);
    await row(wrapper, "b").trigger("keydown", { key: "F10", shiftKey: true });
    await flushPromises();
    expect(menu(wrapper).exists()).toBe(true);
    expect(store.value.getRedisKey).toHaveBeenCalledWith("b");
    wrapper.unmount();
  });

  it("evaluates redis.type when clauses", async () => {
    pluginMenuItems.value = [{ ...MENU_ITEM, whenClauses: parseWhenExpression('redis.type == "hash"') }];
    const wrapper = mountWithKeys();
    await flushPromises();
    await row(wrapper, "b").trigger("contextmenu");
    await flushPromises();
    expect(wrapper.findAll(".app-plugin-menu-item")).toHaveLength(0);
    menu(wrapper).vm.$emit("close");
    await flushPromises();
    await row(wrapper, "session:42").trigger("contextmenu");
    await flushPromises();
    expect(wrapper.findAll(".app-plugin-menu-item")).toHaveLength(1);
    wrapper.unmount();
  });

  it("refuses to send a key over redisKeyMaxBytes", async () => {
    pluginMenuItems.value = [MENU_ITEM];
    const wrapper = mountWithKeys();
    await flushPromises();
    await row(wrapper, LONG_KEY).trigger("contextmenu");
    await flushPromises();
    await menuItems(wrapper).at(-1)!.trigger("click");
    await flushPromises();
    expect(plugin.execute).not.toHaveBeenCalled();
    expect(messages.error).toHaveBeenCalledWith(expect.stringMatching(/所选对象过长|too long/));
    wrapper.unmount();
  });

  it("closes the menu and drops the target when the key leaves the list or the database changes", async () => {
    pluginMenuItems.value = [MENU_ITEM];
    const wrapper = mountWithKeys();
    await flushPromises();
    await row(wrapper, "session:42").trigger("contextmenu");
    await flushPromises();
    expect(menu(wrapper).exists()).toBe(true);
    store.value.redisKeys.keys = store.value.redisKeys.keys.filter((item) => item.key !== "session:42");
    await flushPromises();
    expect(menu(wrapper).exists()).toBe(false);

    await row(wrapper, "b").trigger("contextmenu");
    await flushPromises();
    expect(menu(wrapper).exists()).toBe(true);
    store.value.redisDbIndex = 4;
    await flushPromises();
    expect(menu(wrapper).exists()).toBe(false);

    await row(wrapper, "b").trigger("contextmenu");
    await flushPromises();
    store.value.activeConnectionId = "redis-2";
    await flushPromises();
    expect(menu(wrapper).exists()).toBe(false);
    wrapper.unmount();
  });
});

describe("redis key rows from the keyboard", () => {
  afterEach(() => {
    // A pointer press disarms the guard against the native contextmenu that follows Shift+F10.
    window.dispatchEvent(new MouseEvent("mousedown"));
    pluginMenuItems.value = [];
    vi.clearAllMocks();
  });

  const rows = (wrapper: ReturnType<typeof mountPanel>) => wrapper.findAll(".redis-key-row");
  const tabStops = (wrapper: ReturnType<typeof mountPanel>) =>
    rows(wrapper).filter((item) => item.attributes("tabindex") === "0").map((item) => item.find(".redis-key-row__name").text());

  it("renders key rows as plain options, not one ElMenuItem per key", async () => {
    // Thousands of loaded keys: selecting one must not update a component per row.
    const wrapper = mountPanel();
    await flushPromises();
    expect(wrapper.findAllComponents({ name: "ElMenuItem" })).toHaveLength(0);
    expect(wrapper.get(".redis-key-menu").attributes("role")).toBe("listbox");
    store.value.selectedRedisKey = "a";
    await flushPromises();
    const selected = rows(wrapper).filter((item) => item.classes("is-active"));
    expect(selected.map((item) => item.find(".redis-key-row__name").text())).toEqual(["a"]);
    expect(selected[0]!.attributes("aria-selected")).toBe("true");
    expect(rows(wrapper)[0]!.attributes("role")).toBe("option");
    wrapper.unmount();
  });

  it("keeps one row in the tab order: the selected key, else the first", async () => {
    const wrapper = mountPanel();
    await flushPromises();
    expect(tabStops(wrapper)).toEqual(["a"]);
    store.value.selectedRedisKey = "b";
    await flushPromises();
    expect(tabStops(wrapper)).toEqual(["b"]);
    expect(rows(wrapper)[0]!.attributes("aria-keyshortcuts")).toBe("ContextMenu Shift+F10");
    wrapper.unmount();
  });

  it("moves focus with the arrow keys and selects with Enter", async () => {
    const wrapper = mountPanel({ realMenu: true });
    await flushPromises();
    const [first, second] = rows(wrapper);
    (first!.element as HTMLElement).focus();
    await first!.trigger("keydown", { key: "ArrowDown" });
    expect(document.activeElement).toBe(second!.element);
    await flushPromises();
    expect(tabStops(wrapper)).toEqual(["b"]);
    await second!.trigger("keydown", { key: "ArrowUp" });
    expect(document.activeElement).toBe(first!.element);
    await first!.trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(store.value.getRedisKey).toHaveBeenCalledWith("a");
    wrapper.unmount();
  });

  it("opens the menu with the ContextMenu key on the focused row and Escape returns focus to it", async () => {
    const wrapper = mountPanel({ realMenu: true });
    await flushPromises();
    const rowElement = rows(wrapper)[1]!.element as HTMLElement;
    rowElement.focus();
    rowElement.dispatchEvent(new KeyboardEvent("keydown", { key: "ContextMenu", bubbles: true }));
    await flushPromises();
    await new Promise((resolve) => setTimeout(resolve, 20));
    const menu = document.querySelector<HTMLElement>(".app-context-menu__popper .el-dropdown-menu");
    expect(menu?.textContent).toMatch(/复制键名|Copy key name/);
    // Element Plus moves focus into the opened menu.
    menu!.focus();
    menu!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await flushPromises();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(document.querySelector(".app-context-menu__popper .el-dropdown-menu")).toBeNull();
    expect(document.activeElement).toBe(rowElement);
    wrapper.unmount();
  });

  it("does not reopen at the pointer when the browser follows Shift+F10 with a contextmenu event", async () => {
    const wrapper = mountPanel({ realMenu: true });
    await flushPromises();
    const rowElement = rows(wrapper)[1]!.element as HTMLElement;
    rowElement.focus();
    rowElement.dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true }));
    await flushPromises();
    const menu = () => wrapper.findComponent({ name: "AppContextMenu" });
    const anchor = { x: menu().props("x"), y: menu().props("y") };
    // What Chromium / WebView2 dispatch right after the keydown.
    const native = new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 640, clientY: 480 });
    rowElement.dispatchEvent(native);
    await flushPromises();
    expect(native.defaultPrevented).toBe(true);
    expect({ x: menu().props("x"), y: menu().props("y") }).toEqual(anchor);

    // A real right-click afterwards still opens at the pointer.
    rowElement.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 2 }));
    rowElement.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 300, clientY: 200 }));
    await flushPromises();
    expect({ x: menu().props("x"), y: menu().props("y") }).toEqual({ x: 300, y: 200 });
    wrapper.unmount();
  });

  it("retries a selected key that failed to load, as a click does", async () => {
    const wrapper = mountPanel();
    await flushPromises();
    await select(wrapper, "a");
    pending.get("a")!.reject(new Error("gone"));
    await flushPromises();
    expect(store.value.getRedisKey).toHaveBeenCalledTimes(1);
    await rows(wrapper)[0]!.trigger("contextmenu");
    await flushPromises();
    expect(store.value.getRedisKey).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });
});
