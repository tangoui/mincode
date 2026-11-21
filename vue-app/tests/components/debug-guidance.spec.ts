import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, type Component, type VNode } from "vue";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import type { DebuggerInfo, PreparedDebugLaunch, RunConfiguration } from "@/domain/debug/types";

const mocks = vi.hoisted(() => ({
  listDebuggers: vi.fn(),
  checkDebugger: vi.fn(),
  debuggerEntryPoints: vi.fn(),
  prepareDebugger: vi.fn(),
  trustDebugger: vi.fn(),
  startDebugger: vi.fn(),
  push: vi.fn(),
  confirm: vi.fn(),
  message: { info: vi.fn(), warning: vi.fn(), error: vi.fn(), success: vi.fn() },
}));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  canInvoke: () => true,
  invokeBackend: vi.fn(),
  listenAppEvent: vi.fn(() => () => undefined),
}));
vi.mock("@/services/debug-service", () => ({
  listDebuggers: mocks.listDebuggers,
  checkDebugger: mocks.checkDebugger,
  debuggerEntryPoints: mocks.debuggerEntryPoints,
  prepareDebugger: mocks.prepareDebugger,
  trustDebugger: mocks.trustDebugger,
  startDebugger: mocks.startDebugger,
  debugRequest: vi.fn().mockResolvedValue({}),
  setSessionBreakpoints: vi.fn(),
  stopDebugSession: vi.fn(),
  onDebugEvent: () => () => undefined,
}));
vi.mock("@/router", () => ({ default: { push: mocks.push } }));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));
vi.mock("element-plus", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  ElMessage: mocks.message,
  ElMessageBox: { confirm: mocks.confirm },
}));

import {
  OFFICIAL_DEBUGGER_PLUGINS,
  checkBeforeLaunch,
  ensureDebuggerFor,
  launchConfigurationFor,
  setDebugTerminalOpener,
} from "@/services/debug-guidance";
import { launchDebugSession } from "@/services/debug-launch";
import { useProjectEditorDebug } from "@/composables/use-project-editor-debug";
import { useDebugConfigsStore } from "@/stores/debug-configs";

const ROOT = "/Users/A/Proj";
const FILE = "/Users/A/Proj/app.py";
const INSTALL = "/usr/bin/python3 -m pip install debugpy";

function pythonDebugger(change: Partial<DebuggerInfo> = {}): DebuggerInfo {
  return {
    pluginId: "python-debug",
    pluginName: "Python Debug",
    type: "python",
    title: "Python",
    languages: ["python"],
    configurationSnippets: [
      { label: "Attach", request: "attach", body: { connect: { port: 5678 } } },
      { label: "Current file", request: "launch", body: { program: "${file}" } },
      { label: "Module", request: "launch", body: { module: "app" } },
    ],
    hasEntryPoints: true,
    hasCheck: true,
    ...change,
  };
}

const cfg: RunConfiguration = {
  id: "cfg-1",
  name: "App",
  pluginId: "python-debug",
  type: "python",
  request: "launch",
  fields: { program: "${file}" },
};

function prepared(): PreparedDebugLaunch {
  return {
    prepareId: "prep-1",
    type: "python",
    kind: "",
    projectRoot: ROOT,
    request: "launch",
    arguments: { program: FILE },
    launch: {
      pluginId: "python-debug",
      pluginName: "Python Debug",
      pluginVersion: "1.0.0",
      point: "debuggers",
      entryKey: "python",
      title: "debugpy",
      argv: ["python3", "-m", "debugpy.adapter"],
      env: {},
      cwd: ROOT,
      pluginExecutable: false,
      fingerprint: "a".repeat(64),
      trusted: true,
      previouslyTrusted: false,
    },
  };
}

const flush = async () => {
  for (let i = 0; i < 5; i += 1) await Promise.resolve();
};

let disposeOpener: (() => void) | undefined;

beforeEach(() => {
  setActivePinia(createPinia());
  for (const mock of Object.values(mocks)) if (typeof mock === "function") mock.mockReset();
  for (const mock of Object.values(mocks.message)) mock.mockReset();
  mocks.push.mockResolvedValue(undefined);
});

afterEach(() => {
  disposeOpener?.();
  disposeOpener = undefined;
  vi.useRealTimers();
});

