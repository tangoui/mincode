import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const workspaceView = await readFile(
  new URL("../src/views/DatabaseWorkspaceView.vue", import.meta.url),
  "utf8"
);

test("保存当前表查询时默认脚本名只使用表名", () => {
  assert.match(
    workspaceView,
    /inputValue:\s*selectedTable\.value\s*\?\s*selectedTable\.value\s*:\s*t\("database\.defaultScriptName"\)/s
  );
  assert.doesNotMatch(workspaceView, /t\("database\.tableQueryName"/);
});

test("点击保存的脚本复用同名同 SQL 的查询 tab，否则新建", () => {
  assert.match(
    workspaceView,
    /function openScript\(script: DatabaseSavedScript\)\s*\{[\s\S]*?tab\.name === script\.name && tab\.sql === script\.sql[\s\S]*?newQuery\(script\.sql, script\.name\);\s*\}/s
  );
});

test("已有脚本名称隐藏自动生成的查询后缀", async () => {
  const sidebar = await readFile(
    new URL("../src/components/database/sidebar/DatabaseConnectionSidebar.vue", import.meta.url),
    "utf8"
  );

  assert.match(sidebar, /displaySavedScriptName\(script\.name\)/);
  assert.match(sidebar, /suffixes\s*=\s*\[" 查询", " query"\]/);
});
