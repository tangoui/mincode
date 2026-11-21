import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const read = (path) => readFile(new URL(path, import.meta.url), "utf8");
const [store, drawer, sidebar, app, taskButton, runNavigation, persistence, preferences, settings] = await Promise.all([
  read("../src/stores/ai-assistant.ts"),
  read("../src/components/ai/AiAssistantDrawer.vue"),
  read("../src/components/ai/AiConversationSidebar.vue"),
  read("../src/App.vue"),
  read("../src/components/layout/AiTaskProgressButton.vue"),
  read("../src/composables/use-agent-run-navigation.ts"),
  read("../src/services/ai-conversation-persistence.ts"),
  read("../src/stores/ai-conversation-preferences.ts"),
  read("../src/views/SettingsWorkspaceView.vue"),
]);

test("AI 会话范围偏好默认全局显示并保留已保存的按工作区选择", () => {
  assert.match(preferences, /scopeMode: record\.scopeMode === "workspace" \? "workspace" : "global"/);
  assert.match(preferences, /AI_CONVERSATION_PREFERENCES_KEY/);
  assert.match(preferences, /activeScopeId/);
  assert.match(store, /conversationPreferences\.activeScopeId \|\| context\.scopeId/);
  assert.match(store, /async function activateTopWorkspaceConversation/);
  assert.match(drawer, /conversation\.workspaceId === assistant\.topWorkspaceId/);
  assert.match(store, /conversationPreferences\.setActiveScopeId\(targetScopeId\)/);
  assert.match(store, /!restoredWorkspace && targetScopeId !== context\.scopeId[\s\S]*?targetScopeId = context\.scopeId/);
  assert.match(settings, /settings\.ai\.conversationScopeWorkspace/);
  assert.match(settings, /settings\.ai\.conversationScopeGlobal/);
});

test("全局模式只聚合轻量概览并按 scope 懒加载会话详情", () => {
  assert.match(persistence, /"ai_store_list_workspaces"/);
  assert.match(persistence, /"ai_store_move_conversation"/);
  assert.match(store, /const allConversations = computed/);
  assert.match(store, /listAiWorkspaceOverviews<AiConversationCollection>/);
  assert.match(store, /async function switchToConversation\(id: string, scopeId\?: string\)/);
  assert.match(store, /await switchWorkspaceScope\(targetScopeId\)/);
  assert.match(store, /return switchConversation\(id\)/);
  assert.match(store, /async function moveConversationToWorkspace/);
  assert.match(store, /conversations\.value = sourceRemaining/);
});

test("顶部工作区切换在全局模式不替换 AI 活动会话", () => {
  assert.match(app, /aiStore\.followTopWorkspace\(\{/);
  assert.match(store, /if \(conversationPreferences\.globalMode\) \{[\s\S]*?refreshGlobalConversationOverviews\(\);[\s\S]*?return;/);
  assert.match(taskButton, /navigation\.openRun\(run\)/);
  assert.match(runNavigation, /conversationPreferences\.globalMode && run\.navigation\.type === "ai-conversation"/);
  assert.match(runNavigation, /assistant\.switchToConversation/);
});

test("全局会话列表支持工作区筛选、标签和顶部工作区跳转", () => {
  assert.match(drawer, /conversationWorkspaceFilter/);
  assert.match(drawer, /conversationSearch/);
  assert.match(drawer, /assistant\.allConversations/);
  assert.match(sidebar, /v-if="globalMode"/);
  assert.match(sidebar, /workspaceOptions/);
  assert.match(sidebar, /class="ai-conversation-group__label">\{\{ group\.label \}\}<\/span>/);
  assert.match(
    drawer,
    /const groupConversationsByWorkspace = computed\([\s\S]*?conversationPreferences\.globalMode[\s\S]*?conversationWorkspaceFilter\.value === "all"/,
  );
  assert.match(drawer, /key: JSON\.stringify\(\[workspaceId, projectRoot\]\)/);
  assert.match(drawer, /label: workspaceGroup\?\.label \?\? conversationDateLabel\(date\)/);
  assert.match(drawer, /conversationPreferences\.workspaceGroupOrder\.map/);
  assert.match(sidebar, /@pointerdown="startGroupPointerDrag\(\$event, group\.key\)"/);
  assert.match(sidebar, /emit\("reorderWorkspaceGroups", moveWorkspaceGroupKey/);
  assert.match(sidebar, /workspaceAvailable\(conversation\)[\s\S]*?emit\('select', conversation\.id\)/);
  assert.match(sidebar, /emit\('relinkWorkspace', conversation\)/);
});

test("工具上下文固定使用会话持久化归属且拒绝已删除工作区", () => {
  assert.match(store, /workspaceId\?: string/);
  assert.match(store, /workspaceScopeId\?: string/);
  assert.match(store, /projectRoot\?: string/);
  assert.match(drawer, /workbench\.workspaces\.find\(\(item\) => item\.id === conversation\.workspaceId\)/);
  assert.match(drawer, /throw new Error\(t\("ai\.conversation\.workspace\.unavailable"\)\)/);
  assert.match(drawer, /"local_fs_file_info", \{[\s\S]*?projectRoot: context\.projectRoot[\s\S]*?filePath: "\."/);
  assert.match(drawer, /loadAiGitRepositories\(projectRoot\)/);
  assert.match(drawer, /\["workspace", "git"\]/);
  assert.match(drawer, /ACTIVE_WORKSPACE_UI_TOOL_NAMES/);
});
