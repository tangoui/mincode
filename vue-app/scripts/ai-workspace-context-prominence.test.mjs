import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const read = (path) => readFile(new URL(path, import.meta.url), "utf8");
const [drawer, styles, zh, en] = await Promise.all([
  read("../src/components/ai/AiAssistantDrawer.vue"),
  read("../src/styles/workspaces/ai/drawer.css"),
  read("../src/i18n/locales/zh.ts"),
  read("../src/i18n/locales/en.ts"),
]);

test("全局会话在顶部工具栏紧凑显示当前工作区", () => {
  assert.match(drawer, /<el-icon><FolderOpened \/><\/el-icon>/);
  assert.match(drawer, /t\("ai\.conversation\.workspace\.contextLabel"\)/);
  assert.match(drawer, /<header class="ai-assistant-header">[\s\S]*?class="ai-assistant-workspace-context"[\s\S]*?class="ai-assistant-header__spacer"/);
  assert.doesNotMatch(drawer, /<\/header>\s*<div[^>]+class="ai-assistant-workspace-context"/);
  assert.match(drawer, /class="ai-assistant-workspace-context__name"[\s\S]*?activeConversationWorkspaceName/);
  assert.match(styles, /\.ai-assistant-workspace-context \{[\s\S]*?max-width: min\(320px, 30vw\)/);
  assert.match(styles, /\.ai-assistant-workspace-context \{[\s\S]*?height: 28px;/);
  const contextIconStyles = styles.match(/\.ai-assistant-workspace-context__icon \{[^}]+\}/)?.[0] ?? "";
  assert.match(contextIconStyles, /background: color-mix\(/);
  assert.match(contextIconStyles, /color: color-mix\(/);
  assert.doesNotMatch(contextIconStyles, /background: var\(--app-accent\)/);
  assert.match(styles, /@container ai-assistant-header \(max-width: 760px\)[\s\S]*?\.ai-assistant-workspace-context__label \{[\s\S]*?display: none/);
  assert.match(styles, /\.ai-assistant-workspace-context__name \{[\s\S]*?text-overflow: ellipsis;[\s\S]*?font-weight: 700/);
  assert.match(zh, /"ai\.conversation\.workspace\.contextLabel": "当前工作区"/);
  assert.match(en, /"ai\.conversation\.workspace\.contextLabel": "Current workspace"/);
});
