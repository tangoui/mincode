import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h, ref } from "vue";
import HttpWorkspaceView from "@/views/HttpWorkspaceView.vue";
import type { PluginServiceEntry } from "@/domain/plugins/services";
import { useVueI18n } from "@/i18n/messages";

const services = vi.hoisted(() => ({ run: vi.fn(), entries: undefined as unknown as import("vue").ShallowRef<unknown[]> }));
vi.mock("@/services/plugin-services", async (original) => {
  const actual = await original<typeof import("@/services/plugin-services")>();
  const { shallowRef } = await import("vue");
  services.entries = shallowRef<unknown[]>([]);
  return { ...actual, pluginServices: services.entries, runPluginService: (...args: unknown[]) => services.run(...args) };
});

const dialogs = vi.hoisted(() => ({ confirm: vi.fn(), success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }));
vi.mock("element-plus", async (importOriginal) => ({
  ...(await importOriginal<typeof import("element-plus")>()),
  ElMessage: { success: dialogs.success, error: dialogs.error, warning: dialogs.warning, info: dialogs.info },
  ElMessageBox: { confirm: dialogs.confirm },
}));

// The desktop runtime: http_send_request answers with the test's response.
const native = vi.hoisted(() => ({ response: null as unknown, sent: vi.fn() }));
vi.mock("@/services/desktop-bridge", async (original) => ({
  ...(await original<typeof import("@/services/desktop-bridge")>()),
  isDesktopRuntime: () => true,
  // HttpWorkspaceView gates sending on canInvoke (desktop or a browser-local handler).
  canInvoke: () => true,
  invokeBackend: async (command: string, args: unknown) => {
    if (command !== "http_send_request") return undefined;
    native.sent(args);
    return structuredClone(native.response);
  },
}));
vi.mock("@/services/http-state-persistence", () => ({
  loadOrMigrateHttpResources: async () => null,
  saveHttpResources: async () => {},
  loadHttpState: async () => null,
  saveHttpState: async () => {},
}));

