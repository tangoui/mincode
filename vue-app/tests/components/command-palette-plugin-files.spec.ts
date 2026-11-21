import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { defineComponent, h, nextTick } from "vue";
import { mount, type VueWrapper } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import { useCommandPalette } from "@/composables/use-command-palette";
import { usePluginSearchProvidersStore } from "@/stores/plugin-search-providers";
import { useCommandPaletteStore } from "@/stores/command-palette";
import { messagesZh } from "@/i18n/locales/zh";
import { messagesEn } from "@/i18n/locales/en";

const fixtures = vi.hoisted(() => ({
  scope: { workspaceId: "a", root: "/a", revision: "1", ready: true },
  listeners: new Set<() => void>(),
  validationError: "",
  searches: 0,
  warnings: [] as string[],
  errors: [] as string[],
}));
// Mock only the native boundary and unrelated palette resources; the actual
// composable, provider/usage stores, search parser and click service run here.
vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  nativeErrorMessage: (e: unknown) => e instanceof Error ? e.message : String(e),
  invokeBackend: async (command: string) => {
    if (command === "plugin_host_search") {
      fixtures.searches++;
      const { workspaceId, root, revision } = fixtures.scope;
      return { items: [
        { id: "file", title: "Main", target: { type: "file", path: "src/main.go", line: 2 } },
        { id: "view", title: "View", target: { type: "view", view: "main" } },
      ], dropped: 0, truncated: false, fileScope: { workspaceId, root, revision }, providerRuntime: { instance: "1", generation: "1" } };
    }
    if (command === "plugin_host_validate_search_file_target") throw new Error(fixtures.validationError);
    throw new Error(`unexpected native command ${command}`);
  },
}));
vi.mock("element-plus", () => ({
  ElMessage: { warning: (o: { message: string }) => fixtures.warnings.push(o.message), error: (o: string | { message: string }) => fixtures.errors.push(typeof o === "string" ? o : o.message) },
  ElMessageBox: {},
}));
vi.mock("@/services/plugin-workspace-scope", () => ({
  currentPluginWorkspaceScope: () => fixtures.scope,
  onPluginWorkspaceScopeChange: (listener: () => void) => { fixtures.listeners.add(listener); return () => fixtures.listeners.delete(listener); },
}));
vi.mock("@/stores/workbench", () => ({ useWorkbenchStore: () => ({ workspaces: [], tabs: [] }) }));
vi.mock("@/composables/use-workspace-state-scope", () => ({ useWorkspaceStateScope: () => ({ hydrate: async () => undefined, capture: () => ({ scopeId: "a", workspaceId: "a" }) }) }));
vi.mock("@/stores/local-project", () => ({ useLocalProjectStore: () => ({ projects: [], projectRoot: "", documents: [], hydrated: true, switchWorkspace: () => undefined }) }));
vi.mock("@/stores/ssh-connections", () => ({ useSshConnectionsStore: () => ({ sortedProfiles: [], hydrate: async () => undefined }) }));
vi.mock("@/stores/database-workspace", () => ({ useDatabaseWorkspaceStore: () => ({ connections: [], initialize: async () => undefined }) }));
vi.mock("@/stores/ai-assistant-shell", () => ({ useAiAssistantShellStore: () => ({}) }));
vi.mock("@/stores/keybindings", () => ({ useKeybindingsStore: () => ({ label: () => "", paletteShortcut: "mod-k" }) }));
vi.mock("@/services/keybinding-dispatcher", () => ({ currentKeybindingContext: () => ({}) }));
vi.mock("@/services/command-registry", async () => { const { ref } = await import("vue"); return { useRegisteredCommands: () => ref([]), executeRegisteredCommand: async () => undefined }; });
vi.mock("@/services/plugin-services", async () => { const { ref } = await import("vue"); return { pluginServices: ref([]), runPluginService: async () => undefined }; });
vi.mock("@/services/http-state-persistence", () => ({ loadHttpState: async () => ({ collections: [] }) }));
vi.mock("@/services/plugin-commands", () => ({ refreshPluginCommands: async () => undefined, executePluginCommandWithArgs: async () => undefined }));

