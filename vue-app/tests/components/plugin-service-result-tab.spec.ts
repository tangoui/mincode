import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h, nextTick, reactive } from "vue";

const bridge = vi.hoisted(() => ({
  invoke: vi.fn(),
  handlers: new Map<string, Array<(payload: unknown) => void>>(),
  copy: vi.fn(),
  saveText: vi.fn(),
  push: vi.fn(),
  persisted: new Map<string, unknown>(),
}));

vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: async () => undefined,
  savePersistedValue: async (key: string, value: unknown) => {
    bridge.persisted.set(key, JSON.parse(JSON.stringify(value)));
  },
}));

// The route the result view reads; tests point it at a result id.
const route = reactive<{ name: string; params: Record<string, string> }>({ name: "service-result", params: {} });

vi.mock("vue-router", () => ({
  useRouter: () => ({ push: bridge.push, replace: bridge.push }),
  useRoute: () => route,
}));

vi.mock("@/services/file-download", () => ({
  saveTextFileToDisk: (...args: unknown[]) => bridge.saveText(...args),
}));

vi.mock("@/services/desktop-bridge", () => ({
  invokeBackend: (...args: unknown[]) => bridge.invoke(...args),
  isDesktopRuntime: () => true,
  listenAppEvent: (event: string, handler: (payload: unknown) => void) => {
    const list = bridge.handlers.get(event) ?? [];
    list.push(handler);
    bridge.handlers.set(event, list);
    return () => bridge.handlers.set(event, (bridge.handlers.get(event) ?? []).filter((item) => item !== handler));
  },
  nativeErrorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
  chooseNativeSaveFile: vi.fn(),
  copyText: (...args: unknown[]) => bridge.copy(...args),
}));

vi.mock("element-plus", async (original) => {
  const actual = await original<typeof import("element-plus")>();
  const message = Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() });
  return { ...actual, ElMessage: message };
});

// Monaco stays out of the test: the viewer stub renders what it was given.
vi.mock("@/components/ui/ReadonlyCodeViewer.vue", () => ({
  __esModule: true,
  default: defineComponent({
    name: "ReadonlyCodeViewer",
    props: { value: String, language: String, ariaLabel: String, editorKind: String },
    setup: (props) => () => h("pre", { class: "viewer-stub", "data-language": props.language }, props.value),
  }),
}));

const { default: PluginServiceResultDialog } = await import("@/components/plugins/PluginServiceResultDialog.vue");
const { default: PluginServiceResultWorkspaceView } = await import("@/views/PluginServiceResultWorkspaceView.vue");
const { pluginServiceResult, runPluginService, startPluginServiceSync } = await import("@/services/plugin-services");
const { installPluginServiceResultTabCleanup, pluginServiceResultDocument } = await import(
  "@/services/plugin-service-result-tabs"
);
const { useWorkbenchStore } = await import("@/stores/workbench");

function emit(event: string, payload: unknown) {
  for (const handler of bridge.handlers.get(event) ?? []) handler(payload);
}

const provider = { pluginId: "demo.provider", entryId: "reverse" };
const service = {
  pluginId: "demo.provider",
  pluginName: "Demo Provider",
  entryId: "reverse",
  service: "demo.text.reverse",
  title: "Reverse text",
  description: "",
  input: { kind: "text", maxBytes: 8192 },
  output: { kind: "text" },
  surfaces: ["palette"],
  risk: "read",
  timeoutMs: 30000,
  isDefault: false,
};

const statsService = {
  ...service,
  entryId: "stats",
  service: "demo.text.stats",
  title: "Text stats",
  output: { kind: "json" },
};

let stops: Array<() => void> = [];

beforeEach(async () => {
  bridge.handlers.clear();
  bridge.invoke.mockReset();
  bridge.copy.mockReset();
  bridge.saveText.mockReset();
  bridge.push.mockReset();
  route.params = {};
  bridge.invoke.mockImplementation(async (command: string) => {
    if (command === "plugin_services_list") return { services: [service, statsService] };
    if (command === "plugin_services_start") return { callId: "sc_1", state: "running", provider };
    return {};
  });
  stops = [startPluginServiceSync(), installPluginServiceResultTabCleanup(useWorkbenchStore())];
  await flushPromises();
});

afterEach(() => {
  for (const stop of stops) stop();
  stops = [];
});

async function showResult(output: unknown, entry: typeof service = service) {
  const callId = "sc_1";
  const callProvider = { pluginId: entry.pluginId, entryId: entry.entryId };
  bridge.invoke.mockImplementation(async (command: string) => {
    if (command === "plugin_services_list") return { services: [service, statsService] };
    if (command === "plugin_services_start") return { callId, state: "running", provider: callProvider };
    return {};
  });
  await runPluginService({ service: entry.service, surface: "palette", input: "abc" });
  emit("plugin:service-call-changed", { callId, state: "succeeded", provider: callProvider, output });
  await vi.waitFor(() => expect(pluginServiceResult.value?.callId).toBe(callId));
}

