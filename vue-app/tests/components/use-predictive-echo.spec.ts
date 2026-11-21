import { afterEach, describe, expect, it, vi } from "vitest";
import { Terminal } from "@xterm/xterm";
import type { OverlayFrame, TerminalView } from "@/domain/terminal/predictive-echo/types";
import type { OverlayStyle, PredictiveOverlay } from "@/services/terminal-predictive-overlay";
import { readTerminalPreferences } from "@/services/terminal-preferences";
import { usePredictiveEcho, type PredictiveEchoBinding } from "@/composables/use-predictive-echo";
import { clearPredictiveEchoProbe, enablePredictiveEchoProbe, readPredictiveEchoProbe } from "@/services/e2e-predictive-echo-probe";

// 包一层真实覆盖层：注入假 canvas，记录 draw / remeasure
const spies = vi.hoisted(() => ({
  createCanvas: null as unknown as ReturnType<typeof import("vitest").vi.fn>,
  createOverlay: null as unknown as ReturnType<typeof import("vitest").vi.fn>,
  draws: [] as Array<OverlayFrame | null>,
  views: [] as TerminalView[],
  remeasures: 0,
}));

vi.mock("@/services/terminal-predictive-overlay", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/services/terminal-predictive-overlay")>();
  const fakeCanvas = () => {
    const noop = () => {};
    const ctx = { setTransform: noop, clearRect: noop, fillRect: noop, fillText: noop, globalAlpha: 1, fillStyle: "", font: "", textBaseline: "", textAlign: "" };
    const canvas = document.createElement("canvas");
    Object.defineProperty(canvas, "getContext", { value: () => ctx });
    return canvas;
  };
  spies.createCanvas = vi.fn(fakeCanvas);
  spies.createOverlay = vi.fn((...args: Parameters<typeof real.createPredictiveOverlay>): PredictiveOverlay => {
    const [screen, cols, rows, style] = args;
    const inner = real.createPredictiveOverlay(screen, cols, rows, style, spies.createCanvas as unknown as () => HTMLCanvasElement);
    return {
      draw(frame, view) { spies.draws.push(frame); spies.views.push(view); inner.draw(frame, view); },
      remeasure() { spies.remeasures += 1; inner.remeasure(); },
      setStyle: (s) => inner.setStyle(s),
      dispose: () => inner.dispose(),
    };
  });
  return { ...real, createPredictiveOverlay: spies.createOverlay };
});

const style: OverlayStyle = {
  fontFamily: "Menlo", fontSize: 13, foreground: "#fff", background: "#000", cursorColor: "#0f0", cursorStyle: "bar",
};

let term: Terminal | undefined;
let binding: PredictiveEchoBinding | undefined;

afterEach(() => {
  binding?.dispose();
  term?.dispose();
  binding = undefined;
  term = undefined;
  spies.draws.length = 0;
  spies.views.length = 0;
  spies.remeasures = 0;
  spies.createCanvas.mockClear();
  spies.createOverlay.mockClear();
  vi.useRealTimers();
});

function setup(mode: unknown, options: { enabled?: () => boolean; probeId?: () => string; scrollback?: number } = {}) {
  localStorage.setItem("app_settings_v2", JSON.stringify(mode === undefined ? {} : { predictiveEcho: mode }));
  const host = document.createElement("div");
  document.body.appendChild(host);
  const terminal = new Terminal({ cols: 40, rows: 5, scrollback: options.scrollback ?? 100 });
  terminal.open(host);
  terminal.focus();
  term = terminal;
  const created = usePredictiveEcho({
    terminal: () => terminal,
    screen: () => host.querySelector<HTMLElement>(".xterm-screen"),
    enabled: options.enabled ?? (() => true),
    style: () => style,
    probeId: options.probeId,
  });
  binding = created;
  /** 等 xterm 解析完所有已写入的数据（write 回调按顺序执行） */
  const flush = () => new Promise<void>((resolve) => terminal.write("", resolve));
  const echo = async (data: string) => {
    created.write(data);
    await flush();
  };
  return { terminal, binding: created, flush, echo };
}

const lastDraw = () => spies.draws[spies.draws.length - 1];
const predictedChars = (frame: OverlayFrame | null | undefined) =>
  (frame?.cells ?? []).filter((cell) => cell.predicted).map((cell) => cell.char).join("");

/** 提示符后打 a 并收到回显：本行首字已确认 */
async function confirmedLine(ctx: ReturnType<typeof setup>) {
  await ctx.echo("$ ");
  ctx.binding.input("a");
  await ctx.echo("a");
}

