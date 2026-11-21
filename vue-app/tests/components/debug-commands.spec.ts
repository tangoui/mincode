import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { newSessionView, type DebugSessionView } from "@/domain/debug/session-model";
import type { RunConfiguration } from "@/domain/debug/types";

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => false,
  canInvoke: () => true,
  invokeBackend: vi.fn(),
  listenAppEvent: vi.fn(() => () => undefined),
}));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
  registerDesktopBrowserSnapshotKeys: vi.fn(),
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));

import { registerDebugCommands, type DebugCommandsOptions } from "@/composables/use-debug-commands";
import { buildKeybindingContext } from "@/domain/keybindings/context";
import { executeRegisteredCommand } from "@/services/command-registry";
import { installKeybindingDispatcher } from "@/services/keybinding-dispatcher";
import { useDebugConfigsStore } from "@/stores/debug-configs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const ROOT = "/p";

function session(change: Partial<DebugSessionView> = {}): DebugSessionView {
  return {
    ...newSessionView({ id: "s1", projectRoot: ROOT, configId: "c", configName: "App", type: "python", noDebug: false, capabilities: {}, startedAt: 1 }),
    state: "running",
    ...change,
  };
}

const cfg: RunConfiguration = { id: "cfg-1", name: "App", pluginId: "mincode.python-debug", type: "python", request: "launch", fields: {} };

function options(change: Partial<DebugCommandsOptions> = {}): DebugCommandsOptions {
  return {
    projectRoot: () => ROOT,
    activeFile: () => "/p/app.py",
    ownsProject: (root) => root === ROOT,
    cursor: () => ({ path: "/p/app.py", line: 7 }),
    toggleBreakpoint: vi.fn(),
    openEvaluate: vi.fn(),
    newConfiguration: vi.fn(),
    ...change,
  };
}

const context = () => buildKeybindingContext({ target: null, platform: "mac" });
const key = (init: KeyboardEventInit) => new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("debug commands", () => {
  let disposers: Array<() => void> = [];
  beforeEach(() => {
    setActivePinia(createPinia());
  });
  afterEach(() => {
    for (const dispose of disposers) dispose();
    disposers = [];
    document.body.innerHTML = "";
  });

  it("debug.active follows sessions", () => {
    const sessions = useDebugSessionsStore();
    disposers = registerDebugCommands(options());
    expect(context()["debug.active"]).toBe(false);
    expect(context()["debug.paused"]).toBe(false);

    sessions.sessions.push(session());
    expect(context()["debug.active"]).toBe(true);
    expect(context()["debug.paused"]).toBe(false);

    sessions.sessions[0]!.state = "paused";
    expect(context()["debug.paused"]).toBe(true);

    sessions.sessions[0]!.state = "ended";
    expect(context()["debug.active"]).toBe(false);
    expect(context()["debug.paused"]).toBe(false);

    // A Run (no debugging) session and another workspace's session are not debug sessions here.
    sessions.sessions.push(session({ id: "s2", noDebug: true }), session({ id: "s3", projectRoot: "/other" }));
    expect(context()["debug.active"]).toBe(false);

    for (const dispose of disposers) dispose();
    disposers = [];
    sessions.sessions.push(session({ id: "s4" }));
    expect(context()["debug.active"]).toBeUndefined();
  });

  it("debug keys do not fire while a terminal has focus", async () => {
    const sessions = useDebugSessionsStore();
    sessions.sessions.push(session({ state: "paused" }));
    const stepOver = vi.spyOn(sessions, "stepOver").mockResolvedValue(undefined);
    const opts = options();
    disposers = registerDebugCommands(opts);
    const uninstall = installKeybindingDispatcher();
    disposers.push(uninstall);

    const pane = document.createElement("div");
    pane.tabIndex = 0;
    const terminal = document.createElement("div");
    terminal.className = "xterm";
    const terminalInput = document.createElement("textarea");
    terminal.append(terminalInput);
    document.body.append(pane, terminal);

    pane.dispatchEvent(key({ key: "F8", code: "F8" }));
    await flush();
    expect(stepOver).toHaveBeenCalledWith("s1");

    const inTerminal = key({ key: "F8", code: "F8" });
    terminalInput.dispatchEvent(inTerminal);
    const toggle = key({ key: "F8", code: "F8", metaKey: true });
    terminalInput.dispatchEvent(toggle);
    await flush();
    expect(stepOver).toHaveBeenCalledTimes(1);
    expect(inTerminal.defaultPrevented).toBe(false);
    expect(toggle.defaultPrevented).toBe(false);
    expect(opts.toggleBreakpoint).not.toHaveBeenCalled();

    pane.dispatchEvent(key({ key: "F8", code: "F8", metaKey: true }));
    await flush();
    expect(opts.toggleBreakpoint).toHaveBeenCalledWith(7);
  });

  it("step, resume and run to cursor act on this workspace's debug session", async () => {
    const sessions = useDebugSessionsStore();
    sessions.sessions.push(session({ id: "other", projectRoot: "/other", state: "paused" }), session({ state: "paused" }));
    sessions.selectSession("other");
    const spies = {
      stepInto: vi.spyOn(sessions, "stepInto").mockResolvedValue(undefined),
      stepOut: vi.spyOn(sessions, "stepOut").mockResolvedValue(undefined),
      resume: vi.spyOn(sessions, "resume").mockResolvedValue(undefined),
      runToCursor: vi.spyOn(sessions, "runToCursor").mockResolvedValue(undefined),
    };
    disposers = registerDebugCommands(options());
    await executeRegisteredCommand("project:debug.stepInto");
    expect(spies.stepInto).toHaveBeenCalledWith("s1");
    expect(sessions.selectedSessionId).toBe("s1");
    await executeRegisteredCommand("project:debug.stepOut");
    expect(spies.stepOut).toHaveBeenCalledWith("s1");
    await executeRegisteredCommand("project:debug.runToCursor");
    expect(spies.runToCursor).toHaveBeenCalledWith("s1", "/p/app.py", 7);
    await executeRegisteredCommand("project:debug.resume");
    expect(spies.resume).toHaveBeenCalledWith("s1");
  });

  it("evaluate opens the dialog; run and debug start the selected configuration", async () => {
    const configs = useDebugConfigsStore();
    const sessions = useDebugSessionsStore();
    const start = vi.spyOn(sessions, "start").mockResolvedValue("s9");
    const stop = vi.spyOn(sessions, "stop").mockResolvedValue(undefined);
    const opts = options();
    disposers = registerDebugCommands(opts);

    await executeRegisteredCommand("project:debug.debug");
    expect(opts.newConfiguration).toHaveBeenCalledTimes(1);
    expect(start).not.toHaveBeenCalled();

    configs.upsertConfig(ROOT, cfg);
    await executeRegisteredCommand("project:debug.run");
    expect(start).toHaveBeenLastCalledWith(ROOT, expect.objectContaining({ id: "cfg-1" }), { noDebug: true, activeFile: "/p/app.py" });
    await executeRegisteredCommand("project:debug.debug");
    expect(start).toHaveBeenLastCalledWith(ROOT, expect.objectContaining({ id: "cfg-1" }), { noDebug: false, activeFile: "/p/app.py" });

    // Stop also ends a Run (no debugging) session.
    sessions.sessions.push(session({ id: "run", noDebug: true }));
    await executeRegisteredCommand("project:debug.stop");
    expect(stop).toHaveBeenCalledWith("run");

    sessions.sessions.push(session({ state: "paused" }));
    await executeRegisteredCommand("project:debug.evaluate");
    expect(opts.openEvaluate).toHaveBeenCalledTimes(1);
  });
});
