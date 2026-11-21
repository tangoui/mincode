import assert from "node:assert/strict";
import test from "node:test";
import { createPredictionGate } from "../src/domain/terminal/predictive-echo/gate.ts";
import { createPredictiveEcho } from "../src/domain/terminal/predictive-echo/engine.ts";
import { charWidth, isPredictableChar } from "../src/domain/terminal/predictive-echo/char-width.ts";

const WIDE = /[ᄀ-ᅟ⺀-〾ぁ-㏿㐀-䶿一-鿿가-힣＀-｠]/u;

/** 二维数组实现的终端视图；type() 模拟服务器回显后的格子与光标。 */
class FakeView {
  constructor(cols = 40, rows = 5) {
    this.cols = cols;
    this.rows = rows;
    this.cursorX = 0;
    this.cursorY = 0;
    this.alternate = false;
    this.atBottom = true;
    this.appCursorKeys = false;
    this.baseY = 0;
    this.cellCalls = 0;
    this.grid = Array.from({ length: rows }, () => this.blankRow());
  }
  get cursorAbsY() {
    return this.baseY + this.cursorY;
  }
  blankRow() {
    return Array.from({ length: this.cols }, () => ({ char: " ", width: 1, dim: false }));
  }
  cell(row, col) {
    this.cellCalls += 1;
    return this.grid[row][col];
  }
  /** 覆盖写入并推进光标（宽字符占两格）。 */
  type(text, dim = false) {
    for (const ch of text) {
      const w = WIDE.test(ch) ? 2 : 1;
      const row = this.grid[this.cursorY];
      row[this.cursorX] = { char: ch, width: w, dim };
      if (w === 2) row[this.cursorX + 1] = { char: "", width: 0, dim };
      this.cursorX += w;
    }
    return this;
  }
  /** 整行重写，光标不动。 */
  setLine(row, text, dimText = "") {
    const saved = [this.cursorX, this.cursorY];
    this.grid[row] = this.blankRow();
    this.cursorY = row;
    this.cursorX = 0;
    this.type(text);
    this.type(dimText, true);
    [this.cursorX, this.cursorY] = saved;
    return this;
  }
  moveTo(x, y = this.cursorY) {
    this.cursorX = x;
    this.cursorY = y;
    return this;
  }
}

function spyGate(mode) {
  const gate = createPredictionGate(mode);
  const spy = { misses: 0, latencies: [] };
  const recordMiss = gate.recordMiss.bind(gate);
  const recordLatency = gate.recordLatency.bind(gate);
  gate.recordMiss = (now) => {
    spy.misses += 1;
    recordMiss(now);
  };
  gate.recordLatency = (ms) => {
    spy.latencies.push(ms);
    recordLatency(ms);
  };
  return { gate, spy };
}

function setup(mode = "always", cols = 40) {
  const { gate, spy } = spyGate(mode);
  const frames = [];
  const engine = createPredictiveEcho(mode, (f) => frames.push(f), gate);
  const view = new FakeView(cols);
  return { engine, view, frames, gate, spy };
}

/**
 * 让 row 0 内容为 text、光标在 col，并让本行已有一个字被真实回显确认
 * （text[col - 1] 作为首字：先不回显地输入，再由服务器回显）。
 */
function arm(ctx, text, col, now = 0, dim = "") {
  const { engine, view } = ctx;
  const chars = [...text];
  const before = chars.slice(0, col - 1).join("") + " " + chars.slice(col).join("");
  view.setLine(0, before, dim).moveTo(col - 1, 0);
  engine.userInput(chars[col - 1], view, now, false);
  assert.equal(engine.frame(), null, "first char of a line is never predicted");
  view.setLine(0, text, dim).moveTo(col, 0);
  engine.serverParsed(view, now + 50);
  assert.equal(engine.frame(), null);
}

const pred = (col, char, width = 1) => ({ col, char, width, predicted: true });