describe("usePredictiveEcho", () => {
  it("typing after a confirmed echo draws a prediction", async () => {
    const ctx = setup("always");
    await confirmedLine(ctx);
    expect(spies.draws).toEqual([]);
    ctx.binding.input("b");
    expect(predictedChars(lastDraw())).toBe("b");
    expect(lastDraw()?.cursorCol).toBe(4);
  });

  it("write confirms and clears", async () => {
    const ctx = setup("always");
    await confirmedLine(ctx);
    ctx.binding.input("b");
    expect(lastDraw()).not.toBeNull();
    await ctx.echo("b");
    expect(lastDraw()).toBeNull();
    expect(ctx.terminal.buffer.active.getLine(0)?.translateToString(true)).toBe("$ ab");
  });

  it("resize clears overlay", async () => {
    const ctx = setup("always");
    await confirmedLine(ctx);
    ctx.binding.input("b");
    const before = spies.remeasures;
    ctx.terminal.resize(50, 6);
    expect(lastDraw()).toBeNull();
    expect(spies.remeasures).toBe(before + 1);
  });

  it("off mode never draws and reads only the cursor line while keys are pending", async () => {
    const ctx = setup("off");
    await ctx.echo("$ ");
    const buffer = ctx.terminal.buffer.active;
    const getLine = vi.spyOn(buffer, "getLine");
    let duringWrites = 0;
    const text = "abcdefghijklmnopqrst";
    for (const ch of text) {
      ctx.binding.input(ch);
      const before = getLine.mock.calls.length;
      await ctx.echo(ch);
      duringWrites += getLine.mock.calls.length - before;
    }
    expect(spies.createCanvas).not.toHaveBeenCalled();
    expect(spies.createOverlay).not.toHaveBeenCalled();
    expect(duringWrites).toBeGreaterThan(0);
    expect(duringWrites).toBeLessThanOrEqual(text.length);

    ctx.binding.input("\r");
    await ctx.echo("\r\n");
    getLine.mockClear();
    for (let i = 0; i < 50; i += 1) ctx.binding.write(`line ${i} ${"x".repeat(30)}\r\n`);
    await ctx.flush();
    expect(getLine).not.toHaveBeenCalled();
  });

  // xterm 6 的 scrollLines 经由视口 DOM 滚动，happy-dom 没有布局不会真的滚动：
  // 这里把 viewportY 改成离开底部，并直接调用 binding 订阅的 onScroll 监听
  function scrollAway(terminal: Terminal) {
    const buffer = terminal.buffer.active;
    const viewportY = buffer.baseY - 3;
    vi.spyOn(buffer, "viewportY", "get").mockReturnValue(viewportY);
    return viewportY;
  }

  it("scrolled up does not predict", async () => {
    const ctx = setup("always");
    for (let i = 0; i < 20; i += 1) ctx.binding.write(`out ${i}\r\n`);
    await confirmedLine(ctx);
    scrollAway(ctx.terminal);
    ctx.binding.input("b");
    expect(predictedChars(lastDraw())).toBe("");
  });

  /** 截获 binding 订阅的 xterm 事件监听（happy-dom 里滚动/选区不会真的发生） */
  function captureListener<K extends "onScroll" | "onSelectionChange">(terminal: Terminal, name: K) {
    const box: { fire: (...args: never[]) => void } = { fire: () => {} };
    const real = terminal[name].bind(terminal) as (handler: (...args: never[]) => void) => { dispose(): void };
    const capture = (handler: (...args: never[]) => void) => {
      box.fire = handler;
      return real(handler);
    };
    Object.defineProperty(terminal, name, { configurable: true, get: () => capture });
    return box;
  }

  it("any scroll clears a shown prediction (output scrolling at the bottom included)", async () => {
    const ctx = setup("always");
    const scroll = captureListener(ctx.terminal, "onScroll");
    for (let i = 0; i < 20; i += 1) ctx.binding.write(`out ${i}\r\n`);
    await confirmedLine(ctx);
    ctx.binding.input("b");
    expect(predictedChars(lastDraw())).toBe("b");
    // 预测自己的回显从不滚屏（折行/回车不预测）：停在底部的滚动也说明屏幕动了
    const remeasures = spies.remeasures;
    (scroll.fire as (y: number) => void)(ctx.terminal.buffer.active.baseY);
    expect(lastDraw()).toBeNull();
    expect(spies.remeasures).toBe(remeasures);

    await confirmedLine(ctx);
    ctx.binding.input("c");
    expect(predictedChars(lastDraw())).toBe("c");
    (scroll.fire as (y: number) => void)(scrollAway(ctx.terminal));
    expect(lastDraw()).toBeNull();
  });

  it("scroll and selection floods without a frame neither remeasure nor force layout", async () => {
    const ctx = setup("always");
    const scroll = captureListener(ctx.terminal, "onScroll");
    const selection = captureListener(ctx.terminal, "onSelectionChange");
    for (let i = 0; i < 20; i += 1) ctx.binding.write(`out ${i}\r\n`);
    await confirmedLine(ctx);
    ctx.binding.input("b");
    await ctx.echo("b");
    expect(lastDraw()).toBeNull();
    const screen = ctx.terminal.element!.querySelector<HTMLElement>(".xterm-screen")!;
    const layout = vi.spyOn(screen, "getBoundingClientRect");
    const remeasures = spies.remeasures;
    const draws = spies.draws.length;
    for (let i = 0; i < 200; i += 1) (scroll.fire as (y: number) => void)(ctx.terminal.buffer.active.baseY);
    scrollAway(ctx.terminal);
    for (let i = 0; i < 200; i += 1) (scroll.fire as (y: number) => void)(0);
    vi.spyOn(ctx.terminal, "hasSelection").mockReturnValue(true);
    for (let i = 0; i < 200; i += 1) (selection.fire as () => void)();
    ctx.terminal.textarea!.blur();
    expect(layout).not.toHaveBeenCalled();
    expect(spies.remeasures).toBe(remeasures);
    expect(spies.draws.length).toBe(draws);
  });

  it("a selection drag over a shown prediction clears it once without remeasuring", async () => {
    const ctx = setup("always");
    const selection = captureListener(ctx.terminal, "onSelectionChange");
    await confirmedLine(ctx);
    ctx.binding.input("b");
    expect(predictedChars(lastDraw())).toBe("b");
    const screen = ctx.terminal.element!.querySelector<HTMLElement>(".xterm-screen")!;
    const layout = vi.spyOn(screen, "getBoundingClientRect");
    const remeasures = spies.remeasures;
    const draws = spies.draws.length;
    vi.spyOn(ctx.terminal, "hasSelection").mockReturnValue(true);
    for (let i = 0; i < 50; i += 1) (selection.fire as () => void)();
    expect(spies.draws.length).toBe(draws + 1);
    expect(lastDraw()).toBeNull();
    expect(spies.remeasures).toBe(remeasures);
    expect(layout).not.toHaveBeenCalled();
  });

  it("blur clears a shown prediction without remeasuring", async () => {
    const ctx = setup("always");
    await confirmedLine(ctx);
    ctx.binding.input("b");
    const remeasures = spies.remeasures;
    ctx.terminal.textarea!.blur();
    expect(lastDraw()).toBeNull();
    expect(spies.remeasures).toBe(remeasures);
  });

  it("the view resolves cell colours from the buffer and the terminal theme", async () => {
    const ctx = setup("always");
    ctx.terminal.options.theme = { red: "#ff1111", foreground: "#eeeeee", background: "#101010" };
    // 行 0：红字蓝底 Q、反显 R、24 位色 S、256 色 T
    await ctx.echo("\x1b[31;44mQ\x1b[7mR\x1b[0m\x1b[38;2;10;11;12mS\x1b[0m\x1b[48;5;208mT\x1b[0m\r\n");
    await confirmedLine(ctx);
    ctx.binding.input("b");
    const view = spies.views[spies.views.length - 1];
    expect(view.cell(0, 0, true)).toMatchObject({ char: "Q", fg: "#ff1111", bg: "#3465a4" });
    expect(view.cell(0, 1, true)).toMatchObject({ char: "R", fg: "#3465a4", bg: "#ff1111" });
    expect(view.cell(0, 2, true)).toMatchObject({ char: "S", fg: "#0a0b0c", bg: "#101010" });
    expect(view.cell(0, 3, true)).toMatchObject({ char: "T", fg: "#eeeeee", bg: "#ff8700" });
    // 不要颜色时不解析
    expect(view.cell(0, 0)).not.toHaveProperty("fg");
  });

  /** 极小的回滚缓冲并写满：之后再滚屏 baseY 不再增加 */
  async function fullScrollback() {
    const ctx = setup("always", { scrollback: 10 });
    for (let i = 0; i < 30; i += 1) ctx.binding.write(`fill ${i}\r\n`);
    await ctx.flush();
    return ctx;
  }

  it("cursorAbsY still changes on a scroll once the scrollback is full", async () => {
    const ctx = await fullScrollback();
    await confirmedLine(ctx);
    ctx.binding.input("b");
    const typed = spies.views[spies.views.length - 1];
    const baseY = ctx.terminal.buffer.active.baseY;
    await ctx.echo("\r\n");
    expect(ctx.terminal.buffer.active.baseY).toBe(baseY);
    const after = spies.views[spies.views.length - 1];
    expect(after).not.toBe(typed);
    expect(after.cursorY).toBe(typed.cursorY);
    expect(after.cursorAbsY).toBe(typed.cursorAbsY + 1);
  });

  it("password prompt scrolled onto the same cell is not an echo, even with a full scrollback", async () => {
    const ctx = await fullScrollback();
    await ctx.echo("$ run");
    ctx.binding.input("\r");
    await ctx.echo("\r\n");
    // 提示符出现前抢先打字（都只是样本）
    ctx.binding.input("x");
    ctx.binding.input("y");
    ctx.binding.input("z");
    // 服务器滚屏后在同一视口行打印恰好同字的提示符
    await ctx.echo("\r\nx");
    expect(ctx.terminal.buffer.active.cursorX).toBe(1);
    expect(spies.draws.filter((frame) => frame !== null)).toEqual([]);
    ctx.binding.input("s");
    expect(spies.draws.filter((frame) => frame !== null)).toEqual([]);
  });

  it("typing and echo without a scroll still confirm with a full scrollback", async () => {
    const ctx = await fullScrollback();
    await confirmedLine(ctx);
    ctx.binding.input("b");
    expect(predictedChars(lastDraw())).toBe("b");
    await ctx.echo("b");
    expect(lastDraw()).toBeNull();
    ctx.binding.input("c");
    expect(predictedChars(lastDraw())).toBe("c");
  });

  it("paste is never predicted", async () => {
    const ctx = setup("always");
    await confirmedLine(ctx);
    ctx.terminal.textarea!.dispatchEvent(new Event("paste", { bubbles: true, cancelable: true }));
    ctx.binding.input("b");
    expect(predictedChars(lastDraw())).toBe("");
    // 粘贴标记只作用于紧随其后的那次输入
    await confirmedLine(ctx);
    ctx.binding.input("\x1b[200~c\x1b[201~");
    expect(predictedChars(lastDraw())).toBe("");
  });

  it("binding.paste is never predicted", async () => {
    const ctx = setup("always");
    // 组件在 onData 里把输入交给 binding（与 SshTerminal 接入方式一致）
    const sub = ctx.terminal.onData((data) => ctx.binding.input(data));
    await confirmedLine(ctx);
    const pasted: string[] = [];
    const spy = ctx.terminal.onData((data) => pasted.push(data));
    ctx.binding.paste("ls");
    expect(pasted).toEqual(["ls"]);
    expect(predictedChars(lastDraw())).toBe("");
    spy.dispose();
    // 之后正常打字照常预测
    await ctx.echo("ls");
    ctx.binding.input("x");
    await ctx.echo("x");
    ctx.binding.input("y");
    expect(predictedChars(lastDraw())).toBe("y");
    sub.dispose();
  });

  it("a throwing write does not leave a pending write behind", async () => {
    const ctx = setup("always");
    await confirmedLine(ctx);
    const realWrite = ctx.terminal.write.bind(ctx.terminal);
    const write = vi.spyOn(ctx.terminal, "write").mockImplementationOnce(() => {
      throw new Error("boom");
    });
    expect(() => ctx.binding.write("zzz")).toThrow("boom");
    write.mockImplementation(realWrite);
    ctx.binding.input("b");
    expect(predictedChars(lastDraw())).toBe("b");
    // 计数没被卡住：回显后照常核对并清空
    await ctx.echo("b");
    expect(lastDraw()).toBeNull();
  });

  it("does nothing after dispose", async () => {
    const ctx = setup("always");
    await confirmedLine(ctx);
    ctx.binding.input("b");
    expect(predictedChars(lastDraw())).toBe("b");
    const textareaRemoved = vi.spyOn(ctx.terminal.textarea!, "removeEventListener");
    const elementRemoved = vi.spyOn(ctx.terminal.element!, "removeEventListener");
    ctx.binding.dispose();
    expect(textareaRemoved.mock.calls.map((call) => call[0]).sort()).toEqual(["blur", "focus"]);
    expect(elementRemoved.mock.calls.map((call) => [call[0], call[2]])).toEqual([["paste", true]]);
    spies.draws.length = 0;
    const remeasures = spies.remeasures;
    ctx.terminal.resize(50, 6);
    ctx.terminal.textarea!.blur();
    ctx.terminal.focus();
    ctx.binding.write("b");
    ctx.binding.input("c");
    ctx.binding.reset();
    await ctx.flush();
    expect(spies.draws).toEqual([]);
    expect(spies.remeasures).toBe(remeasures);
  });

  it("does not predict when unfocused or disabled", async () => {
    let enabled = true;
    const ctx = setup("always", { enabled: () => enabled });
    await confirmedLine(ctx);
    // 焦点不在 textarea（不经 blur 事件，单独验证按键时的判断）
    const active = vi.spyOn(document, "activeElement", "get").mockReturnValue(document.body);
    ctx.binding.input("b");
    expect(predictedChars(lastDraw())).toBe("");
    active.mockRestore();

    await confirmedLine(ctx);
    ctx.binding.input("b");
    expect(predictedChars(lastDraw())).toBe("b");
    ctx.terminal.textarea!.blur();
    expect(lastDraw()).toBeNull();

    ctx.terminal.focus();
    await confirmedLine(ctx);
    enabled = false;
    ctx.binding.input("c");
    expect(predictedChars(lastDraw())).toBe("");
  });

  it("judges the view only after all pending writes are parsed", async () => {
    const ctx = setup("always");
    await confirmedLine(ctx);
    ctx.binding.input("b");
    // 服务器把整行重画成两块：第一块清空该行（中间状态），第二块画出最终结果
    ctx.binding.write("\x1b[2K\r");
    ctx.binding.write("$ ab");
    await ctx.flush();
    expect(lastDraw()).toBeNull();
    // 没被当成猜错：本行仍已确认，下一个字照常预测
    ctx.binding.input("c");
    expect(predictedChars(lastDraw())).toBe("c");
  });

  it("an unconfirmed prediction expires", async () => {
    const ctx = setup("always");
    await confirmedLine(ctx);
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
    ctx.binding.input("b");
    expect(predictedChars(lastDraw())).toBe("b");
    vi.advanceTimersByTime(999);
    expect(lastDraw()).not.toBeNull();
    vi.advanceTimersByTime(2);
    expect(lastDraw()).toBeNull();
  });

  it("reset clears the overlay and remeasures", async () => {
    const ctx = setup("always");
    await confirmedLine(ctx);
    ctx.binding.input("b");
    const before = spies.remeasures;
    ctx.binding.reset();
    expect(lastDraw()).toBeNull();
    expect(spies.remeasures).toBe(before + 1);
  });

  it("rereads the mode on focus", async () => {
    const ctx = setup("always");
    await confirmedLine(ctx);
    localStorage.setItem("app_settings_v2", JSON.stringify({ predictiveEcho: "off" }));
    ctx.terminal.textarea!.blur();
    ctx.terminal.focus();
    await confirmedLine(ctx);
    ctx.binding.input("b");
    expect(predictedChars(lastDraw())).toBe("");
  });
});

