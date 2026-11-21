import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/settings-persistence", () => ({ readSettingsSnapshot: () => ({ webgl: true }) }));

// Fake WebGL addon. Like the real one, loading it into an unopened terminal waits for open.
vi.mock("@xterm/addon-webgl", () => ({
  WebglAddon: class {
    private canvas?: HTMLCanvasElement;
    onContextLoss() {}
    activate(terminal: FakeTerminal) {
      if (!terminal.element) {
        terminal.willOpen.push(() => this.activate(terminal));
        return;
      }
      this.canvas = document.createElement("canvas");
      terminal.element.appendChild(this.canvas);
    }
    dispose() {
      this.canvas?.remove();
    }
  },
}));

interface FakeTerminal {
  element?: HTMLElement;
  willOpen: (() => void)[];
  loadAddon: ReturnType<typeof vi.fn>;
  open(): void;
}

function fakeTerminal(): FakeTerminal {
  const terminal: FakeTerminal = {
    element: undefined,
    willOpen: [],
    loadAddon: vi.fn((addon: { activate(terminal: FakeTerminal): void }) => addon.activate(terminal)),
    open() {
      terminal.element = document.createElement("div");
      for (const listener of terminal.willOpen) listener();
    },
  };
  return terminal;
}

async function freshModule() {
  vi.resetModules();
  return import("@/services/terminal-preferences");
}

describe("terminal WebGL preload", () => {
  beforeEach(() => vi.clearAllMocks());

  it("without a preload the renderer still loads asynchronously, after open", async () => {
    const { attachTerminalWebglAddon } = await freshModule();
    const terminal = fakeTerminal();
    terminal.open();
    attachTerminalWebglAddon(terminal as never);
    expect(terminal.loadAddon).not.toHaveBeenCalled();
    await vi.waitFor(() => expect(terminal.loadAddon).toHaveBeenCalledTimes(1));
  });

  it("once preloaded, loads the renderer at once, before open, so xterm never builds a DOM renderer first", async () => {
    const { attachTerminalWebglAddon, preloadTerminalWebgl } = await freshModule();
    await preloadTerminalWebgl();
    const terminal = fakeTerminal();
    const settled = vi.fn();
    attachTerminalWebglAddon(terminal as never, settled);
    expect(terminal.loadAddon).toHaveBeenCalledTimes(1);
    terminal.open();
    expect(terminal.element?.querySelector("canvas")).not.toBeNull();
    // Settling stays asynchronous, as with a lazy load: callers assign the handle first.
    expect(settled).not.toHaveBeenCalled();
    await vi.waitFor(() => expect(settled).toHaveBeenCalledTimes(1));
  });

  it("releasing a renderer loaded before open loses the GL context of the canvas open created", async () => {
    const { attachTerminalWebglAddon, preloadTerminalWebgl } = await freshModule();
    await preloadTerminalWebgl();
    const terminal = fakeTerminal();
    const handle = attachTerminalWebglAddon(terminal as never)!;
    terminal.open();
    const canvas = terminal.element!.querySelector("canvas")!;
    const loseContext = vi.fn();
    vi.spyOn(canvas, "getContext").mockReturnValue({ getExtension: (name: string) => (name === "WEBGL_lose_context" ? { loseContext } : null) } as never);

    handle.dispose();
    expect(loseContext).toHaveBeenCalledTimes(1);
    expect(terminal.element!.querySelector("canvas")).toBeNull();
  });
});
