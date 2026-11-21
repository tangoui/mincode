import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const view = new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url);
const zh = new URL("../src/i18n/locales/zh.ts", import.meta.url);
const en = new URL("../src/i18n/locales/en.ts", import.meta.url);

test("提交图谱在未探测到仓库时给出可见提示而不是空白抽屉", async () => {
  const source = await readFile(view, "utf8");
  // 抽屉内容不再整体依赖 repoPath：缺少仓库时渲染空状态。
  assert.match(source, /<el-empty v-if="!gitInspectorRepoPath" :description="t\('git\.graph\.noRepo'\)" \/>/);
  // 打开图谱时用当前项目目录兜底解析，避免 Git 面板尚未就绪就显示空白。
  assert.match(source, /async function loadGitGraph\(\)[\s\S]*?resolveOwningRepo\(project\.projectRoot\)/);
  // 兜底失败时清空列表而不是保留上一次仓库的记录。
  assert.match(source, /if \(!gitInspectorRepoPath\.value\) \{\r?\n\s+gitGraphLogs\.value = \[\];\r?\n\s+return;\r?\n\s+\}/);
});

test("空状态文案中英文都提供", async () => {
  for (const url of [zh, en]) {
    const locale = await readFile(url, "utf8");
    assert.match(locale, /"git\.graph\.noRepo": ".+"/);
  }
});