// Element Plus dropdowns need a live popper; this stub opens on a trigger click and renders the menu inline.
const Dropdown = defineComponent({
  name: "ElDropdownStub",
  emits: ["visible-change", "command"],
  setup(_, { slots, emit, expose }) {
    const open = ref(false);
    // Like Element Plus: closing through the instance hides the popper and reports it.
    expose({
      handleOpen: () => (open.value = true),
      handleClose: () => {
        if (!open.value) return;
        open.value = false;
        emit("visible-change", false);
      },
    });
    return () =>
      h("div", { class: "dropdown-stub" }, [
        h("div", {
          class: "dropdown-stub__trigger",
          onClick: () => {
            open.value = !open.value;
            emit("visible-change", open.value);
          },
        }, slots.default?.()),
        open.value ? h("ul", { class: "dropdown-stub__menu" }, slots.dropdown?.()) : null,
      ]);
  },
});
const DropdownMenu = defineComponent({
  setup(_, { slots }) {
    return () => h("div", slots.default?.());
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
  ElDropdown: Dropdown,
  "el-dropdown": Dropdown,
  ElDropdownMenu: DropdownMenu,
  "el-dropdown-menu": DropdownMenu,
  ElDropdownItem: DropdownItem,
  "el-dropdown-item": DropdownItem,
  HttpBodyEditor: true,
  HttpPluginTemplatesPanel: true,
  HttpMockServerPanel: true,
};

const { t } = useVueI18n();
const BUTTON = t("http.processWithPlugin");

function entry(overrides: Partial<PluginServiceEntry> = {}): PluginServiceEntry {
  return {
    pluginId: "demo.json",
    pluginName: "Demo JSON",
    entryId: "types",
    service: "json.types",
    title: "Generate types",
    description: "",
    input: { kind: "file", accepts: [".json"], maxBytes: 1048576 },
    output: { kind: "text" },
    surfaces: ["httpResponse"],
    risk: "read",
    timeoutMs: 30000,
    isDefault: false,
    ...overrides,
  };
}

function response(overrides: Record<string, unknown> = {}) {
  return {
    status: 200,
    status_text: "OK",
    headers: [
      ["Content-Type", "application/json; charset=utf-8"],
      ["Set-Cookie", "sid=abcdef123456; HttpOnly"],
      ["X-Echo", "echo bearer-token-value-123 back"],
      ["X-Debug-Header", "saw hdr-secret-456"],
      ["X-Request-Id", "req-1"],
    ],
    body: '{"ok":true}',
    elapsed_ms: 42,
    size_bytes: 11,
    ...overrides,
  };
}

let wrapper: VueWrapper | undefined;

beforeEach(async () => {
  // Loads the mocked module (and its test-owned provider list) before any lazy menu does.
  await import("@/services/plugin-services");
  setActivePinia(createPinia());
  services.entries.value = [entry()];
  services.run.mockReset();
  native.response = response();
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  services.entries.value = [];
  vi.clearAllMocks();
});

async function mountView() {
  wrapper = mount(HttpWorkspaceView, { attachTo: document.body, global: { stubs } });
  await flushPromises();
}

async function sendRequest() {
  await fillRequest();
  await wrapper!.find(".http-url-input input").trigger("keyup", { key: "Enter" });
  await flushPromises();
  expect(native.sent).toHaveBeenCalled();
}

/** The active tab's address, bearer token and a sensitive request header, as a user would enter them. */
async function fillRequest() {
  await wrapper!.find(".http-url-input input").setValue("https://api.example.com/users?page=1");
  const tab = (wrapper!.vm as unknown as { activeTab: Record<string, unknown> }).activeTab;
  Object.assign(tab, {
    authType: "bearer",
    authToken: "bearer-token-value-123",
    headers: [{ id: "h1", key: "X-Api-Key", value: "hdr-secret-456", enabled: true }],
  });
  await flushPromises();
}

function pluginButton() {
  return wrapper!.find(`[aria-label="${BUTTON}"]`);
}

async function openServices() {
  await pluginButton().trigger("click");
  await flushPromises();
  return wrapper!.findAll(".dropdown-stub__menu [role=menuitem]");
}

describe("HttpWorkspaceView response plugin services (httpResponse)", () => {
  it("shows no button before a response arrives", async () => {
    await mountView();
    expect(pluginButton().exists()).toBe(false);
  });

  it("hands over the response body with its type and filtered headers", async () => {
    await mountView();
    await sendRequest();
    expect(pluginButton().exists()).toBe(true);
    const items = await openServices();
    expect(items.map((item) => item.text())).toEqual(["Generate types"]);
    await items[0]!.trigger("click");
    expect(services.run).toHaveBeenCalledTimes(1);
    const request = services.run.mock.calls[0]![0] as {
      service: string;
      surface: string;
      content: { text: string; contentType: string };
      surfaceContext: { response: { headers: [string, string][]; headersRedacted: number; status: number; elapsedMs: number }; request?: { url: string } };
    };
    expect(request.service).toBe("json.types");
    expect(request.surface).toBe("httpResponse");
    expect(request.content).toEqual({ text: '{"ok":true}', contentType: "application/json; charset=utf-8" });
    const { headers, headersRedacted, status, elapsedMs } = request.surfaceContext.response;
    expect({ status, elapsedMs }).toEqual({ status: 200, elapsedMs: 42 });
    expect(headers).toEqual([
      ["Content-Type", "application/json; charset=utf-8"],
      ["X-Request-Id", "req-1"],
    ]);
    expect(headersRedacted).toBe(3);
    const context = JSON.stringify(request.surfaceContext);
    for (const leaked of ["Set-Cookie", "sid=", "bearer-token-value-123", "hdr-secret-456", "page=1"]) expect(context).not.toContain(leaked);
    expect(request.surfaceContext.request).toEqual({ method: "GET", url: "https://api.example.com/users" });
  });

  it("renders no button when no service matches the response", async () => {
    services.entries.value = [entry({ input: { kind: "file", accepts: [".xml"], maxBytes: 1048576 } })];
    await mountView();
    await sendRequest();
    expect(pluginButton().exists()).toBe(false);
  });

  it("offers nothing for a binary response", async () => {
    services.entries.value = [entry({ input: { kind: "file", accepts: ["*/*", ".json", ".txt"], maxBytes: 1048576 } })];
    native.response = response({ headers: [["Content-Type", "image/png"]], body: "\u0089PNG" });
    await mountView();
    await sendRequest();
    expect(pluginButton().exists()).toBe(false);
  });

  it("greys out the services for a response the host cut at 10 MiB", async () => {
    native.response = response({ size_bytes: 10485761 });
    await mountView();
    await sendRequest();
    const [item] = await openServices();
    const reason = t("plugins.services.disabled.responseTruncated");
    expect(item!.attributes("disabled")).toBeDefined();
    expect(item!.attributes("title")).toBe(reason);
    await item!.trigger("click");
    expect(services.run).not.toHaveBeenCalled();
  });

  it("closes the open service menu when another request tab becomes active", async () => {
    await mountView();
    await sendRequest();
    expect(await openServices()).toHaveLength(1);
    (wrapper!.vm as unknown as { addRequest: () => void }).addRequest();
    await flushPromises();
    expect(wrapper!.find(".dropdown-stub__menu").exists()).toBe(false);
  });

  it("names no request and uses no tab credentials for a response without a send record", async () => {
    await mountView();
    await fillRequest();
    const tab = (wrapper!.vm as unknown as { activeTab: Record<string, unknown> }).activeTab;
    tab.response = response();
    await flushPromises();
    const [item] = await openServices();
    await item!.trigger("click");
    const request = services.run.mock.calls[0]![0] as { surfaceContext: { response: { headers: [string, string][]; headersRedacted: number }; request?: unknown } };
    expect(request.surfaceContext).not.toHaveProperty("request");
    // Name rules still apply (Set-Cookie); headers echoing credentials cannot be matched without the record.
    expect(request.surfaceContext.response.headers.map(([name]) => name)).not.toContain("Set-Cookie");
    expect(request.surfaceContext.response.headersRedacted).toBe(1);
  });

  it("withholds a response header echoing a sensitive query parameter of the request", async () => {
    native.response = response({ headers: [["Content-Type", "application/json"], ["X-Echo-Query", "got qtok-secret-789"], ["X-Request-Id", "req-1"]] });
    await mountView();
    await wrapper!.find(".http-url-input input").setValue("https://api.example.com/users?access_token=qtok-secret-789&page=1");
    await flushPromises();
    await wrapper!.find(".http-url-input input").trigger("keyup", { key: "Enter" });
    await flushPromises();
    const [item] = await openServices();
    await item!.trigger("click");
    const request = services.run.mock.calls[0]![0] as { surfaceContext: { response: { headers: [string, string][]; headersRedacted: number } } };
    expect(request.surfaceContext.response.headers.map(([name]) => name)).toEqual(["Content-Type", "X-Request-Id"]);
    expect(request.surfaceContext.response.headersRedacted).toBe(1);
    expect(JSON.stringify(request.surfaceContext)).not.toContain("qtok-secret-789");
  });

  it("withholds headers echoing a sensitive query value either percent-encoded or decoded", async () => {
    native.response = response({ headers: [["Content-Type", "application/json"], ["X-Raw", "saw qtok%2Fsecret-789"], ["X-Decoded", "saw qtok/secret-789"], ["X-Request-Id", "req-1"]] });
    await mountView();
    await wrapper!.find(".http-url-input input").setValue("https://api.example.com/users?api_key=qtok%2Fsecret-789");
    await flushPromises();
    await wrapper!.find(".http-url-input input").trigger("keyup", { key: "Enter" });
    await flushPromises();
    const [item] = await openServices();
    await item!.trigger("click");
    const request = services.run.mock.calls[0]![0] as { surfaceContext: { response: { headers: [string, string][]; headersRedacted: number } } };
    expect(request.surfaceContext.response.headers.map(([name]) => name)).toEqual(["Content-Type", "X-Request-Id"]);
    expect(request.surfaceContext.response.headersRedacted).toBe(2);
  });

  it("refuses a body whose UTF-8 size is over the provider's limit, whatever size_bytes says", async () => {
    services.entries.value = [entry({ input: { kind: "file", accepts: [".json"], maxBytes: 16 } })];
    // 8 characters, 24 bytes of UTF-8; the reported size fits the provider.
    native.response = response({ body: '"汉汉汉汉汉汉"', size_bytes: 8 });
    await mountView();
    await sendRequest();
    const [item] = await openServices();
    expect(item!.attributes("title")).not.toBe(t("plugins.services.disabled.tooLargeFile"));
    await item!.trigger("click");
    expect(services.run).not.toHaveBeenCalled();
    expect(dialogs.warning).toHaveBeenCalledWith(expect.objectContaining({ message: t("plugins.services.responseTooLarge") }));
  });
});