test("char width", () => {
  assert.equal(charWidth("a"), 1);
  assert.equal(charWidth("中"), 2);
  assert.equal(charWidth("あ"), 2);
  assert.equal(charWidth("한"), 2);
  assert.equal(charWidth("Ａ"), 2);
  assert.equal(charWidth("，"), 2);
  assert.equal(charWidth("😀"), 2);
  assert.equal(isPredictableChar("a"), true);
  assert.equal(isPredictableChar("́"), false, "combining mark");
  assert.equal(isPredictableChar("​"), false, "zero width space");
  assert.equal(isPredictableChar("\x01"), false);
  assert.equal(isPredictableChar("ab"), false);
});

test("predicts after first confirmed echo", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  view.setLine(0, "$ ").moveTo(2, 0);
  engine.userInput("l", view, 0, false);
  assert.equal(engine.frame(), null);
  view.type("l");
  engine.serverParsed(view, 60);
  engine.userInput("s", view, 100, false);
  assert.deepEqual(engine.frame(), { row: 0, cells: [pred(3, "s")], cursorCol: 4 });
  assert.deepEqual(ctx.frames.at(-1), engine.frame());
});

test("confirm removes prediction", () => {
  const ctx = setup();
  const { engine, view, spy } = ctx;
  arm(ctx, "$ l", 3);
  engine.userInput("s", view, 100, false);
  view.type("s");
  engine.serverParsed(view, 180);
  assert.equal(engine.frame(), null);
  assert.equal(ctx.frames.at(-1), null);
  assert.deepEqual(spy.latencies, [50, 80]);
  assert.equal(spy.misses, 0);
});

test("partial echo keeps the rest", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  arm(ctx, "$ l", 3);
  for (const ch of "abcde") engine.userInput(ch, view, 100, false);
  assert.equal(engine.frame().cells.length, 5);
  view.type("ab");
  engine.serverParsed(view, 150);
  assert.deepEqual(engine.frame(), {
    row: 0,
    cells: [pred(5, "c"), pred(6, "d"), pred(7, "e")],
    cursorCol: 8,
  });
});

test("full line redraw confirms", () => {
  const ctx = setup();
  const { engine, view, spy } = ctx;
  arm(ctx, "$ l", 3);
  engine.userInput("s", view, 100, false);
  view.setLine(0, "").moveTo(0, 0).type("$ ls");
  engine.serverParsed(view, 170);
  assert.equal(engine.frame(), null);
  assert.equal(spy.latencies.at(-1), 70);
  assert.equal(spy.misses, 0);
});

test("mismatch clears and next prediction is tentative", () => {
  const ctx = setup();
  const { engine, view, spy } = ctx;
  arm(ctx, "$ l", 3);
  engine.userInput("s", view, 100, false);
  view.type("x");
  engine.serverParsed(view, 150);
  assert.equal(engine.frame(), null);
  assert.equal(spy.misses, 1);
  // 猜错清除了首字标记：下一个字不预测
  engine.userInput("a", view, 200, false);
  assert.equal(engine.frame(), null);
  view.type("a");
  engine.serverParsed(view, 250);
  engine.userInput("b", view, 300, false);
  engine.userInput("c", view, 301, false);
  assert.deepEqual(engine.frame().cells, [pred(5, "b")], "tentative: only one char");
});

test("unrelated output keeps predictions", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  arm(ctx, "$ l", 3);
  engine.userInput("s", view, 100, false);
  const before = engine.frame();
  const calls = ctx.frames.length;
  view.setLine(2, "tail -f output");
  engine.serverParsed(view, 150);
  assert.deepEqual(engine.frame(), before);
  assert.equal(ctx.frames.length, calls, "unchanged frame does not call onFrame");
});

test("cursor row change without confirmation is a miss", () => {
  const ctx = setup();
  const { engine, view, spy } = ctx;
  arm(ctx, "$ l", 3);
  engine.userInput("s", view, 100, false);
  view.moveTo(0, 1);
  engine.serverParsed(view, 150);
  assert.equal(engine.frame(), null);
  assert.equal(spy.misses, 1);
});

