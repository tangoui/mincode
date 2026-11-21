import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";

const native = vi.hoisted(() => ({
  listeners: new Map<string, (payload: unknown) => void>(),
  invoke: vi.fn(),
  attach: vi.fn(),
  wheelHandler: undefined as ((event: WheelEvent) => boolean) | undefined,
  calls: [] as string[],
  written: [] as string[],
  snapshot: { read: vi.fn(), save: vi.fn(), remove: vi.fn() },
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options = {};
    buffer = { active: { type: "alternate" } };
    modes = { mouseTrackingMode: "none" };
    loadAddon() {}
    open() {}
    write(data: string) {
      native.written.push(data);
    }
    writeln(data: string) {
      native.written.push(`${data}\n`);
    }
    reset() {
      native.written.length = 0;
    }
    onData() {
      return { dispose() {} };
    }
    attachCustomKeyEventHandler() {}
    attachCustomWheelEventHandler(handler: (event: WheelEvent) => boolean) {
      native.wheelHandler = handler;
    }
    focus() {}
    dispose() {}
  },
}));
vi.mock("@xterm/addon-fit", () => ({ FitAddon: class { fit() {} } }));
vi.mock("@xterm/addon-search", () => ({ SearchAddon: class {} }));
vi.mock("@xterm/addon-serialize", () => ({ SerializeAddon: class { serialize() { return "snapshot"; } } }));
vi.mock("@/services/terminal-web-links", () => ({ createTerminalWebLinksAddon: () => ({ activate() {}, dispose() {} }) }));
vi.mock("@/services/terminal-plugin-links", () => ({ attachPluginTerminalLinks: () => ({ dispose() {} }) }));
vi.mock("@/services/terminal-preferences", () => ({
  readTerminalPreferences: () => ({ scrollback: 1000 }),
  attachTerminalWebglAddon: () => null,
}));
vi.mock("@/services/terminal-theme", () => ({
  resolveTerminalTheme: () => ({}),
  setTerminalTheme: () => undefined,
  TERMINAL_COLOR_SCHEME_CHANGED_EVENT: "terminal-color-scheme-changed",
}));
vi.mock("@/services/terminal-viewport", () => ({
  clampTerminalDimensions: (cols: number, rows: number) => ({ cols, rows }),
  hasTerminalLayout: () => false,
  waitForTerminalLayout: () => Promise.resolve(),
}));
vi.mock("@/services/terminal-session-cache", () => ({
  readTerminalSnapshot: native.snapshot.read,
  saveTerminalSnapshot: native.snapshot.save,
  deleteTerminalSnapshot: native.snapshot.remove,
}));
vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  canInvoke: () => true,
  invokeBackend: native.invoke,
  listenAppEvent: (name: string, handler: (payload: unknown) => void) => {
    native.calls.push(`listen:${name}`);
    native.listeners.set(name, handler);
    return () => native.listeners.delete(name);
  },
  copyText: vi.fn(),
  readText: vi.fn(),
}));
vi.mock("@/services/debug-service", () => ({
  attachTerminal: (id: string) => {
    native.calls.push(`attach:${id}`);
    return native.attach(id);
  },
}));

import LocalTerminal from "@/components/terminal/LocalTerminal.vue";

class NoopObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

let wrapper: ReturnType<typeof mount> | undefined;

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", NoopObserver);
  native.listeners.clear();
  native.calls.length = 0;
  native.written.length = 0;
  native.invoke.mockReset();
  native.invoke.mockResolvedValue(undefined);
  native.attach.mockReset();
  native.wheelHandler = undefined;
  for (const mock of Object.values(native.snapshot)) mock.mockReset();
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.unstubAllGlobals();
});

const ID = "debug-s1-1";

function emitOutput(data: string, offset: number) {
  native.listeners.get(`shell-output:${ID}`)?.({ sessionId: ID, data, offset });
}

describe("LocalTerminal attach-only mode", () => {
  it("maps an alternate-screen wheel event to PageDown input", async () => {
    native.attach.mockResolvedValue({ ok: true, output: "", offset: 0, ended: false });
    wrapper = mount(LocalTerminal, { props: { sessionId: ID, attachOnlySessionId: ID } });
    await flushPromises();

    expect(native.wheelHandler).toBeTypeOf("function");
    expect(native.wheelHandler?.({ deltaY: 120 } as WheelEvent)).toBe(false);
    await flushPromises();
    expect(native.invoke.mock.calls).toContainEqual([
      "write_to_shell",
      { payload: { sessionId: ID, input: "\u001b[6~" } },
    ]);
  });

  it("subscribes before attaching, shows the kept output once and never starts or closes the shell", async () => {
    let resolveAttach: (value: unknown) => void = () => undefined;
    native.attach.mockReturnValue(new Promise((resolve) => (resolveAttach = resolve)));
    wrapper = mount(LocalTerminal, {
      props: { sessionId: ID, attachOnlySessionId: ID, active: true, canClose: false, canSplit: false, keepAliveOnUnmount: false },
    });
    await flushPromises();
    expect(native.calls.indexOf(`listen:shell-output:${ID}`)).toBeGreaterThanOrEqual(0);
    expect(native.calls.indexOf(`listen:shell-output:${ID}`)).toBeLessThan(native.calls.indexOf(`attach:${ID}`));

    // Events while attaching: the first is inside the attached output, the second after it.
    emitOutput("hello ", 6);
    emitOutput("world", 11);
    resolveAttach({ ok: true, output: "hello ", offset: 6, ended: false });
    await flushPromises();
    emitOutput("hello ", 6);
    emitOutput("!", 12);
    expect(native.written.join("")).toBe("hello world!");
    expect(native.snapshot.read).not.toHaveBeenCalled();

    wrapper.unmount();
    wrapper = undefined;
    const commands = native.invoke.mock.calls.map(([command]) => command);
    expect(commands).not.toContain("start_local_shell");
    expect(commands).not.toContain("close_local_shell");
    expect(native.snapshot.save).not.toHaveBeenCalled();
  });

  it("an end reported while attaching wins over the attach result", async () => {
    let resolveAttach: (value: unknown) => void = () => undefined;
    native.attach.mockReturnValue(new Promise((resolve) => (resolveAttach = resolve)));
    wrapper = mount(LocalTerminal, { props: { sessionId: ID, attachOnlySessionId: ID } });
    await flushPromises();
    native.listeners.get("shell-session-ended")?.({ sessionId: ID, recoverable: false, reason: "exited" });
    // The attach was answered before the program ended.
    resolveAttach({ ok: true, output: "bye\r\n", offset: 5, ended: false });
    await flushPromises();
    expect(wrapper.find(".terminal-pane-status").classes()).toContain("is-ended");
    expect(native.written[0]).toBe("bye\r\n");
    expect(native.written.filter((line) => line.includes("[MinCode]"))).toHaveLength(1);
  });

  it("shows an ended program's output without accepting input", async () => {
    native.attach.mockResolvedValue({ ok: true, output: "done\r\n", offset: 6, ended: true });
    wrapper = mount(LocalTerminal, { props: { sessionId: ID, attachOnlySessionId: ID } });
    await flushPromises();
    expect(native.written[0]).toBe("done\r\n");
    expect(wrapper.find(".terminal-pane-status").classes()).toContain("is-ended");
  });
});