async function openInNewTab() {
  const dialog = mount(PluginServiceResultDialog, { attachTo: document.body });
  await nextTick();
  const button = [...document.body.querySelectorAll("button")].find((item) =>
    item.textContent?.includes("新标签打开"),
  );
  expect(button).toBeTruthy();
  button!.click();
  await flushPromises();
  return dialog;
}

describe("plugin service result tab", () => {
  it("opens a text result in a read-only workbench tab and closes the dialog", async () => {
    await showResult("cba");
    const dialog = await openInNewTab();
    const workbench = useWorkbenchStore();

    const tab = workbench.activeTab;
    expect(tab?.view).toBe("service-result");
    expect(tab?.title).toMatch(/^Reverse text \d{1,2}:\d{2}/);
    const resultId = tab!.serviceResult!.resultId;
    expect(bridge.push).toHaveBeenCalledWith({ name: "service-result", params: { resultId } });
    expect(pluginServiceResultDocument(resultId)).toMatchObject({ text: "cba", language: "plaintext", kind: "text" });
    expect(pluginServiceResult.value).toBeNull();

    route.params = { resultId };
    const view = mount(PluginServiceResultWorkspaceView);
    await vi.waitFor(() => expect(view.find(".viewer-stub").exists()).toBe(true));
    expect(view.find(".viewer-stub").text()).toBe("cba");
    expect(view.find(".viewer-stub").attributes("data-language")).toBe("plaintext");
    expect(view.text()).toContain("Reverse text");
    view.unmount();
    dialog.unmount();
  });

  it("pretty-prints a JSON result and shows it with the JSON language", async () => {
    await showResult({ words: 1, lines: [1, 2] }, statsService);
    const dialog = await openInNewTab();
    const resultId = useWorkbenchStore().activeTab!.serviceResult!.resultId;
    const text = JSON.stringify({ words: 1, lines: [1, 2] }, null, 2);
    expect(pluginServiceResultDocument(resultId)).toMatchObject({ kind: "json", language: "json", text });

    route.params = { resultId };
    const view = mount(PluginServiceResultWorkspaceView);
    await vi.waitFor(() => expect(view.find(".viewer-stub").exists()).toBe(true));
    expect(view.find(".viewer-stub").attributes("data-language")).toBe("json");
    expect(view.find(".viewer-stub").text()).toBe(text);
    view.unmount();
    dialog.unmount();
  });

  it("copies and saves the tab's content", async () => {
    await showResult({ words: 1 }, statsService);
    const dialog = await openInNewTab();
    const resultId = useWorkbenchStore().activeTab!.serviceResult!.resultId;
    route.params = { resultId };
    const view = mount(PluginServiceResultWorkspaceView);
    await vi.waitFor(() => expect(view.find(".viewer-stub").exists()).toBe(true));

    const buttons = view.findAll("button");
    await buttons.find((button) => button.text().includes("复制"))!.trigger("click");
    await flushPromises();
    expect(bridge.copy).toHaveBeenCalledWith(JSON.stringify({ words: 1 }, null, 2));

    bridge.saveText.mockResolvedValue(true);
    await buttons.find((button) => button.text().includes("另存为文件"))!.trigger("click");
    await flushPromises();
    expect(bridge.saveText).toHaveBeenCalledWith(
      expect.objectContaining({ filename: "Text stats.json", content: JSON.stringify({ words: 1 }, null, 2) }),
    );
    view.unmount();
    dialog.unmount();
  });

  it("frees the content when the tab closes", async () => {
    await showResult("cba");
    const dialog = await openInNewTab();
    const workbench = useWorkbenchStore();
    const tab = workbench.activeTab!;
    const resultId = tab.serviceResult!.resultId;
    expect(pluginServiceResultDocument(resultId)).toBeTruthy();

    workbench.closeTab(tab.id);
    await nextTick();
    expect(pluginServiceResultDocument(resultId)).toBeUndefined();

    route.params = { resultId };
    const view = mount(PluginServiceResultWorkspaceView);
    await flushPromises();
    expect(view.find(".viewer-stub").exists()).toBe(false);
    expect(view.text()).toContain("结果已不可用");
    view.unmount();
    dialog.unmount();
  });

  it("keeps result tabs out of the saved session and of a deleted workspace's archive", async () => {
    const workbench = useWorkbenchStore();
    await workbench.hydrate();
    const other = workbench.createWorkspace("Other")!;
    await showResult("cba");
    const dialog = await openInNewTab();
    const tab = workbench.activeTab!;
    expect(tab.workspaceId).toBe(other.id);
    const resultId = tab.serviceResult!.resultId;

    await workbench.flushPersistence();
    const session = bridge.persisted.get("app_workbench_session_v1") as { tabs: unknown[]; activeTabId: string };
    expect(session.tabs).toEqual([]);
    expect(session.activeTabId).toBe("");

    workbench.deleteWorkspace(other.id);
    await nextTick();
    expect(workbench.deletedWorkspaces[0]?.tabs).toEqual([]);
    expect(pluginServiceResultDocument(resultId)).toBeUndefined();
    dialog.unmount();
  });
});

