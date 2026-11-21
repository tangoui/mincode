import assert from "node:assert/strict";
import test from "node:test";
import { buildCellPalette, extendedAnsiColor, resolveCellColors } from "../src/domain/terminal/predictive-echo/cell-colors.ts";

/** IBufferCell 的颜色部分：mode = default | palette | rgb */
function cell({ fg = ["default"], bg = ["default"], inverse = false } = {}) {
  return {
    isInverse: () => (inverse ? 1 : 0),
    isFgDefault: () => fg[0] === "default",
    isFgPalette: () => fg[0] === "palette",
    isFgRGB: () => fg[0] === "rgb",
    getFgColor: () => (fg[0] === "default" ? 0 : fg[1]),
    isBgDefault: () => bg[0] === "default",
    isBgPalette: () => bg[0] === "palette",
    isBgRGB: () => bg[0] === "rgb",
    getBgColor: () => (bg[0] === "default" ? 0 : bg[1]),
  };
}

const palette = buildCellPalette({ red: "#ff0000", brightWhite: "#fefefe", foreground: "#cccccc" }, "#d4d4d4", "#1e1e1e");

test("palette: theme ansi, xterm defaults, cube and greys", () => {
  assert.equal(palette.ansi.length, 256);
  assert.equal(palette.ansi[1], "#ff0000", "theme red");
  assert.equal(palette.ansi[15], "#fefefe", "theme brightWhite");
  assert.equal(palette.ansi[0], "#2e3436", "xterm default black");
  assert.equal(palette.ansi[16], "#000000");
  assert.equal(palette.ansi[21], "#0000ff");
  assert.equal(palette.ansi[196], "#ff0000");
  assert.equal(palette.ansi[208], "#ff8700");
  assert.equal(palette.ansi[231], "#ffffff");
  assert.equal(palette.ansi[232], "#080808");
  assert.equal(palette.ansi[255], "#eeeeee");
  assert.equal(extendedAnsiColor(244), "#808080");
  assert.equal(palette.foreground, "#cccccc", "theme foreground wins");
  assert.equal(palette.background, "#1e1e1e", "fallback background");
  const extended = buildCellPalette({ extendedAnsi: ["#123456"] }, "#fff", "#000");
  assert.equal(extended.ansi[16], "#123456");
  assert.equal(extended.ansi[17], "#00005f");
});

test("default, palette and RGB colours", () => {
  assert.deepEqual(resolveCellColors(cell(), palette), { fg: "#cccccc", bg: "#1e1e1e" });
  assert.deepEqual(resolveCellColors(cell({ fg: ["palette", 1], bg: ["palette", 236] }), palette), { fg: "#ff0000", bg: "#303030" });
  assert.deepEqual(resolveCellColors(cell({ fg: ["rgb", 0x0a0b0c], bg: ["rgb", 0xff8800] }), palette), { fg: "#0a0b0c", bg: "#ff8800" });
});

test("inverse swaps the resolved colours", () => {
  assert.deepEqual(resolveCellColors(cell({ inverse: true }), palette), { fg: "#1e1e1e", bg: "#cccccc" });
  assert.deepEqual(resolveCellColors(cell({ fg: ["palette", 1], inverse: true }), palette), { fg: "#1e1e1e", bg: "#ff0000" });
});