test("timeout clears", () => {
  const ctx = setup();
  const { engine, view, spy, gate } = ctx;
  arm(ctx, "$ l", 3);
  assert.equal(engine.nextDeadline(), null);
  engine.userInput("s", view, 100, false);
  assert.equal(gate.timeoutMs(), 1000);
  assert.equal(engine.nextDeadline(), 1100);
  engine.expire(1099);
  assert.notEqual(engine.frame(), null);
  engine.expire(100 + 1001);
  assert.equal(engine.frame(), null);
  assert.equal(spy.misses, 1);
  assert.equal(engine.nextDeadline(), null);
  engine.userInput("t", view, 1200, false);
  assert.equal(engine.frame(), null, "timeout cleared the first-char mark");
});

test("wide char", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  arm(ctx, "$ abcdefgh", 10);
  engine.userInput("中", view, 100, false);
  assert.deepEqual(engine.frame(), { row: 0, cells: [pred(10, "中", 2)], cursorCol: 12 });
  view.type("中");
  engine.serverParsed(view, 150);
  assert.equal(engine.frame(), null);

  const edge = setup();
  arm(edge, "$ " + "x".repeat(36), 38);
  edge.engine.userInput("中", edge.view, 100, false);
  assert.equal(edge.engine.frame(), null, "would wrap");
  edge.engine.userInput("a", edge.view, 101, false);
  assert.equal(edge.engine.frame(), null);
});

test("mid-line insert shifts right", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  arm(ctx, "$ echo world", 7);
  engine.userInput("X", view, 100, false);
  const shifted = [..."world"].map((char, i) => ({
    col: 8 + i, char, width: 1, predicted: false, sourceCol: 7 + i,
  }));
  assert.deepEqual(engine.frame(), { row: 0, cells: [pred(7, "X"), ...shifted], cursorCol: 8 });
  view.setLine(0, "$ echo Xworld").moveTo(8);
  engine.serverParsed(view, 150);
  assert.equal(engine.frame(), null);
});

test("dim suggestion is not shifted", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  arm(ctx, "$ ec", 4, 0, "ho hi");
  engine.userInput("h", view, 100, false);
  assert.deepEqual(engine.frame(), { row: 0, cells: [pred(4, "h")], cursorCol: 5 });
});

test("backspace and arrows", () => {
  const ctx = setup();
  const { engine, view, spy } = ctx;
  arm(ctx, "$ abc", 5);
  engine.userInput("\x7f", view, 100, false);
  assert.deepEqual(engine.frame(), {
    row: 0,
    cells: [{ col: 4, char: " ", width: 1, predicted: false, sourceCol: 5 }],
    cursorCol: 4,
  });
  engine.userInput("\x1b[D", view, 101, false);
  assert.equal(engine.frame().cursorCol, 3);
  engine.userInput("\x1b[C", view, 102, false);
  assert.equal(engine.frame().cursorCol, 4);
  // 服务器：\b \b 删掉 c，再左移、右移
  view.setLine(0, "$ ab").moveTo(4);
  engine.serverParsed(view, 150);
  assert.equal(engine.frame(), null, "end state confirms all three");
  assert.equal(spy.misses, 0);

  engine.userInput("\b", view, 200, false);
  assert.equal(engine.frame().cursorCol, 3);
  engine.reset();

  // 应用光标键模式
  view.appCursorKeys = true;
  arm(ctx, "$ abc", 5, 300);
  engine.userInput("\x1bOD", view, 400, false);
  assert.deepEqual(engine.frame(), { row: 0, cells: [], cursorCol: 4 });
  engine.userInput("\x1b[D", view, 401, false);
  assert.equal(engine.frame(), null, "normal-mode sequence in app mode is not predictable");

  // 宽字符删两格；0 列不预测
  const wide = setup();
  arm(wide, "$ a", 3);
  wide.engine.userInput("中", wide.view, 100, false);
  assert.equal(wide.engine.frame().cursorCol, 5);
  wide.engine.userInput("\x7f", wide.view, 101, false);
  assert.equal(wide.engine.frame().cursorCol, 3, "wide char deletes two cells");
  wide.engine.userInput("\x7f", wide.view, 102, false);
  assert.equal(wide.engine.frame().cursorCol, 2);
  const zero = setup();
  arm(zero, "a", 1);
  zero.engine.userInput("\x1b[D", zero.view, 100, false);
  assert.equal(zero.engine.frame().cursorCol, 0);
  zero.engine.userInput("\x7f", zero.view, 101, false);
  assert.equal(zero.engine.frame(), null);
});

