// Native-only regression fixture, loaded by the terminal-tabs smoke scenario.
import { Terminal, type IDisposable } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { attachTerminalWebglAddon } from "@/services/terminal-preferences";
import { readSettingsSnapshot, updateSettingsRuntimeSnapshot } from "@/services/settings-persistence";
import { fitTerminalSize } from "@/services/terminal-viewport";

const settle = () => new Promise<void>((resolve) => setTimeout(resolve, 250));

export async function verifyTerminalRendererSwitch(log: { done(step: string): void }) {
  const settings = readSettingsSnapshot();
  const host = document.createElement("div");
  Object.assign(host.style, { position: "fixed", left: "100px", top: "120px", width: "640px", height: "320px", zIndex: "10000" });
  document.body.appendChild(host);
  const terminal = new Terminal({ fontFamily: "Menlo, monospace", fontSize: 13, lineHeight: 1.25, cursorBlink: false });
  const fit = new FitAddon();
  let handle: IDisposable | undefined;
  try {
    terminal.loadAddon(fit);
    terminal.open(host);
    await settle();
    fitTerminalSize(terminal, fit);
    // Different colors keep glyphs in separate spans in the DOM renderer.
    await new Promise<void>((resolve) => terminal.write("\r\n\x1b[31mA\x1b[32mB\x1b[33mC\x1b[34mD\x1b[35mE\x1b[36mF\x1b[0m\r\n", resolve));
    await settle();
    const glyphs = () => [...host.querySelectorAll<HTMLElement>(".xterm-rows > div > span")].filter((span) => /^[ABCDEF]$/.test(span.textContent ?? ""));
    const baseline = glyphs().map((span) => span.getBoundingClientRect().width);
    if (baseline.length !== 6 || baseline.some((width) => width < 5 || width > 15)) throw new Error(`invalid renderer fixture baseline: ${JSON.stringify(baseline)}`);
    const size = [terminal.cols, terminal.rows];

    updateSettingsRuntimeSnapshot({ ...settings, webgl: true });
    handle = attachTerminalWebglAddon(terminal);
    await settle();
    host.style.display = "none";
    // This is the same hidden-release path as switching a terminal tab.
    handle?.dispose();
    handle = undefined;
    await settle();
    updateSettingsRuntimeSnapshot({ ...settings, webgl: false });
    host.style.display = "block";
    await settle();
    fitTerminalSize(terminal, fit);
    await settle();
    const restored = glyphs().map((span) => span.getBoundingClientRect().width);
    log.done(`renderer geometry after hidden release: ${JSON.stringify({ baseline, restored, size, after: [terminal.cols, terminal.rows] })}`);
    if (restored.length !== 6 || restored.some((width, index) => Math.abs(width - baseline[index]!) > 1)) {
      throw new Error(`terminal glyph widths changed after tab switch: ${JSON.stringify({ baseline, restored })}`);
    }

    // Force a real GPU-context loss: the fallback must repaint without a DOM
    // resize notification or another tab switch to rescue it.
    updateSettingsRuntimeSnapshot({ ...settings, webgl: true });
    let transitions = 0;
    handle = attachTerminalWebglAddon(terminal, () => {
      transitions += 1;
      fitTerminalSize(terminal, fit);
    });
    await settle();
    const context = [...host.querySelectorAll("canvas")].map((canvas) => canvas.getContext("webgl2")).find((value) => value !== null);
    const extension = context?.getExtension("WEBGL_lose_context");
    if (!extension) throw new Error("native renderer fixture has no WebGL context to test loss");
    extension.loseContext();
    const deadline = performance.now() + 5000;
    while (transitions < 2 && performance.now() < deadline) await settle();
    if (transitions !== 2) throw new Error(`WebGL fallback did not notify the pane: ${transitions} transitions`);
    await settle();
    const fallback = glyphs().map((span) => span.getBoundingClientRect().width);
    if (fallback.length !== 6 || fallback.some((width, index) => Math.abs(width - baseline[index]!) > 1)) {
      throw new Error(`terminal glyph widths changed after context loss: ${JSON.stringify({ baseline, fallback })}`);
    }
    log.done(`real WebGL context loss refit and repainted the DOM fallback: ${JSON.stringify(fallback)}`);
  } finally {
    handle?.dispose();
    terminal.dispose();
    host.remove();
    updateSettingsRuntimeSnapshot(settings);
  }
}
