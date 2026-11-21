import assert from "node:assert/strict";
import test from "node:test";
import { rowWindow, ROW_WINDOW_THRESHOLD } from "../src/domain/ui/row-window.ts";

const row = 29;

test("small groups render every row", () => {
  assert.deepEqual(rowWindow({ count: 40, groupTop: 0, rowHeight: row, scrollTop: 0, viewportHeight: 600 }), { start: 0, end: 40, paddingTop: 0, paddingBottom: 0 });
  assert.equal(ROW_WINDOW_THRESHOLD, 200);
});

test("a large group renders only rows near the viewport, with spacers keeping its height", () => {
  const window = rowWindow({ count: 2000, groupTop: 100, rowHeight: row, scrollTop: 0, viewportHeight: 600 });
  assert.equal(window.start, 0);
  assert.ok(window.end > 600 / row && window.end < 120, `end ${window.end}`);
  assert.equal(window.paddingTop, 0);
  assert.equal(window.paddingBottom, (2000 - window.end) * row);
});

test("scrolling into the middle of a group moves the window, offset by where the group starts", () => {
  const window = rowWindow({ count: 2000, groupTop: 100, rowHeight: row, scrollTop: 100 + 1000 * row, viewportHeight: 600 });
  assert.ok(window.start < 1000 && window.start > 900, `start ${window.start}`);
  assert.ok(window.end > 1000 + 600 / row && window.end < 1100, `end ${window.end}`);
  assert.equal(window.paddingTop, window.start * row);
  assert.equal(window.start * row + (window.end - window.start) * row + window.paddingBottom, 2000 * row);
});

test("a group entirely off screen renders no rows; spacers keep its full height", () => {
  const below = rowWindow({ count: 2000, groupTop: 50_000, rowHeight: row, scrollTop: 0, viewportHeight: 600 });
  assert.deepEqual(below, { start: 0, end: 0, paddingTop: 0, paddingBottom: 2000 * row });
  const above = rowWindow({ count: 2000, groupTop: 0, rowHeight: row, scrollTop: 200_000, viewportHeight: 600 });
  assert.deepEqual(above, { start: 2000, end: 2000, paddingTop: 2000 * row, paddingBottom: 0 });
});

test("threshold 0 windows even a short group", () => {
  const window = rowWindow({ count: 50, groupTop: 10_000, rowHeight: row, scrollTop: 0, viewportHeight: 600, threshold: 0 });
  assert.equal(window.end - window.start, 0);
});

test("an unknown viewport (before layout) mounts a bounded window", () => {
  const window = rowWindow({ count: 3000, groupTop: 0, rowHeight: row, scrollTop: 0, viewportHeight: 0 });
  assert.ok(window.end <= 100, `end ${window.end}`);
});