describe("debug guidance", () => {
  it("maps languages to the official debugger plugins", () => {
    expect(OFFICIAL_DEBUGGER_PLUGINS).toEqual({ python: "python-debug", java: "java-debug", go: "go-debug", php: "php-debug" });
  });

  it("opens plugin manager when no debugger for python", async () => {
    mocks.listDebuggers.mockResolvedValue([]);
    mocks.confirm.mockRejectedValueOnce("cancel");
    await expect(ensureDebuggerFor("python", ROOT)).resolves.toBeNull();
    expect(mocks.push).not.toHaveBeenCalled();

    mocks.confirm.mockResolvedValueOnce("confirm");
    await expect(ensureDebuggerFor("python", ROOT)).resolves.toBeNull();
    expect(String(mocks.confirm.mock.calls[1]![0])).toContain("Python");
    expect(mocks.push).toHaveBeenCalledWith({ name: "plugins", query: { search: "python-debug" } });
  });

  it("returns the installed debugger of the language without asking", async () => {
    const debugger_ = pythonDebugger();
    mocks.listDebuggers.mockResolvedValue([pythonDebugger({ type: "node", languages: ["javascript"] }), debugger_]);
    await expect(ensureDebuggerFor("python", ROOT)).resolves.toEqual(debugger_);
    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(mocks.checkDebugger).not.toHaveBeenCalled();
  });

  it("a failed debugger listing shows the error instead of the install prompt", async () => {
    mocks.listDebuggers.mockRejectedValue(new Error("host down"));
    await expect(ensureDebuggerFor("python", ROOT)).resolves.toBeNull();
    expect(mocks.message.error).toHaveBeenCalledTimes(1);
    expect(String((mocks.message.error.mock.calls[0]![0] as { message: string }).message)).toContain("host down");
    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(mocks.push).not.toHaveBeenCalled();
    await expect(launchConfigurationFor(ROOT, "python")).resolves.toBeNull();
    expect(useDebugConfigsStore().stateFor(ROOT).configs).toHaveLength(0);
  });

  it("unsupported language shows notice and returns null", async () => {
    mocks.listDebuggers.mockResolvedValue([pythonDebugger()]);
    await expect(ensureDebuggerFor("cobol", ROOT)).resolves.toBeNull();
    expect(mocks.message.info).toHaveBeenCalledTimes(1);
    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(mocks.push).not.toHaveBeenCalled();
  });

  it("missing tools dialog runs install command in a terminal only after confirm", async () => {
    const opener = vi.fn();
    disposeOpener = setDebugTerminalOpener(opener);
    mocks.checkDebugger.mockResolvedValue({
      ready: false,
      missing: [{ name: "debugpy", hint: "pip install debugpy", installCommand: INSTALL }],
    });

    mocks.confirm.mockRejectedValueOnce("cancel");
    await expect(checkBeforeLaunch(pythonDebugger(), ROOT, cfg, FILE)).resolves.toBe(false);
    expect(mocks.checkDebugger).toHaveBeenCalledWith({
      pluginId: "python-debug",
      type: "python",
      projectRoot: ROOT,
      configuration: { program: FILE, request: "launch" },
    });
    const content = mocks.confirm.mock.calls[0]![0] as VNode;
    expect((content.props as { missing: unknown }).missing).toEqual([
      { name: "debugpy", hint: "pip install debugpy", installCommand: INSTALL, runnable: true },
    ]);
    expect(opener).not.toHaveBeenCalled();

    mocks.confirm.mockResolvedValueOnce("confirm");
    await expect(checkBeforeLaunch(pythonDebugger(), ROOT, cfg, FILE)).resolves.toBe(false);
    expect(opener).toHaveBeenCalledTimes(1);
    expect(opener).toHaveBeenCalledWith({ command: INSTALL, cwd: ROOT, title: "debugpy" });
  });

  it("an install command with invisible characters is shown escaped and never run", async () => {
    const opener = vi.fn();
    disposeOpener = setDebugTerminalOpener(opener);
    const hidden = "pip install\u200b debugpy";
    mocks.checkDebugger.mockResolvedValue({
      ready: false,
      missing: [
        { name: "debugpy", hint: "", installCommand: hidden },
        { name: "pytest", hint: "", installCommand: "pip install pytest" },
      ],
    });
    mocks.confirm.mockResolvedValueOnce("confirm");
    await expect(checkBeforeLaunch(pythonDebugger(), ROOT, cfg, FILE)).resolves.toBe(false);
    expect(opener).toHaveBeenCalledTimes(1);
    expect(opener).toHaveBeenCalledWith({ command: "pip install pytest", cwd: ROOT, title: "pytest" });

    const content = mocks.confirm.mock.calls[0]![0] as VNode;
    const dialog = mount(content.type as Component, { props: content.props as Record<string, unknown> });
    const commands = dialog.findAll("[data-install-command]").map((item) => item.text());
    expect(commands).toEqual(["pip install\\u{200b} debugpy", "pip install pytest"]);
    expect(dialog.findAll("[data-command-unsafe]")).toHaveLength(1);
    expect(dialog.findAll("[data-missing-tool]")[0]!.find("[data-command-unsafe]").exists()).toBe(true);
    dialog.unmount();
  });

  it("only display-only commands: nothing to run in a terminal", async () => {
    const opener = vi.fn();
    disposeOpener = setDebugTerminalOpener(opener);
    mocks.checkDebugger.mockResolvedValue({
      ready: false,
      missing: [{ name: "debugpy", hint: "", installCommand: "pip\u202einstall debugpy" }],
    });
    mocks.confirm.mockResolvedValueOnce("confirm");
    await expect(checkBeforeLaunch(pythonDebugger(), ROOT, cfg, FILE)).resolves.toBe(false);
    expect((mocks.confirm.mock.calls[0]![2] as { showConfirmButton: boolean }).showConfirmButton).toBe(false);
    expect(opener).not.toHaveBeenCalled();
  });

  it("passes when the check is ready", async () => {
    mocks.checkDebugger.mockResolvedValue({ ready: true, missing: [] });
    await expect(checkBeforeLaunch(pythonDebugger(), ROOT, cfg, FILE)).resolves.toBe(true);
    expect(mocks.confirm).not.toHaveBeenCalled();
  });

  it("check is skipped when hasCheck is false", async () => {
    await expect(checkBeforeLaunch(pythonDebugger({ hasCheck: false }), ROOT, cfg, FILE)).resolves.toBe(true);
    expect(mocks.checkDebugger).not.toHaveBeenCalled();
  });

  it("every launch checks the tools before preparing, and stops when they are missing", async () => {
    mocks.listDebuggers.mockResolvedValue([pythonDebugger()]);
    mocks.checkDebugger.mockResolvedValue({ ready: false, missing: [{ name: "Python", hint: "Install Python 3" }] });
    mocks.confirm.mockRejectedValueOnce("cancel");
    await expect(launchDebugSession(ROOT, cfg, { noDebug: false, activeFile: FILE })).resolves.toBeNull();
    expect(mocks.checkDebugger).toHaveBeenCalledTimes(1);
    expect(mocks.prepareDebugger).not.toHaveBeenCalled();

    mocks.checkDebugger.mockResolvedValue({ ready: true, missing: [] });
    mocks.prepareDebugger.mockResolvedValue(prepared());
    mocks.startDebugger.mockResolvedValue({ sessionId: "s1", capabilities: {}, skippedBreakpoints: [] });
    await expect(launchDebugSession(ROOT, cfg, { noDebug: true, activeFile: FILE })).resolves.toMatchObject({ sessionId: "s1" });
    expect(mocks.checkDebugger).toHaveBeenCalledTimes(2);
  });

  it("a configuration whose debugger plugin is gone offers the plugin manager instead of launching", async () => {
    mocks.listDebuggers.mockResolvedValue([]);
    mocks.confirm.mockResolvedValueOnce("confirm");
    await expect(launchDebugSession(ROOT, cfg, { noDebug: false, activeFile: FILE })).resolves.toBeNull();
    expect(mocks.push).toHaveBeenCalledWith({ name: "plugins", query: { search: "python-debug" } });
    expect(mocks.prepareDebugger).not.toHaveBeenCalled();
  });

  it("first debug on a file without configurations creates a current-file configuration", async () => {
    mocks.listDebuggers.mockResolvedValue([pythonDebugger()]);
    const created = await launchConfigurationFor(ROOT, "python");
    expect(created).toMatchObject({ pluginId: "python-debug", type: "python", request: "launch", fields: { program: "${file}" } });
    const configs = useDebugConfigsStore();
    const state = configs.stateFor(ROOT);
    expect(state.configs).toHaveLength(1);
    expect(state.selectedConfigId).toBe((created as RunConfiguration).id);
    expect(mocks.checkDebugger).not.toHaveBeenCalled();

    // Afterwards the selected configuration is used.
    await expect(launchConfigurationFor(ROOT, "python")).resolves.toMatchObject({ id: (created as RunConfiguration).id });
  });

  it("without a language, a language without any debugger, or without a launch snippet, asks for a new configuration", async () => {
    await expect(launchConfigurationFor(ROOT, undefined)).resolves.toBe("new");
    mocks.listDebuggers.mockResolvedValue([pythonDebugger()]);
    await expect(launchConfigurationFor(ROOT, "cobol")).resolves.toBe("new");
    expect(mocks.message.info).not.toHaveBeenCalled();
    expect(mocks.confirm).not.toHaveBeenCalled();
    mocks.listDebuggers.mockResolvedValue([pythonDebugger({ configurationSnippets: [] })]);
    await expect(launchConfigurationFor(ROOT, "python")).resolves.toBe("new");
  });

  it("no check runs on file open", async () => {
    vi.useFakeTimers();
    mocks.listDebuggers.mockResolvedValue([pythonDebugger()]);
    mocks.debuggerEntryPoints.mockResolvedValue([]);
    const Harness = defineComponent({
      setup() {
        useProjectEditorDebug({
          activeDocument: () => ({ path: FILE, loaded: true }),
          projectRootForPath: () => ROOT,
          languageIdFor: () => "python",
          editorReady: () => true,
          active: () => true,
          ownsProject: () => true,
          openAt: vi.fn(), openVirtualSource: () => undefined,
        });
        return () => h("div");
      },
    });
    const wrapper = mount(Harness);
    await vi.advanceTimersByTimeAsync(1000);
    await flush();
    expect(mocks.debuggerEntryPoints).toHaveBeenCalledTimes(1);
    expect(mocks.checkDebugger).not.toHaveBeenCalled();
    expect(mocks.confirm).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});
