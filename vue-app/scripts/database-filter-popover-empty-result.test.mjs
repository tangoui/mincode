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
const filtersSource = fs.readFileSync(
  path.join(appRoot, "src/composables/database/useTableFilters.ts"),
  "utf8",
);
const viewSource = fs.readFileSync(
  path.join(appRoot, "src/views/DatabaseWorkspaceView.vue"),
  "utf8",
);

test("empty table results retain metadata columns for adding filter conditions", () => {
  assert.match(
    viewSource,
    /const filterColumns = computed\(\(\) => \{[\s\S]*?\.\.\.resultColumns\.value,[\s\S]*?\.\.\.databaseStore\.tableColumns\.map\(\(column\) => column\.name\),[\s\S]*?\}\);/,
  );
  assert.match(viewSource, /:filter-columns="filterColumns"/);
  assert.match(
    filtersSource,
    /function addFilterCondition\(column = options\.filterColumns\.value\[0\] \?\? ""\)/,
  );
});

test("filter poppers stay inside the multi-filter popover", () => {
  const fieldSelect = toolbarSource.match(
    /<el-select\s+filterable\s+append-to="\.db-multi-filter-panel"[\s\S]*?<\/el-select>/,
  );
  assert.ok(fieldSelect, "字段选择器应关闭 teleport");
  assert.match(fieldSelect[0], /v-for="column in filterColumns"/);

  const selectTags = toolbarSource.match(/<el-select\b[^>]*>/g) ?? [];
  assert.ok(selectTags.length > 0);
  assert.ok(
    selectTags.every((tag) => tag.includes('append-to=".db-multi-filter-panel"')),
    "所有筛选下拉层都应挂载到父级 Popover 内",
  );

  const datePickerTags = toolbarSource.match(/<el-date-picker\b[^>]*>/g) ?? [];
  assert.ok(datePickerTags.length > 0);
  assert.ok(
    datePickerTags.every((tag) => tag.includes('append-to=".db-multi-filter-panel"')),
    "日期筛选浮层也应挂载到父级 Popover 内",
  );
});
