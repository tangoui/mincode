import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { KeepAlive, defineComponent, h, ref } from "vue";
import TerminalContextMenu from "@/components/terminal/TerminalContextMenu.vue";
import SshDockerTerminal from "@/components/ssh/SshDockerTerminal.vue";
import PluginServiceMenuItems from "@/components/plugins/PluginServiceMenuItems.vue";
import type { PluginMenuItem } from "@/domain/plugins/menus";
import type { PluginServiceEntry } from "@/domain/plugins/services";
import { useVueI18n } from "@/i18n/messages";
import { pluginMenuItems } from "@/services/contributions/menus";

// Plugin services: the provider list is a test-owned ref, runPluginService a spy.
const services = vi.hoisted(() => ({ run: vi.fn(), entries: undefined as unknown as import("vue").ShallowRef<unknown[]> }));
vi.mock("@/services/plugin-services", async (original) => {
  const actual = await original<typeof import("@/services/plugin-services")>();
  const { shallowRef } = await import("vue");
  services.entries = shallowRef<unknown[]>([]);
  return { ...actual, pluginServices: services.entries, runPluginService: (...args: unknown[]) => services.run(...args) };
});

vi.mock("@/services/plugin-commands", async () => {
  const { shallowRef } = await import("vue");
  return {
    pluginCommandState: shallowRef({ commands: [{ pluginId: "demo.menus", id: "explain" }], contexts: {} }),
    executePluginCommand: vi.fn(),
    setPluginCommandPaletteOverrides: () => {},
  };
});

// SshDockerTerminal mounts xterm; a minimal fake keeps the selection under test control.
const xterm = vi.hoisted(() => ({ selection: "" }));
vi.mock("@xterm/xterm", () => {
  const disposable = { dispose: () => {} };
  class Terminal {
    cols = 80;
    rows = 24;
    modes = { bracketedPasteMode: false };
    loadAddon() {}
    open() {}
    attachCustomKeyEventHandler() {}
    onData() {
      return disposable;
    }
    onResize() {
      return disposable;
    }
    getSelection() {
      return xterm.selection;
    }
    write() {}
    writeln() {}
    focus() {}
    clear() {}
    selectAll() {}
    paste() {}
    dispose() {}
  }
  return { Terminal };
});
vi.mock("@xterm/addon-fit", () => ({ FitAddon: class { fit() {} } }));
vi.mock("@/services/terminal-theme", () => ({
  resolveTerminalTheme: () => ({}),
  setTerminalTheme: () => {},
  TERMINAL_COLOR_SCHEME_CHANGED_EVENT: "terminal-color-scheme-changed",
}));
vi.mock("@/services/terminal-preferences", () => ({
  attachTerminalWebglAddon: () => null,
  readTerminalPreferences: () => ({ scrollback: 1000 }),
}));
vi.mock("@/services/terminal-viewport", () => ({
  clampTerminalDimensions: (cols: number, rows: number) => ({ cols, rows }),
  hasTerminalLayout: () => false,
  // Never laid out: the stream is not started in these tests.
  waitForTerminalLayout: () => new Promise(() => {}),
}));
vi.mock("@/services/terminal-plugin-links", () => ({ attachPluginTerminalLinks: () => ({ dispose: () => {} }) }));
vi.mock("@/services/ssh-docker-service", () => ({
  resizeSshDockerExec: vi.fn(),
  startSshDockerExec: vi.fn(),
  startSshDockerLogStream: vi.fn(),
  stopSshDockerStream: vi.fn().mockResolvedValue(undefined),
  writeToSshDockerExec: vi.fn(),
}));
vi.mock("@/services/desktop-bridge", () => ({
  copyText: vi.fn(),
  readText: vi.fn(),
  listenAppEvent: () => () => {},
  isDesktopRuntime: () => true,
}));

