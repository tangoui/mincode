import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const appRoot = path.resolve(scriptDir, "..");
const toolbarSource = fs.readFileSync(
  path.join(appRoot, "src/components/database/results/DatabaseGridToolbar.vue"),
  "utf8",
);
const gridStyles = fs.readFileSync(
  path.join(appRoot, "src/styles/workspaces/database/result-grid.css"),
  "utf8",
);

test("column picker keeps its header outside the independently scrolling field list", () => {
  const pickerMatch = toolbarSource.match(
    /<div class="db-column-picker">([\s\S]*?)<\/div>\s*<\/el-popover>/,
  );

  assert.ok(pickerMatch, "字段选择器应使用独立外层容器");
  const picker = pickerMatch[1];
  const headerIndex = picker.indexOf('class="db-column-picker__header"');
  const scrollbarIndex = picker.indexOf('class="db-column-picker__list"');

  assert.ok(headerIndex >= 0, "字段选择器标题应始终存在");
  assert.ok(scrollbarIndex > headerIndex, "标题应固定在字段滚动列表上方");
  assert.match(
    picker,
    /<el-scrollbar class="db-column-picker__list" max-height="min\(50vh, 280px\)">[\s\S]*?v-for="column in resultColumns"/,
    "字段项应位于受视口高度约束的滚动列表内",
  );
});

test("column picker styles preserve the viewport cap and long-name truncation", () => {
  assert.match(
    gridStyles,
    /\.db-column-picker\s*\{[\s\S]*?max-height:\s*min\(60vh, 340px\);[\s\S]*?flex-direction:\s*column;/,
  );
  assert.match(
    gridStyles,
    /\.db-column-picker__list\s*\{[\s\S]*?min-height:\s*0;/,
  );
  assert.match(
    gridStyles,
    /\.db-column-picker \.el-checkbox__label\s*\{[\s\S]*?text-overflow:\s*ellipsis;[\s\S]*?white-space:\s*nowrap;/,
  );
});