test("enter clears and re-arms first char rule", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  arm(ctx, "$ l", 3);
  engine.userInput("s", view, 100, false);
  engine.userInput("\r", view, 101, false);
  assert.equal(engine.frame(), null);
  assert.equal(engine.nextDeadline(), null);
  view.type("s").moveTo(0, 1).setLine(1, "$ ").moveTo(2, 1);
  engine.serverParsed(view, 150);
  engine.userInput("t", view, 200, false);
  assert.equal(engine.frame(), null);
});

test("password never shown", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  view.setLine(0, "Token ").moveTo(6, 0);
  engine.userInput("p", view, 0, false);
  view.setLine(3, "unrelated");
  engine.serverParsed(view, 50);
  for (let i = 0; i < 10; i += 1) {
    engine.userInput("abcdefghij"[i], view, 100 + i, false);
    assert.equal(engine.frame(), null);
    view.setLine(3, `noise ${i}`);
    engine.serverParsed(view, 110 + i);
  }
  assert.equal(ctx.frames.length, 0, "onFrame never called");
});

test("alternate screen starts tentative", () => {
  const ctx = setup();
  const { engine, view, spy } = ctx;
  arm(ctx, "$ l", 3);
  engine.userInput("s", view, 100, false);
  view.alternate = true;
  view.setLine(0, "").moveTo(0, 0);
  engine.serverParsed(view, 150);
  assert.equal(engine.frame(), null);
  assert.equal(spy.misses, 0, "screen switch is a reset, not a miss");

  arm(ctx, "x", 1, 200);
  engine.userInput("a", view, 300, false);
  engine.userInput("b", view, 301, false);
  assert.deepEqual(engine.frame().cells, [pred(1, "a")], "tentative: one char");
  engine.reset();
  arm(ctx, "x", 1, 400);
  engine.userInput("a", view, 500, false);
  view.type("a");
  engine.serverParsed(view, 550);
  engine.userInput("c", view, 600, false);
  engine.userInput("d", view, 601, false);
  assert.deepEqual(engine.frame().cells, [pred(2, "c"), pred(3, "d")]);
});

test("blocked input not predicted", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  arm(ctx, "$ l", 3);
  engine.userInput("s", view, 100, true);
  assert.equal(engine.frame(), null);
  view.atBottom = false;
  engine.userInput("s", view, 101, false);
  assert.equal(engine.frame(), null, "viewport scrolled up");
});

test("off mode still samples latency but never frames", () => {
  const ctx = setup("off");
  const { engine, view, gate, frames } = ctx;
  arm(ctx, "$ l", 3);
  engine.userInput("s", view, 100, false);
  view.type("s");
  engine.serverParsed(view, 180);
  assert.equal(frames.length, 0);
  assert.equal(gate.medianLatency(), 65);
  engine.setMode("auto");
  engine.userInput("x", view, 200, false);
  assert.deepEqual(engine.frame().cells, [pred(4, "x")], "auto enabled by sampled latency");
  engine.setMode("off");
  assert.equal(engine.frame(), null);
});

test("no pending keystrokes reads no cells", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  view.setLine(0, "$ ").moveTo(2, 0);
  view.cellCalls = 0;
  for (let i = 0; i < 100; i += 1) engine.serverParsed(view, i);
  assert.equal(view.cellCalls, 0);
});

