import { describe, expect, it, vi } from "vitest";

vi.mock("@/services/settings-persistence", () => ({ readSettingsSnapshot: () => ({ webgl: true }) }));
const contextLoss = vi.hoisted(() => ({ emit: () => {} }));

// Fake WebGL addon: on load it adds a canvas to the terminal, as the real renderer does.
vi.mock("@xterm/addon-webgl", () => ({
  WebglAddon: class {
    private canvas?: HTMLCanvasElement;
    onContextLoss(listener: () => void) { contextLoss.emit = listener; }
    activate(terminal: { element: HTMLElement }) {
      this.canvas = document.createElement("canvas");
      this.canvas.dataset.renderer = "webgl";
      terminal.element.appendChild(this.canvas);
    }
    dispose() {
      this.canvas?.remove();
    }
  },
}));

import { attachTerminalWebglAddon } from "@/services/terminal-preferences";

describe("terminal WebGL renderer release", () => {
  it("loses the renderer's GL context on dispose and leaves other canvases alone", async () => {
    const element = document.createElement("div");
    const other = document.createElement("canvas");
    element.appendChild(other);
    const otherGetContext = vi.spyOn(other, "getContext");
    const loseContext = vi.fn();
    const terminal = {
      element,
      loadAddon(addon: { activate(terminal: unknown): void }) {
        addon.activate(terminal);
        const canvas = element.querySelector<HTMLCanvasElement>("canvas[data-renderer]")!;
        vi.spyOn(canvas, "getContext").mockReturnValue({ getExtension: (name: string) => (name === "WEBGL_lose_context" ? { loseContext } : null) } as never);
      },
    };
    const handle = attachTerminalWebglAddon(terminal as never)!;
    await vi.waitFor(() => expect(element.querySelector("canvas[data-renderer]")).not.toBeNull());

    handle.dispose();
    expect(loseContext).toHaveBeenCalledTimes(1);
    expect(otherGetContext).not.toHaveBeenCalled();
    expect(element.querySelector("canvas[data-renderer]")).toBeNull();
  });

  it("reports once the renderer settled, so the terminal can fit to its cell size; not after a release", async () => {
    const terminal = () => {
      const element = document.createElement("div");
      return { element, loadAddon: (addon: { activate(terminal: unknown): void }) => addon.activate({ element }) };
    };
    const settled = vi.fn();
    attachTerminalWebglAddon(terminal() as never, settled);
    await vi.waitFor(() => expect(settled).toHaveBeenCalledTimes(1));

    const released = vi.fn();
    attachTerminalWebglAddon(terminal() as never, released)!.dispose();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(released).not.toHaveBeenCalled();
  });

  it("notifies the pane to refit and repaint when WebGL falls back after context loss", async () => {
    const element = document.createElement("div");
    const settled = vi.fn();
    const terminal = { element, loadAddon: (addon: { activate(terminal: unknown): void }) => addon.activate({ element }) };
    const handle = attachTerminalWebglAddon(terminal as never, settled)!;
    await vi.waitFor(() => expect(settled).toHaveBeenCalledTimes(1));
    expect(element.querySelector("canvas")).not.toBeNull();

    contextLoss.emit();
    expect(element.querySelector("canvas")).toBeNull();
    expect(settled).toHaveBeenCalledTimes(2);

    handle.dispose();
    contextLoss.emit();
    expect(settled).toHaveBeenCalledTimes(2);
  });
});