let wrapper: VueWrapper | undefined;
let palette: ReturnType<typeof useCommandPalette>;
const provider = { pluginId: "acme.search", pluginName: "Search", id: "code", prefix: "find", title: "Code", minQueryLength: 1 };

beforeEach(() => {
  fixtures.scope = { workspaceId: "a", root: "/a", revision: "1", ready: true };
  fixtures.validationError = "unavailable: search provider was removed; search again";
  fixtures.searches = 0;
  fixtures.warnings = [];
  fixtures.errors = [];
  const router = createRouter({ history: createMemoryHistory(), routes: [] });
  wrapper = mount(defineComponent({ setup() {
    palette = useCommandPalette({ router, openSettings: () => undefined });
    return () => h("div", palette.results.value.map(item => h("span", { key: item.id }, item.title)));
  } }));
  usePluginSearchProvidersStore().setContributions([provider], []);
});
afterEach(() => { wrapper?.unmount(); expect(fixtures.listeners.size).toBe(0); });

async function search() {
  palette.openPalette({ prefix: "find:main" });
  await vi.waitFor(() => expect(palette.results.value).toHaveLength(2));
  expect(wrapper?.text()).toContain("Main");
}

it("actual palette keeps failed activation open, clears expired plugin file rows and writes no history", async () => {
  await search();
  const file = palette.results.value.find(row => row.action.type === "plugin-open-file");
  expect(file).toBeDefined();
  if (!file) throw new Error("missing file row");
  await palette.execute(file);
  expect(palette.open.value).toBe(true);
  expect(palette.executingId.value).toBe("");
  expect(palette.results.value.map(row => row.title)).toEqual(["View"]);
  expect(palette.pluginSearch.value?.staleFiles).toBe(1);
  expect(fixtures.warnings).toHaveLength(1);
  expect(fixtures.errors).toEqual([]);
  expect(useCommandPaletteStore().usage).toEqual({});
  for (const messages of [messagesZh, messagesEn]) {
    expect(messages["pluginContrib.searchProviders.fileStaleCleared"]).toMatch(/项目或插件|project or plugin/);
  }
});

it("actual palette retains genuine missing-file and permission failures without classifying them stale", async () => {
  await search();
  for (const failure of ["not_found: file does not exist", "forbidden: file could not be inspected"]) {
    fixtures.validationError = failure;
    const file = palette.results.value.find(row => row.action.type === "plugin-open-file");
    if (!file) throw new Error("missing file row");
    await palette.execute(file);
    expect(palette.open.value).toBe(true);
    expect(palette.results.value).toHaveLength(2);
    expect(palette.pluginSearch.value?.staleFiles).toBeUndefined();
  }
  expect(fixtures.errors).toHaveLength(2);
  expect(fixtures.warnings).toEqual([]);
  expect(useCommandPaletteStore().usage).toEqual({});
});

it("mounted scope subscription drops old files and provider refresh re-queries under the new scope", async () => {
  await search();
  fixtures.scope = { workspaceId: "b", root: "/b", revision: "2", ready: true };
  for (const listener of fixtures.listeners) listener();
  await nextTick();
  expect(palette.results.value.map(row => row.title)).toEqual(["View"]);
  expect(palette.pluginSearch.value?.staleFiles).toBe(1);
  usePluginSearchProvidersStore().setContributions([{ ...provider, pluginName: "Updated" }], []);
  await vi.waitFor(() => expect(fixtures.searches).toBe(2));
  await vi.waitFor(() => expect(palette.results.value).toHaveLength(2));
  const file = palette.results.value.find(row => row.action.type === "plugin-open-file");
  expect(file?.action).toMatchObject({ fileScope: { workspaceId: "b", root: "/b", revision: "2" } });
  usePluginSearchProvidersStore().clearContributions();
  await nextTick();
  expect(palette.results.value.some(row => row.action.type === "plugin-open-file")).toBe(false);
});
