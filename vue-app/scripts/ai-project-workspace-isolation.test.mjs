import assert from "node:assert/strict";
import { readSourceSync as readFileSync } from "./read-source.mjs";
import test from "node:test";
const source = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const drawer = source("../src/components/ai/AiAssistantDrawer.vue");
const tools = source("../src/services/ai-tools/workspace-tools.ts");
const store = source("../src/stores/local-project.ts");

test("项目列表从会话 scope 获取，不使用当前界面项目列表", () => {
  assert.match(drawer, /projectsForWorkspace\(conversation\.workspaceScopeId \|\| ""\)/);
  assert.doesNotMatch(drawer, /listSavedProjects: \(\) => localProject\.projects/);
  assert.match(store, /persistedWorkspaces\[normalized\]\?\.projects \?\? \[\]/);
});
test("索引执行传递会话 ID，空 roots 不得触发全局索引搜索", () => {
  assert.match(tools, /listSavedProjects\?\.\(conversationId\)/);
  assert.match(tools, /if \(!saved\.length\) throw new Error/);
  assert.match(tools, /if \(!requested\.length\) throw new Error/);
  for (const name of ["refresh_file_index", "search_file_index"]) {
    assert.ok(tools.includes(`"workspace.${name}",\n                  conversationId,`));
  }
});
test("私有项目 UI 执行校验会话工作区及目标根目录", () => {
  assert.match(drawer, /owner\.id !== workbench\.activeWorkspaceId/);
  assert.match(drawer, /requestedRoots\.some\(\(root\) => !allowedRoots\.includes\(root\)\)/);
});