test("cost is independent of output size", () => {
  const measure = (cols) => {
    const ctx = setup("always", cols);
    const { engine, view } = ctx;
    arm(ctx, "$ l", 3);
    for (const ch of "abcde") engine.userInput(ch, view, 100, false);
    view.cellCalls = 0;
    const start = performance.now();
    for (let i = 0; i < 10_000; i += 1) engine.serverParsed(view, 200);
    const elapsed = performance.now() - start;
    assert.equal(engine.frame().cells.length, 5);
    return { elapsed, calls: view.cellCalls };
  };
  const narrow = measure(40);
  const wide = measure(400);
  assert.ok(narrow.elapsed < 2000, `took ${narrow.elapsed}ms`);
  assert.ok(wide.elapsed < 2000, `took ${wide.elapsed}ms`);
  assert.equal(narrow.calls, wide.calls, "cell reads do not depend on line width");
  assert.ok(narrow.calls <= 10_000 * 2 * (5 + 1), `cell reads ${narrow.calls}`);
});

test("partial confirm with divergent rest is a miss", () => {
  const ctx = setup();
  const { engine, view, spy } = ctx;
  arm(ctx, "$ l", 3);
  engine.userInput("a", view, 100, false);
  engine.userInput("b", view, 100, false);
  view.type("ax");
  engine.serverParsed(view, 150);
  assert.equal(engine.frame(), null, "b must not be drawn over x");
  assert.equal(spy.misses, 1);
});

test("server moving to a new line clears the first-char mark", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  arm(ctx, "$ y", 3);
  // read -n1：回显 y 后服务器换行打印密码提示，用户没有按回车
  view.moveTo(0, 1).setLine(1, "Password: ").moveTo(10, 1);
  engine.serverParsed(view, 200);
  engine.userInput("s", view, 300, false);
  assert.equal(engine.frame(), null);
});

test("prompt printed on the same line clears the first-char mark", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  arm(ctx, "Continue? y", 11);
  view.setLine(0, "Continue? y Password: ").moveTo(22, 0);
  engine.serverParsed(view, 200);
  engine.userInput("s", view, 300, false);
  assert.equal(engine.frame(), null);
});

test("unrelated output with cursor in place keeps the mark", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  arm(ctx, "$ l", 3);
  view.setLine(2, "status");
  engine.serverParsed(view, 100);
  engine.userInput("s", view, 200, false);
  assert.deepEqual(engine.frame().cells, [pred(3, "s")]);
});

test("backspace with dim suggestion does not shift it", () => {
  const ctx = setup();
  const { engine, view, spy } = ctx;
  arm(ctx, "$ ls", 4, 0, " -la");
  engine.userInput("\x7f", view, 100, false);
  assert.deepEqual(engine.frame(), {
    row: 0,
    cells: [{ col: 3, char: " ", width: 1, predicted: false }],
    cursorCol: 3,
  });
  // zsh 重画：s 变成建议的一部分
  view.setLine(0, "$ l", "s -la").moveTo(3, 0);
  engine.serverParsed(view, 150);
  assert.equal(engine.frame(), null);
  assert.equal(spy.misses, 0);
});

test("IME commit of several chars is predicted as one group", () => {
  const ctx = setup();
  const { engine, view, spy } = ctx;
  arm(ctx, "$ l", 3);
  engine.userInput("中文", view, 100, false);
  assert.deepEqual(engine.frame(), {
    row: 0,
    cells: [pred(3, "中", 2), pred(5, "文", 2)],
    cursorCol: 7,
  });
  view.type("中");
  engine.serverParsed(view, 150);
  assert.deepEqual(engine.frame().cells, [pred(5, "文", 2)], "partial confirm per char");
  view.type("文");
  engine.serverParsed(view, 160);
  assert.equal(engine.frame(), null);
  assert.equal(spy.misses, 0);
});

