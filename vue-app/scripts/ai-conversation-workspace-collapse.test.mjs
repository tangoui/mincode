import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const [drawer, sidebar, styles] = await Promise.all([
  readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ai/AiConversationSidebar.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/styles/workspaces/ai/drawer.css", import.meta.url), "utf8"),
]);

test("会话工作区分组支持折叠并暴露可访问状态", () => {
  assert.match(sidebar, /const collapsedGroups = ref<Record<string, boolean>>\(\{\}\)/);
  assert.match(sidebar, /function toggleGroup\(key: string\)/);
  assert.match(sidebar, /class="ai-conversation-group__toggle"/);
  assert.match(sidebar, /:aria-expanded="!isGroupCollapsed\(group\.key\)"/);
  assert.match(sidebar, /@click="toggleGroup\(group\.key\)"/);
  assert.match(sidebar, /v-if="!isGroupCollapsed\(group\.key\)"/);
});

test("会话工作区分组使用克制的强调色而非紫色渐变图标", () => {
  assert.match(styles, /\.ai-conversation-group__toggle \{[\s\S]*?color: var\(--app-accent\)/);
  assert.match(styles, /\.ai-conversation-group__toggle \{[\s\S]*?background: color-mix\(/);
  const iconStyles = styles.match(/\.ai-conversation-group__icon \{[^}]+\}/)?.[0] ?? "";
  // 图标只用分组强调色着色，不再加底色（1882416cd 起为透明背景）。
  assert.match(iconStyles, /background: (?:transparent|color-mix\()/);
  assert.match(iconStyles, /color: color-mix\(/);
  assert.doesNotMatch(iconStyles, /linear-gradient\(/);
  assert.match(styles, /\.ai-conversation-group__count \{[\s\S]*?color: var\(--app-accent\)/);
});

test("会话工作区颜色由稳定标识固定映射到柔和色板", () => {
  assert.match(drawer, /function workspaceGroupTone\(identity: string\)/);
  assert.match(drawer, /tone: workspaceGroupTone\(workspaceId \|\| projectRoot \|\| workspaceName\)/);
  assert.match(sidebar, /group\.tone !== undefined \? `is-tone-\$\{group\.tone\}` : ''/);
  assert.equal((styles.match(/\.ai-conversation-group\.is-tone-\d/g) ?? []).length, 8);
  assert.match(styles, /--ai-conversation-group-accent: var\(--app-accent\)/);
});

test("直接按住工作区分组标题拖动排序，拖动后不误触折叠", () => {
  assert.doesNotMatch(sidebar, /ai-conversation-group__drag-handle/);
  assert.doesNotMatch(styles, /ai-conversation-group__drag-handle/);
  const toggle = sidebar.match(/<button\s+type="button"\s+class="ai-conversation-group__toggle"[\s\S]*?>/)?.[0] ?? "";
  assert.match(toggle, /@click="toggleGroup\(group\.key\)"/);
  assert.match(toggle, /@pointerdown="startGroupPointerDrag\(\$event, group\.key\)"/);
  assert.match(toggle, /@lostpointercapture="cancelGroupPointerDrag"/);
  assert.match(sidebar, /if \(!props\.workspaceGroupOrderable \|\| event\.button !== 0\) return;/);
  assert.match(sidebar, /handle\.setPointerCapture\(event\.pointerId\)/);
  assert.match(sidebar, /resolveWorkspaceGroupDropTarget\(/);
  assert.match(sidebar, /function toggleGroup\(key: string\) \{[\s\S]*?if \(suppressToggleClick\) \{[\s\S]*?return;/);
  assert.match(sidebar, /if \(sourceKey\) \{\s*suppressToggleClick = true;/);
  assert.match(styles, /\.ai-conversation-group__toggle\.is-orderable \{[^}]*touch-action: none/);
});

test("会话列表边距不漏到卡片底部的操作行", () => {
  assert.match(styles, /\.ai-conversation-sidebar__list > \.el-scrollbar__wrap > \.el-scrollbar__view \{\s*padding: 8px 10px 14px;/);
  assert.doesNotMatch(styles, /\.ai-conversation-sidebar__list \.el-scrollbar__view \{/);
});
