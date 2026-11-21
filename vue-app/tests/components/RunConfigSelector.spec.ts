import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import type { RunConfiguration } from "@/domain/debug/types";

const runtime = vi.hoisted(() => ({ desktop: true }));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => runtime.desktop,
  canInvoke: () => runtime.desktop,
  invokeBackend: vi.fn(),
  listenAppEvent: vi.fn(() => () => undefined),
}));
const listDebuggers = vi.hoisted(() => vi.fn());
vi.mock("@/services/debug-service", () => ({
  listDebuggers,
  debugRequest: vi.fn(),
  setSessionBreakpoints: vi.fn(),
  stopDebugSession: vi.fn().mockResolvedValue(undefined),
  onDebugEvent: () => () => undefined,
}));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));

import RunConfigSelector from "@/components/debug/RunConfigSelector.vue";
import { newSessionView } from "@/domain/debug/session-model";
import { useDebugConfigsStore } from "@/stores/debug-configs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const ROOT = "/Users/A/Proj";
const FILE = "/Users/A/Proj/src/app.py";
const cfg: RunConfiguration = {
  id: "cfg-1",
  name: "App",
  pluginId: "mincode.python-debug",
  type: "python",
  request: "launch",
  fields: { program: "${file}" },
};

let wrapper: ReturnType<typeof mount> | undefined;

beforeEach(() => {
  runtime.desktop = true;
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

async function mountSelector(activeLanguageId?: () => string | undefined) {
  wrapper = mount(RunConfigSelector, { props: { projectRoot: ROOT, activeFile: FILE, activeLanguageId } });
  await flushPromises();
  return wrapper;
}

describe("RunConfigSelector", () => {
  it("debug button starts the selected configuration", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.upsertConfig(ROOT, cfg);
    configs.selectConfig(ROOT, cfg.id);
    const sessions = useDebugSessionsStore();
    const start = vi.spyOn(sessions, "start").mockResolvedValue("s1");
    await mountSelector();
    expect(wrapper!.text()).toContain("App");

    await wrapper!.find('[data-action="debug"]').trigger("click");
    await flushPromises();
    expect(start).toHaveBeenCalledWith(ROOT, expect.objectContaining({ id: "cfg-1" }), { noDebug: false, activeFile: FILE });
    await wrapper!.find('[data-action="run"]').trigger("click");
    await flushPromises();
    expect(start).toHaveBeenLastCalledWith(ROOT, expect.objectContaining({ id: "cfg-1" }), { noDebug: true, activeFile: FILE });
  });

  it("shows a launch started elsewhere (a keybinding) as in flight and ignores clicks", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.upsertConfig(ROOT, cfg);
    configs.selectConfig(ROOT, cfg.id);
    const sessions = useDebugSessionsStore();
    const launch = { configId: cfg.id, noDebug: false };
    const launchFor = vi.spyOn(sessions, "launchFor").mockImplementation(() => launch);
    const start = vi.spyOn(sessions, "start").mockResolvedValue("s1");
    await mountSelector();
    expect(wrapper!.find('[data-action="debug"]').classes()).toContain("is-loading");
    expect(wrapper!.find('[data-action="run"]').attributes("disabled")).toBeDefined();
    await wrapper!.find('[data-action="debug"]').trigger("click");
    await flushPromises();
    expect(start).not.toHaveBeenCalled();
    launchFor.mockRestore();
  });

  it("opens new configuration when none exist", async () => {
    const sessions = useDebugSessionsStore();
    const start = vi.spyOn(sessions, "start");
    await mountSelector();
    await wrapper!.find('[data-action="debug"]').trigger("click");
    await flushPromises();
    await wrapper!.find('[data-action="run"]').trigger("click");
    await flushPromises();
    expect(wrapper!.emitted("new-configuration")).toHaveLength(2);
    expect(start).not.toHaveBeenCalled();
  });

  it("a language without a debugger opens the new configuration flow", async () => {
    listDebuggers.mockResolvedValue([]);
    const sessions = useDebugSessionsStore();
    const start = vi.spyOn(sessions, "start");
    await mountSelector(() => "cobol");
    await wrapper!.find('[data-action="debug"]').trigger("click");
    await flushPromises();
    expect(wrapper!.emitted("new-configuration")).toHaveLength(1);
    expect(start).not.toHaveBeenCalled();
  });

  it("debug on a language file without configurations creates and starts a current-file configuration", async () => {
    listDebuggers.mockResolvedValue([
      {
        pluginId: "python-debug",
        pluginName: "Python Debug",
        type: "python",
        title: "Python",
        languages: ["python"],
        configurationSnippets: [{ label: "Current file", request: "launch", body: { program: "${file}" } }],
        hasEntryPoints: true,
        hasCheck: true,
      },
    ]);
    const sessions = useDebugSessionsStore();
    const start = vi.spyOn(sessions, "start").mockResolvedValue("s1");
    await mountSelector(() => "python");
    await wrapper!.find('[data-action="debug"]').trigger("click");
    await flushPromises();
    expect(wrapper!.emitted("new-configuration")).toBeUndefined();
    const created = useDebugConfigsStore().stateFor(ROOT).configs[0];
    expect(created).toMatchObject({ pluginId: "python-debug", type: "python", request: "launch", fields: { program: "${file}" } });
    expect(start).toHaveBeenCalledWith(ROOT, created, { noDebug: false, activeFile: FILE });
  });

  it("stop stops the selected session of the project", async () => {
    const sessions = useDebugSessionsStore();
    const stop = vi.spyOn(sessions, "stop").mockResolvedValue(undefined);
    await mountSelector();
    expect(wrapper!.find('[data-action="stop"]').attributes("disabled")).toBeDefined();
    sessions.sessions.push(
      newSessionView({ id: "s1", projectRoot: ROOT, configId: "cfg-1", configName: "App", type: "python", noDebug: false, capabilities: {}, startedAt: 1 })
    );
    sessions.selectSession("s1");
    await flushPromises();
    await wrapper!.find('[data-action="stop"]').trigger("click");
    expect(stop).toHaveBeenCalledWith("s1");
  });

  it("shows the desktop requirement in the browser preview", async () => {
    runtime.desktop = false;
    await mountSelector();
    expect(wrapper!.find('[data-action="run"]').exists()).toBe(false);
    expect(wrapper!.find('[data-action="debug"]').exists()).toBe(false);
    expect(wrapper!.find(".run-config-selector__desktop").exists()).toBe(true);
  });
});