// AppContextMenu and the Element Plus items need a live popper; these stubs render the menu inline.
const ContextMenu = defineComponent({
  name: "AppContextMenuStub",
  props: { open: { type: Boolean, default: true } },
  emits: ["close"],
  setup(props, { slots }) {
    return () => (props.open ? h("ul", { class: "context-menu-stub" }, slots.default?.()) : null);
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

const MENU_ITEM: PluginMenuItem = {
  pluginId: "demo.menus",
  pluginName: "Demo Menus",
  menu: "terminal/context",
  command: "explain",
  title: "Explain output",
  whenClauses: [],
  group: "",
  order: 0,
  passContext: false,
  hidden: false,
};

function textProvider(overrides: Partial<PluginServiceEntry> = {}): PluginServiceEntry {
  return {
    pluginId: "demo.explain",
    pluginName: "Demo Explain",
    entryId: "explain",
    service: "demo.text.explain",
    title: "Explain error",
    description: "",
    input: { kind: "text", maxBytes: 64 },
    output: { kind: "text" },
    surfaces: ["terminalSelection"],
    risk: "read",
    timeoutMs: 30000,
    isDefault: false,
    ...overrides,
  };
}

const { t } = useVueI18n();
const SERVICE_MENU = t("plugins.services.menu");

let wrapper: VueWrapper | undefined;

function menuItems() {
  return wrapper!.findComponent(ContextMenu).findAll("[role=menuitem]");
}

function menuLabels() {
  return menuItems().map((item) => item.text());
}

/** Expands "Process with plugin" and returns the service item. */
async function expandService(title = "Explain error") {
  const expander = menuItems().find((item) => item.text() === SERVICE_MENU);
  expect(expander, "service entry").toBeDefined();
  await expander!.trigger("click");
  const item = menuItems().find((entry) => entry.text().startsWith(title));
  expect(item, title).toBeDefined();
  return item!;
}

beforeEach(() => {
  services.entries.value = [textProvider()];
  services.run.mockReset();
  pluginMenuItems.value = [MENU_ITEM];
  xterm.selection = "";
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  services.entries.value = [];
  pluginMenuItems.value = [];
});

describe("TerminalContextMenu 'Process with plugin'", () => {
  function mountMenu(props: Partial<{ open: boolean; selection: string; token: string; url: string; path: string; terminalKind: "local" | "ssh" | "docker" }> = {}) {
    wrapper = mount(TerminalContextMenu, {
      props: { open: true, x: 10, y: 10, selection: "", ready: true, canClose: true, canSplit: true, terminalKind: "local", ...props },
      attachTo: document.body,
      global: { stubs },
    });
  }

  it.each(["local", "ssh"] as const)("hands the normalized selection of a %s terminal to the service, after the plugin items", async (kind) => {
    mountMenu({ selection: "line 1\r\nerr\u001b[31m!\u001b[0m\u0007\r\u0000end", terminalKind: kind });
    await flushPromises();
    const labels = menuLabels();
    expect(labels.indexOf(SERVICE_MENU)).toBeGreaterThan(labels.indexOf("Explain output"));
    expect(labels.at(-1)).toBe(SERVICE_MENU);
    await (await expandService()).trigger("click");
    await flushPromises();
    expect(services.run).toHaveBeenCalledWith({
      service: "demo.text.explain",
      surface: "terminalSelection",
      input: "line 1\nerr!\nend",
      surfaceContext: { terminal: { kind } },
    });
    expect(wrapper!.emitted("close")).toBeTruthy();
  });

  it("does not appear with only the line under the pointer (no explicit selection)", async () => {
    mountMenu({ selection: "", token: "make", url: "https://example.test", path: "/var/log/app.log" });
    await flushPromises();
    expect(menuLabels()).toContain("Explain output");
    expect(menuLabels()).not.toContain(SERVICE_MENU);
    expect(wrapper!.findComponent(PluginServiceMenuItems).props("target")()).toBeNull();
  });

  it("does not appear for a selection that is only control characters", async () => {
    mountMenu({ selection: "\u001b[0m\u0007" });
    await flushPromises();
    expect(menuLabels()).not.toContain(SERVICE_MENU);
  });

  it("greys out a selection over the provider's maxBytes and says it is too large", async () => {
    mountMenu({ selection: "x".repeat(65) });
    await flushPromises();
    const item = await expandService();
    expect(item.attributes("disabled")).toBe("true");
    expect(item.text()).toContain(t("plugins.services.disabled.tooLarge"));
    await item.trigger("click");
    expect(services.run).not.toHaveBeenCalled();
  });

  it("has no target while the menu is closed", async () => {
    mountMenu({ selection: "boom" });
    await flushPromises();
    const target = wrapper!.findComponent(PluginServiceMenuItems).props("target") as () => unknown;
    expect(target()).toMatchObject({ surface: "terminalSelection", kind: "local", selection: "boom" });
    await wrapper!.setProps({ open: false });
    expect(target()).toBeNull();
  });

  it("asks to close when its KeepAlive'd view is deactivated", async () => {
    const shown = ref(true);
    const closed = vi.fn();
    const Host = defineComponent({
      setup() {
        return () =>
          h(KeepAlive, null, [
            shown.value
              ? h(TerminalContextMenu, {
                  key: "menu",
                  open: true,
                  x: 10,
                  y: 10,
                  selection: "boom",
                  ready: true,
                  canClose: true,
                  canSplit: true,
                  terminalKind: "local",
                  onClose: closed,
                })
              : h("div", { key: "other" }),
          ]);
      },
    });
    wrapper = mount(Host, { attachTo: document.body, global: { stubs } });
    await flushPromises();
    shown.value = false;
    await flushPromises();
    expect(closed).toHaveBeenCalled();
  });
});

describe("SshDockerTerminal 'Process with plugin'", () => {
  async function openMenu() {
    wrapper = mount(SshDockerTerminal, {
      props: { parentSessionId: "ssh-1", sessionId: "docker-1", containerId: "c1", mode: "exec" },
      attachTo: document.body,
      global: { stubs },
    });
    await flushPromises();
    await wrapper.find(".ssh-docker-terminal").trigger("contextmenu");
    await flushPromises();
  }

  it("hands the selection over with terminal.kind docker, after the plugin items", async () => {
    xterm.selection = "panic: \u001b[1mboom\u001b[0m\r\n";
    await openMenu();
    const labels = menuLabels();
    expect(labels.indexOf(SERVICE_MENU)).toBeGreaterThan(labels.indexOf("Explain output"));
    await (await expandService()).trigger("click");
    await flushPromises();
    expect(services.run).toHaveBeenCalledWith({
      service: "demo.text.explain",
      surface: "terminalSelection",
      input: "panic: boom\n",
      surfaceContext: { terminal: { kind: "docker" } },
    });
    expect(wrapper!.findComponent(ContextMenu).exists()).toBe(false);
  });

  it("does not appear without a selection and drops the target on close and unmount", async () => {
    await openMenu();
    expect(menuLabels()).toContain("Explain output");
    expect(menuLabels()).not.toContain(SERVICE_MENU);
    wrapper!.findComponent(ContextMenu).vm.$emit("close");
    await flushPromises();

    xterm.selection = "boom";
    await wrapper!.find(".ssh-docker-terminal").trigger("contextmenu");
    await flushPromises();
    const target = wrapper!.findComponent(PluginServiceMenuItems).props("target") as () => unknown;
    expect(target()).toMatchObject({ surface: "terminalSelection", kind: "docker", selection: "boom" });
    wrapper!.unmount();
    wrapper = undefined;
    expect(target()).toBeNull();
  });
});
