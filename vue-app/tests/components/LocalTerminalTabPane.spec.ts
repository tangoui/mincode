import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h, KeepAlive, ref } from "vue";

const native = vi.hoisted(() => ({
  listeners: new Map<string, (payload: unknown) => void>(),
  invoke: vi.fn(),
  attach: vi.fn(),
  calls: [] as string[],
  disposeError: undefined as Error | undefined,
  written: [] as string[],
  snapshot: { read: vi.fn(), save: vi.fn(), remove: vi.fn() },
  webgl: [] as { dispose: ReturnType<typeof vi.fn> }[],
  layout: false,
  observers: [] as (() => void)[],
  measures: 0,
  settled: [] as (() => void)[],
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options = {};
    buffer = { active: { type: "normal" } };
    modes = { mouseTrackingMode: "none" };
    loadAddon() {}
    open() {
      native.calls.push("open:xterm");
    }
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
    attachCustomWheelEventHandler() {}
    focus() {}
    dispose() {
      native.calls.push("dispose:xterm");
      if (native.disposeError) throw native.disposeError;
    }
  },
}));
vi.mock("@xterm/addon-fit", () => ({ FitAddon: class { fit() {} } }));
vi.mock("@xterm/addon-search", () => ({ SearchAddon: class {} }));
vi.mock("@xterm/addon-serialize", () => ({ SerializeAddon: class { serialize() { return "snapshot"; } } }));
vi.mock("@/services/terminal-web-links", () => ({ createTerminalWebLinksAddon: () => ({ activate() {}, dispose() {} }) }));
vi.mock("@/services/terminal-plugin-links", () => ({ attachPluginTerminalLinks: () => ({ dispose() {} }) }));
vi.mock("@/services/terminal-preferences", () => ({
  readTerminalPreferences: () => ({ scrollback: 1000 }),
  attachTerminalWebglAddon: (_terminal: unknown, onSettled: () => void) => {
    native.calls.push("attach:webgl");
    const handle = { dispose: vi.fn(() => { native.calls.push("dispose:webgl"); }) };
    native.webgl.push(handle);
    native.settled.push(onSettled);
    return handle;
  },
}));
vi.mock("@/services/terminal-theme", () => ({
  resolveTerminalTheme: () => ({}),
  setTerminalTheme: () => undefined,
  TERMINAL_COLOR_SCHEME_CHANGED_EVENT: "terminal-color-scheme-changed",
}));
vi.mock("@/services/terminal-viewport", () => ({
  clampTerminalDimensions: (cols: number, rows: number) => ({ cols, rows }),
  hasTerminalLayout: () => native.layout,
  fitTerminalSize: () => {
    native.measures += 1;
    return { cols: 80, rows: 24 };
  },
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

// Pane chrome of a terminal inside a terminal tab: the WebGL renderer follows tab
// visibility, and the close button explains why it is disabled.

class NoopObserver {
  constructor(callback: () => void) {
    native.observers.push(callback);
  }
  observe() {}
  unobserve() {}
  disconnect() {}
}

let wrapper: ReturnType<typeof mount> | undefined;

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", NoopObserver);
  native.listeners.clear();
  native.calls.length = 0;
  native.disposeError = undefined;
  native.webgl.length = 0;
  native.layout = false;
  native.observers.length = 0;
  native.measures = 0;
  native.settled.length = 0;
  native.invoke.mockReset();
  native.invoke.mockResolvedValue(undefined);
  native.attach.mockReset();
  native.attach.mockResolvedValue({ ok: true, output: "", offset: 0, ended: false });
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const ID = "debug-s1-1";

async function mountPane(props: Record<string, unknown>, errorHandler?: (error: unknown) => void) {
  wrapper = mount(LocalTerminal, {
    global: { config: { errorHandler } },
    props: { sessionId: ID, attachOnlySessionId: ID, canSplit: false, keepAliveOnUnmount: false, ...props },
  });
  await flushPromises();
  return wrapper;
}

describe("LocalTerminal in a terminal tab", () => {
  it("destroys xterm before releasing WebGL so a closed pane does not rebuild its renderer", async () => {
    const pane = await mountPane({ active: true, visible: true });
    native.calls.length = 0;
    pane.unmount();
    wrapper = undefined;
    expect(native.calls.filter((call) => call.startsWith("dispose:"))).toEqual(["dispose:xterm", "dispose:webgl"]);
  });

  it("still releases the WebGL handle if xterm disposal throws", async () => {
    const onError = vi.fn();
    const pane = await mountPane({ active: true, visible: true }, onError);
    const error = new Error("xterm disposal failed");
    native.disposeError = error;
    pane.unmount();
    wrapper = undefined;
    expect(onError.mock.calls[0]?.[0]).toBe(error);
    expect(native.webgl[0]!.dispose).toHaveBeenCalledTimes(1);
  });

  it("releases WebGL at once when its terminal tab is hidden", async () => {
    const pane = await mountPane({ active: true, visible: true });
    expect(native.webgl).toHaveLength(1);
    await pane.setProps({ active: false, visible: false });
    expect(native.webgl[0]!.dispose).toHaveBeenCalledTimes(1);
  });

  it("keeps the WebGL release delay for a pane switch inside a shown tab", async () => {
    vi.useFakeTimers();
    const pane = await mountPane({ active: true, visible: true });
    await pane.setProps({ active: false });
    expect(native.webgl[0]!.dispose).not.toHaveBeenCalled();
    vi.advanceTimersByTime(30_000);
    expect(native.webgl[0]!.dispose).toHaveBeenCalledTimes(1);
  });

  // The PTY is sized with WebGL cell metrics, which fit more columns than the DOM renderer.
  // An inactive pane (WebGL released) that fits with the DOM renderer when its tab is shown
  // again or the window resizes changes the PTY width, and again once it is active; each
  // resize leaves full-screen programs (TUIs) redrawn with remnants.
  it("fits an inactive pane with WebGL, then releases it after the delay", async () => {
    vi.useFakeTimers();
    const pane = await mountPane({ active: false, visible: true });
    expect(native.webgl).toHaveLength(1);
    await pane.setProps({ visible: false });
    expect(native.webgl[0]!.dispose).toHaveBeenCalledTimes(1);
    await pane.setProps({ visible: true });
    native.layout = true;
    vi.stubGlobal("requestAnimationFrame", (callback: () => void) => {
      callback();
      return 1;
    });
    for (const notify of native.observers) notify();
    expect(native.webgl).toHaveLength(2);
    expect(native.webgl[1]!.dispose).not.toHaveBeenCalled();
    vi.advanceTimersByTime(30_000);
    expect(native.webgl[1]!.dispose).toHaveBeenCalledTimes(1);
  });

  // Every visible pane fits with WebGL. A new split pane is visible but not focused: attaching
  // WebGL only at its first fit built, fitted and painted a DOM renderer first, then swapped it.
  it.each([
    { active: true, visible: true },
    { active: false, visible: true },
  ])("attaches WebGL before opening xterm for a visible pane (active: $active)", async (props) => {
    vi.useFakeTimers();
    await mountPane(props);
    expect(native.calls.filter((call) => call === "attach:webgl" || call === "open:xterm")).toEqual(["attach:webgl", "open:xterm"]);
    expect(native.webgl).toHaveLength(1);
    vi.advanceTimersByTime(30_000);
    expect(native.webgl[0]!.dispose).toHaveBeenCalledTimes(props.active ? 0 : 1);
  });

  it("does not attach WebGL for a hidden pane", async () => {
    await mountPane({ active: false, visible: false });
    vi.stubGlobal("requestAnimationFrame", (callback: () => void) => {
      callback();
      return 1;
    });
    for (const notify of native.observers) notify();
    expect(native.webgl).toHaveLength(0);
  });

  it("ignores queued layout notifications while a terminal tab is hidden", async () => {
    const frames: (() => void)[] = [];
    vi.stubGlobal("requestAnimationFrame", (callback: () => void) => frames.push(callback));
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    const pane = await mountPane({ active: true, visible: true });
    await pane.setProps({ active: false, visible: false });
    // A hidden ancestor can leave a numeric computed width on its descendants.
    native.layout = true;
    const measures = native.measures;
    for (const notify of native.observers) notify();
    while (frames.length) frames.shift()!();
    await flushPromises();
    expect(native.measures).toBe(measures);
    expect(native.webgl).toHaveLength(1);
    expect(native.invoke.mock.calls.filter(([command]) => command === "resize_local_shell")).toHaveLength(0);
  });

  it("restores an unfocused pane when its tab returns without a resize notification", async () => {
    const pane = await mountPane({ active: false, visible: false });
    native.layout = true;
    await pane.setProps({ visible: true });
    await flushPromises();
    expect(native.webgl).toHaveLength(1);
    expect(native.webgl[0]!.dispose).not.toHaveBeenCalled();
  });

  it("fits after the restored renderer settles and ignores a late callback after hiding", async () => {
    const frames: (() => void)[] = [];
    vi.stubGlobal("requestAnimationFrame", (callback: () => void) => frames.push(callback));
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    const pane = await mountPane({ active: false, visible: false });
    native.layout = true;
    await pane.setProps({ visible: true });
    await flushPromises();
    expect(native.measures).toBe(0);
    native.settled[0]!();
    while (frames.length) frames.shift()!();
    await flushPromises();
    expect(native.measures).toBe(1);
    expect(native.invoke.mock.calls.filter(([command]) => command === "resize_local_shell")).toEqual([
      ["resize_local_shell", { payload: { sessionId: ID, cols: 80, rows: 24 } }],
    ]);
    await pane.setProps({ visible: false });
    native.settled[0]!();
    while (frames.length) frames.shift()!();
    expect(native.measures).toBe(1);
    expect(native.webgl).toHaveLength(1);
  });

  it("keeps cached workbench panes dormant until activation", async () => {
    const shown = ref(true);
    const frames: (() => void)[] = [];
    vi.stubGlobal("requestAnimationFrame", (callback: () => void) => frames.push(callback));
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    wrapper = mount(defineComponent({
      setup: () => () => h(KeepAlive, null, {
        default: () => shown.value
          ? h(LocalTerminal, { sessionId: ID, attachOnlySessionId: ID, active: false, visible: true })
          : null,
      }),
    }));
    await flushPromises();
    shown.value = false;
    await flushPromises();
    native.layout = true;
    const handles = native.webgl.length;
    for (const notify of native.observers) notify();
    while (frames.length) frames.shift()!();
    expect(native.webgl).toHaveLength(handles);
    shown.value = true;
    await flushPromises();
    expect(native.webgl).toHaveLength(handles + 1);
  });

  // A screen and PTY that disagree on the grid (cause not yet reproduced: the screen showed the
  // xterm default 80 columns over a 179-column PTY) make full-screen programs draw garbage. The
  // send dedupe would keep it that way until the next real size change, so showing a pane and
  // the window regaining focus send the current size again; an unchanged PTY size is a no-op.
  it("re-sends the current size when the pane is shown again or the window regains focus", async () => {
    const frames: (() => void)[] = [];
    vi.stubGlobal("requestAnimationFrame", (callback: () => void) => frames.push(callback));
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    native.layout = true;
    const pane = await mountPane({ active: true, visible: true });
    const resizes = () => native.invoke.mock.calls.filter(([command]) => command === "resize_local_shell").length;
    const settle = async () => {
      for (const done of native.settled.splice(0)) done();
      while (frames.length) frames.shift()!();
      await flushPromises();
    };
    await settle();
    const first = resizes();
    expect(first).toBeGreaterThan(0);

    await pane.setProps({ active: false, visible: false });
    await pane.setProps({ active: true, visible: true });
    await settle();
    expect(resizes()).toBe(first + 1);

    window.dispatchEvent(new Event("focus"));
    await settle();
    expect(resizes()).toBe(first + 2);

    await pane.setProps({ active: false, visible: false });
    window.dispatchEvent(new Event("focus"));
    await settle();
    expect(resizes()).toBe(first + 2);
  });

  it("explains a disabled close button", async () => {
    const pane = await mountPane({ active: true, canClose: false, closeDisabledReason: "已固定的标签不能关闭" });
    const close = pane.get('button[aria-label="关闭窗格"]');
    expect(close.attributes("disabled")).toBeDefined();
    expect(close.attributes("aria-description")).toBe("已固定的标签不能关闭");

    await pane.setProps({ canClose: true });
    expect(pane.get('button[aria-label="关闭窗格"]').attributes("aria-description")).toBeUndefined();
  });
});
