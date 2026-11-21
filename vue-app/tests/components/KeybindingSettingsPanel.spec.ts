// The shortcuts table lists every command with four row actions. Rendering them as plain
// buttons (not one ElButton component each, hundreds of them) keeps opening Settings >
// Shortcuts from freezing; the actions keep their labels, test ids and behaviour.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { ElButton, ElTable, ElTag } from "element-plus";
import KeybindingSettingsPanel from "@/components/settings/KeybindingSettingsPanel.vue";
import { useKeybindingsStore } from "@/stores/keybindings";

vi.mock("vue-router", () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock("@/services/desktop-bridge", async (original) => ({
  ...(await original<typeof import("@/services/desktop-bridge")>()),
  isDesktopRuntime: () => false,
}));

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

async function mountPanel() {
  setActivePinia(createPinia());
  wrapper = mount(KeybindingSettingsPanel, {
    props: { globalShortcutEnabled: false },
    global: { stubs: { PluginGlobalShortcutsSection: true, KeybindingRecorderDialog: true } },
    attachTo: document.body,
  });
  await flushPromises();
  return wrapper;
}

describe("KeybindingSettingsPanel row actions", () => {
  it("renders the row actions without an ElButton per row", async () => {
    const view = await mountPanel();
    const rows = view.findAll(".app-keybindings__command");
    expect(rows.length).toBeGreaterThan(20);
    expect(view.findAll("[data-testid=keybinding-change]")).toHaveLength(rows.length);
    expect(view.findAll("[data-testid=keybinding-reset]")).toHaveLength(rows.length);
    const inTable = (component: { element: Element }) => Boolean(component.element.closest?.(".app-keybindings__table"));
    expect(view.findAllComponents(ElButton).filter(inTable)).toHaveLength(0);
    expect(view.findAllComponents(ElTag).filter(inTable)).toHaveLength(0);
    expect(view.findAllComponents(ElTable)).toHaveLength(0);
    // e2e helpers find a command's row as [data-testid=keybinding-table] [data-command] → closest tr.
    expect(view.get('[data-testid=keybinding-table] [data-command="builtin:ai"]').element.closest("tr")).toBeTruthy();
  });

  it("removes a command's bindings from its row's action", async () => {
    const view = await mountPanel();
    const store = useKeybindingsStore();
    const commit = vi.spyOn(store, "setOverrides");
    const row = view.findAll(".app-keybindings__table tbody tr").find((item) => item.find(".app-keybindings__command").attributes("data-command") === "builtin:ai")!;
    const remove = row.findAll(".app-keybindings__actions button")[2];
    expect(remove.attributes("aria-label")).toBeTruthy();
    await remove.trigger("click");
    expect(commit).toHaveBeenCalled();
    const next = commit.mock.calls.at(-1)![0] as { command: string }[];
    expect(next.some((override) => override.command === "builtin:ai")).toBe(true);
  });
});