test("long or mixed multi-char input is not predicted", () => {
  const long = setup();
  arm(long, "$ l", 3);
  long.engine.userInput("x".repeat(33), long.view, 100, false);
  assert.equal(long.engine.frame(), null);

  const mixed = setup();
  arm(mixed, "$ l", 3);
  mixed.engine.userInput("ab\r", mixed.view, 100, false);
  assert.equal(mixed.engine.frame(), null);
  mixed.engine.userInput("c", mixed.view, 101, false);
  assert.equal(mixed.engine.frame(), null, "mixed input clears the first-char mark");

  const edge = setup();
  arm(edge, "$ " + "x".repeat(34), 36);
  edge.engine.userInput("中文", edge.view, 100, false);
  assert.equal(edge.engine.frame(), null, "group would reach the line end");
});

test("new prompt at the same viewport position clears the mark", () => {
  // 底行 \r\n 打印同宽提示符：视口行列不变
  const q1 = setup();
  arm(q1, "? y", 3);
  q1.view.setLine(0, "pw ").moveTo(3, 0);
  q1.engine.serverParsed(q1.view, 200);
  q1.engine.userInput("s", q1.view, 300, false);
  assert.equal(q1.engine.frame(), null);

  // 滚屏后新行内容恰好相同：靠绝对行区分
  const scroll = setup();
  arm(scroll, "? y", 3);
  scroll.view.baseY += 1;
  scroll.engine.serverParsed(scroll.view, 200);
  scroll.engine.userInput("s", scroll.view, 300, false);
  assert.equal(scroll.engine.frame(), null);

  // \r\x1b[K 重画成同宽提示符
  const redraw = setup();
  arm(redraw, "$ y", 3);
  redraw.view.setLine(0, "pw:").moveTo(3, 0);
  redraw.engine.serverParsed(redraw.view, 200);
  redraw.engine.userInput("s", redraw.view, 300, false);
  assert.equal(redraw.engine.frame(), null);
});

test("rest check also runs before input when serverParsed was batched", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  arm(ctx, "$ y", 3);
  view.moveTo(0, 1).setLine(1, "Password: ").moveTo(10, 1);
  engine.userInput("s", view, 300, false);
  assert.equal(engine.frame(), null);
});

test("group as first input after a new prompt is not predicted", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  arm(ctx, "$ l", 3);
  view.moveTo(0, 1).setLine(1, "Password: ").moveTo(10, 1);
  engine.serverParsed(view, 200);
  engine.userInput("密码", view, 300, false);
  assert.equal(engine.frame(), null);
});

test("type-ahead survives an autosuggestion drawn after the first echo", () => {
  const ctx = setup();
  const { engine, view, spy } = ctx;
  arm(ctx, "$ l", 3);
  engine.userInput("a", view, 100, false);
  engine.userInput("b", view, 101, false);
  view.type("a").type("bcd", true).moveTo(4, 0);
  engine.serverParsed(view, 150);
  assert.deepEqual(engine.frame(), { row: 0, cells: [pred(4, "b")], cursorCol: 5 });
  assert.equal(spy.misses, 0);
  engine.serverParsed(view, 160);
  assert.deepEqual(engine.frame().cells, [pred(4, "b")], "kept on unchanged view");
  assert.equal(spy.misses, 0);
  view.moveTo(4, 0).type("b").type("cd", true).moveTo(5, 0);
  engine.serverParsed(view, 200);
  assert.equal(engine.frame(), null);
  assert.equal(spy.misses, 0);
});

test("IME group is not predicted in tentative mode", () => {
  const ctx = setup();
  const { engine, view, gate } = ctx;
  arm(ctx, "$ l", 3);
  gate.enterTentative();
  engine.userInput("中文字", view, 100, false);
  assert.equal(engine.frame(), null);
  // 采样：回显后本行仍确认，单字照常（试探）预测
  view.type("中文字");
  engine.serverParsed(view, 150);
  engine.userInput("a", view, 200, false);
  assert.deepEqual(engine.frame().cells, [pred(9, "a")]);
});

