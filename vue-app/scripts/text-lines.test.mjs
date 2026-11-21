import assert from "node:assert/strict";
import test from "node:test";
import { findMatches, indexRows, rowCount, rowOfOffset, rowText, scrollWindow } from "../src/domain/ui/text-lines.ts";

const rows = (text, columns) => {
  const index = indexRows(text, columns);
  return Array.from({ length: rowCount(index) }, (_, row) => [index.lines[row], rowText(text, index, row)]);
};

test("one row per line for \\n and \\r\\n, without the line break", () => {
  assert.deepEqual(rows("a\r\nbb\n\nccc", 80), [[0, "a"], [1, "bb"], [2, ""], [3, "ccc"]]);
});

test("a final newline ends the last line instead of adding an empty one", () => {
  assert.deepEqual(rows("x\ny\n", 80), [[0, "x"], [1, "y"]]);
});

test("empty text is one empty row", () => {
  assert.deepEqual(rows("", 80), [[0, ""]]);
});

test("a lone \\r stays text", () => {
  assert.deepEqual(rows("a\rb\nc", 80), [[0, "a\rb"], [1, "c"]]);
});

test("a line longer than the columns wraps into continuation rows of the same line", () => {
  assert.deepEqual(rows("abcdefgh\nxy", 3), [[0, "abc"], [0, "def"], [0, "gh"], [1, "xy"]]);
  const index = indexRows("abcdefgh\nxy", 3);
  assert.deepEqual([...index.continues].slice(0, 4), [0, 1, 1, 0]);
});

test("wrapping never splits a surrogate pair", () => {
  const text = "ab😀cd";
  assert.deepEqual(rows(text, 3).map(([, t]) => t), ["ab", "😀c", "d"]);
  assert.equal(rows(text, 3).map(([, t]) => t).join(""), text);
});

test("a 5 MB single-line body becomes screen-wide rows, not one 5 MB row", () => {
  const text = "x".repeat(5_000_000);
  const index = indexRows(text, 200);
  assert.equal(rowCount(index), 25_000);
  assert.equal(rowText(text, index, 24_999).length, 200);
});

test("indexes half a million lines into typed arrays", () => {
  const text = Array.from({ length: 500_000 }, (_, i) => `line ${i}`).join("\n");
  const index = indexRows(text, 120);
  assert.ok(index.starts instanceof Uint32Array && index.lines instanceof Uint32Array);
  assert.equal(rowCount(index), 500_000);
  assert.equal(rowText(text, index, 499_999), "line 499999");
});

test("source offsets of rows let a selection map back to the exact source", () => {
  const text = "ab\r\ncdef";
  const index = indexRows(text, 2);
  assert.deepEqual([...index.starts], [0, 4, 6]);
  assert.equal(text.slice(index.starts[0] + 1, index.starts[2] + 1), "b\r\ncde");
});

test("the window covers the viewport plus overscan and clamps to the content", () => {
  const w = (scrollTop, count = 1000, viewport = 200) => scrollWindow({ count, rowHeight: 20, scrollTop, viewportHeight: viewport, overscan: 5 });
  assert.deepEqual(w(400), { start: 15, end: 35, offset: 300, height: 20_000 });
  assert.deepEqual(w(0), { start: 0, end: 15, offset: 0, height: 20_000 });
  // 20 rows × 20 px scroll at most 200 px: a larger scrollTop is clamped to the bottom.
  assert.deepEqual(w(300, 20), { start: 5, end: 20, offset: 100, height: 400 });
  assert.deepEqual(w(0, 0), { start: 0, end: 0, offset: 0, height: 0 });
});

test("an unmeasured viewport still renders a first screen", () => {
  assert.deepEqual(scrollWindow({ count: 1000, rowHeight: 20, scrollTop: 0, viewportHeight: 0, overscan: 5 }), { start: 0, end: 60, offset: 0, height: 20_000 });
});

test("content taller than the maximum scroll height is scaled and still reaches the last row", () => {
  const input = { count: 10_000_000, rowHeight: 20, viewportHeight: 400, overscan: 0, maxHeight: 1_000_000 };
  const top = scrollWindow({ ...input, scrollTop: 0 });
  assert.equal(top.height, 1_000_000);
  assert.deepEqual([top.start, top.offset], [0, 0]);
  const bottom = scrollWindow({ ...input, scrollTop: 1_000_000 - 400 });
  assert.equal(bottom.end, 10_000_000);
  // The rendered block sits so its last row ends at the viewport's bottom.
  assert.equal(bottom.offset + (bottom.end - bottom.start) * 20, 1_000_000);
  const middle = scrollWindow({ ...input, scrollTop: (1_000_000 - 400) / 2 });
  assert.ok(Math.abs(middle.start - 5_000_000) < 20);
});


test("finds every match case-insensitively as source offsets", () => {
  assert.deepEqual(findMatches("Foo foo FOO bar", "foo"), [0, 4, 8]);
  assert.deepEqual(findMatches("aaaa", "aa"), [0, 2]);
  assert.deepEqual(findMatches("abc", ""), []);
  assert.deepEqual(findMatches("abc", "x"), []);
});

test("stops at the match limit", () => {
  assert.equal(findMatches("a".repeat(100), "a", 10).length, 10);
});

test("searches a 5 MB body quickly enough to run on input", () => {
  const text = `${"x".repeat(5_000_000)}needle`;
  const started = performance.now();
  assert.deepEqual(findMatches(text, "NEEDLE"), [5_000_000]);
  assert.ok(performance.now() - started < 500);
});

test("maps a source offset to the row that shows it, wrapped rows included", () => {
  const text = "abcdefgh\nxy";
  const index = indexRows(text, 3);
  assert.deepEqual([0, 2, 3, 7, 9, 10].map((offset) => rowOfOffset(index, offset)), [0, 0, 1, 2, 3, 3]);
});