describe("usePredictiveEcho e2e probe", () => {
  it("reports draws only once the probe is enabled", async () => {
    const off = setup("always", { probeId: () => "s1" });
    await confirmedLine(off);
    off.binding.input("b");
    expect(readPredictiveEchoProbe("s1").draws).toEqual([]);
    binding?.dispose();
    term?.dispose();

    enablePredictiveEchoProbe();
    const ctx = setup("always", { probeId: () => "s1" });
    await confirmedLine(ctx);
    ctx.binding.input("b");
    ctx.binding.input("c");
    const probe = readPredictiveEchoProbe("s1");
    expect(probe.draws.map((draw) => draw.cells)).toEqual([1, 2]);
    expect(probe.cells).toBe(2);
    await ctx.echo("bc");
    expect(probe.cells).toBe(0);
    expect(probe.draws.length).toBeGreaterThanOrEqual(2);
    expect(probe.misses).toBe(0);
    ctx.binding.input("d");
    await ctx.echo("x");
    expect(probe.misses).toBe(1);
    clearPredictiveEchoProbe("s1");
    expect(readPredictiveEchoProbe("s1").draws).toEqual([]);
  });
});

describe("readTerminalPreferences predictiveEcho", () => {
  const read = (value: unknown) => {
    localStorage.setItem("app_settings_v2", JSON.stringify(value === undefined ? {} : { predictiveEcho: value }));
    return readTerminalPreferences().predictiveEcho;
  };
  it("defaults missing or invalid values to auto", () => {
    expect(read(undefined)).toBe("auto");
    expect(read("bogus")).toBe("auto");
    expect(read(1)).toBe("auto");
  });
  it("keeps valid values", () => {
    expect(read("off")).toBe("off");
    expect(read("always")).toBe("always");
    expect(read("auto")).toBe("auto");
  });
});