test("pending prediction is a miss when a same-width prompt replaces the line", () => {
  // R1：程序不回显，滚屏后在底行打印同宽提示符（视口行列不变，绝对行变了）
  const r1 = setup();
  arm(r1, "> a", 3);
  r1.engine.userInput("y", r1.view, 100, false);
  assert.deepEqual(r1.engine.frame().cells, [pred(3, "y")]);
  r1.view.baseY += 1;
  r1.view.setLine(0, "pw:").moveTo(3, 0);
  r1.engine.serverParsed(r1.view, 150);
  assert.equal(r1.engine.frame(), null);
  assert.equal(r1.spy.misses, 1);
  r1.engine.userInput("s", r1.view, 200, false);
  assert.equal(r1.engine.frame(), null, "password char not predicted");

  // R2：\r\x1b[K 同宽重画，不滚屏：队首左侧那格变了
  const r2 = setup();
  arm(r2, "> a", 3);
  r2.engine.userInput("y", r2.view, 100, false);
  r2.view.setLine(0, "pw:").moveTo(3, 0);
  r2.engine.serverParsed(r2.view, 150);
  assert.equal(r2.engine.frame(), null);
  assert.equal(r2.spy.misses, 1);
  r2.engine.userInput("s", r2.view, 200, false);
  assert.equal(r2.engine.frame(), null);
});

/**
 * 打字比回显快：按键间隔 interval、每个字 rtt 后回显（echo=false 时服务器从不回显，如密码）。
 * 返回每个字是否在回显前被画成预测，以及最终状态。
 */
function typeFast(mode, { interval, rtt, text = "abcdefghij", echo = true, prompt = "$ " } = {}) {
  const ctx = setup(mode);
  const { engine, view, spy } = ctx;
  view.setLine(0, prompt).moveTo(prompt.length, 0);
  const events = [];
  [...text].forEach((ch, i) => {
    events.push({ at: i * interval, kind: "key", ch, i });
    if (echo) events.push({ at: i * interval + rtt, kind: "echo", ch, i });
  });
  events.sort((a, b) => a.at - b.at || (a.kind === "echo" ? -1 : 1));
  const predicted = new Set();
  const echoed = new Set();
  const note = () => {
    for (const cell of engine.frame()?.cells ?? []) {
      if (!cell.predicted) continue;
      const i = cell.col - prompt.length;
      if (!echoed.has(i)) predicted.add(i);
    }
  };
  for (const e of events) {
    if (e.kind === "key") engine.userInput(e.ch, view, e.at, false);
    else {
      view.type(e.ch);
      echoed.add(e.i);
      engine.serverParsed(view, e.at);
    }
    note();
  }
  return { ctx, predicted, misses: spy.misses };
}

test("typing faster than echo: chars after the first echo are predicted", () => {
  for (const mode of ["always", "auto"]) {
    for (const [interval, rtt] of [[60, 150], [100, 150], [180, 300]]) {
      const { ctx, predicted, misses } = typeFast(mode, { interval, rtt });
      const label = `${mode} interval=${interval} rtt=${rtt}`;
      assert.equal(predicted.has(0), false, `${label}: first char is never predicted`);
      for (let i = 1; i < 10; i += 1) assert.ok(predicted.has(i), `${label}: char ${i} predicted`);
      assert.equal(misses, 0, label);
      assert.equal(ctx.engine.frame(), null, `${label}: all confirmed`);
      assert.equal(ctx.engine.nextDeadline(), null, label);
    }
  }
});

test("typing faster than echo: promoted chars are drawn when the first echo arrives", () => {
  const ctx = setup();
  const { engine, view } = ctx;
  view.setLine(0, "$ ").moveTo(2, 0);
  engine.userInput("a", view, 0, false);
  engine.userInput("b", view, 60, false);
  engine.userInput("c", view, 120, false);
  assert.equal(engine.frame(), null, "nothing confirmed yet");
  view.type("a");
  engine.serverParsed(view, 150);
  assert.deepEqual(engine.frame(), { row: 0, cells: [pred(3, "b"), pred(4, "c")], cursorCol: 5 });
  engine.userInput("d", view, 180, false);
  assert.deepEqual(engine.frame().cells, [pred(3, "b"), pred(4, "c"), pred(5, "d")], "new keys predicted normally");
  view.type("bcd");
  engine.serverParsed(view, 400);
  assert.equal(engine.frame(), null);
  assert.equal(ctx.spy.misses, 0);
});

