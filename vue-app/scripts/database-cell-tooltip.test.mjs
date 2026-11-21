import assert from "node:assert/strict";
import test from "node:test";
import {
  DATABASE_CELL_TOOLTIP_MAX_LENGTH,
  formatDatabaseCellTooltip,
} from "../src/domain/database/cell-tooltip.ts";

const formatCell = (value) => (typeof value === "object" ? JSON.stringify(value) : String(value));

test("formats serialized JSON cell values as readable, decoded JSON", () => {
  const tooltip = formatDatabaseCellTooltip(
    String.raw`[{"description":"Create new skills","note":"\u77e5\u8bc6"}]`,
    formatCell
  );

  assert.ok(tooltip);
  assert.match(tooltip, /\n  \{/);
  assert.match(tooltip, /知识/);
  assert.doesNotMatch(tooltip, /\\u77e5/);
});

test("does not add hover tooltips for null or compact scalar values", () => {
  assert.equal(formatDatabaseCellTooltip(null, formatCell), undefined);
  assert.equal(formatDatabaseCellTooltip("ready", formatCell), undefined);
});

test("bounds overly long cell detail without splitting Unicode characters", () => {
  const tooltip = formatDatabaseCellTooltip("知".repeat(DATABASE_CELL_TOOLTIP_MAX_LENGTH + 50), formatCell);

  assert.ok(tooltip);
  assert.equal(Array.from(tooltip).length, DATABASE_CELL_TOOLTIP_MAX_LENGTH);
  assert.equal(tooltip.at(-1), "…");
});