test("typing faster than echo: password (no echo) is never predicted", () => {
  for (const mode of ["always", "auto"]) {
    const { ctx, predicted } = typeFast(mode, { interval: 60, rtt: 150, echo: false, prompt: "Password: " });
    assert.equal(predicted.size, 0, mode);
    assert.equal(ctx.frames.length, 0, `${mode}: onFrame never called`);
  }
});

test("typing faster than echo: tentative mode promotes at most one char", () => {
  const ctx = setup();
  const { engine, view, gate } = ctx;
  view.setLine(0, "$ ").moveTo(2, 0);
  gate.enterTentative();
  engine.userInput("a", view, 0, false);
  engine.userInput("b", view, 60, false);
  engine.userInput("c", view, 120, false);
  view.type("a");
  engine.serverParsed(view, 150);
  assert.deepEqual(engine.frame().cells, [pred(3, "b")]);
});

test("typing faster than echo: no promotion while scrolled up or after a control key", () => {
  const up = setup();
  up.view.setLine(0, "$ ").moveTo(2, 0);
  up.engine.userInput("a", up.view, 0, false);
  up.engine.userInput("b", up.view, 60, false);
  up.view.type("a");
  up.view.atBottom = false;
  up.engine.serverParsed(up.view, 150);
  assert.equal(up.engine.frame(), null, "viewport scrolled up");

  const shifted = setup();
  shifted.view.setLine(0, "$ ").moveTo(2, 0);
  shifted.engine.userInput("a", shifted.view, 0, false);
  shifted.engine.userInput("b", shifted.view, 60, false);
  shifted.engine.userInput("\x01", shifted.view, 70, false); // 控制键清空样本
  shifted.view.type("a");
  shifted.engine.serverParsed(shifted.view, 150);
  assert.equal(shifted.engine.frame(), null);
});

test("typing faster than echo: an IME group is promoted whole or not at all", () => {
  const ctx = setup("always", 12);
  const { engine, view } = ctx;
  view.setLine(0, "$ ").moveTo(2, 0);
  engine.userInput("a", view, 0, false);
  engine.userInput("中文", view, 60, false);
  engine.userInput("字", view, 70, false);
  // 宽 fits (cols 9–10) but 字 would wrap: the whole group stays unpredicted
  engine.userInput("宽字", view, 80, false);
  view.type("a");
  engine.serverParsed(view, 150);
  assert.deepEqual(engine.frame().cells, [pred(3, "中", 2), pred(5, "文", 2), pred(7, "字", 2)]);
});

test("typing faster than echo: a scrolled-in prompt matching a sample at the same viewport cell is not an echo", () => {
  // 回车后在底行抢先打字；服务器滚屏后在同一视口行打印恰好同字的提示符（例如密码提示）
  const ctx = setup();
  const { engine, view } = ctx;
  view.setLine(4, "$ run").moveTo(5, 4);
  engine.userInput("\r", view, 0, false);
  view.moveTo(0, 4);
  view.baseY += 1;
  view.setLine(4, "");
  engine.serverParsed(view, 20);
  engine.userInput("x", view, 40, false);
  engine.userInput("y", view, 100, false);
  engine.userInput("z", view, 160, false);
  view.baseY += 1;
  view.setLine(4, "x").moveTo(1, 4);
  engine.serverParsed(view, 200);
  assert.equal(engine.frame(), null, "nothing promoted");
  assert.equal(ctx.frames.length, 0);
  engine.userInput("s", view, 260, false);
  assert.equal(engine.frame(), null, "line not confirmed by a coincidental match");
});
