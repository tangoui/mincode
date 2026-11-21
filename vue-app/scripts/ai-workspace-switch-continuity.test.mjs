import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";

const [drawerSource, storeSource] = await Promise.all([
  readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/stores/ai-assistant.ts", import.meta.url), "utf8"),
]);

test("切换工作区不会主动取消正在运行的 AI 会话", () => {
  assert.doesNotMatch(drawerSource, /stopAgentsForWorkspaceSwitch/);
  assert.doesNotMatch(storeSource, /interruptRunningConversationsForWorkspaceSwitch/);
  assert.doesNotMatch(drawerSource, /assistant\.activeWorkspaceScopeId,\s*\n\s*\(scopeId, previousScopeId\)/);
  assert.match(drawerSource, /function stopAllAgents\(\)[\s\S]*?onBeforeUnmount/);
});

test("切换时先隔离目标工作区，读取不被旧工作区保存阻塞", () => {
  const switchBody = storeSource.match(
    /async function switchWorkspaceScope\(scopeId: string\) \{([\s\S]*?)\n  \}\n\s*\n  return \{/,
  )?.[1];
  assert.ok(switchBody, "switchWorkspaceScope implementation moved");
  assert.match(switchBody, /activeWorkspaceScopeId\.value = targetScopeId/);
  assert.match(switchBody, /const targetLoad = cachedTarget[\s\S]*?loadWorkspaceConversationState\(targetScopeId\)/);
  assert.match(switchBody, /activeWorkspaceScopeId\.value = targetScopeId[\s\S]*?resetWorkspaceConversationState\(\);[\s\S]*?await targetLoad!/);
  assert.doesNotMatch(
    switchBody,
    /await Promise\.all\(\[[\s\S]*?aiConversationPersistence\.save\(previousScopeId/,
  );
  assert.match(switchBody, /requestId !== workspaceSwitchSequence/);
  assert.match(switchBody, /previousWorkspaceLoading = workspaceConversationLoads\.has\(previousScopeId\)/);
  assert.match(storeSource, /void flushPersistence\(\)/);
  assert.doesNotMatch(switchBody, /aiConversationPersistence\.save\(/);
  assert.match(storeSource, /workspaceSwitching\.value = true;[\s\S]*?await targetLoad![\s\S]*?await switchConversation\(activeConversationId\.value\)[\s\S]*?workspaceSwitching\.value = false;/);
  assert.match(drawerSource, /<AiPromptComposer\s+v-if="!assistant\.workspaceSwitching && !activeParallelDelegationPending"/);
});

test("后台会话按工作区缓存并可在切回后恢复", () => {
  assert.match(storeSource, /inactiveWorkspaceConversations = new Map/);
  assert.match(storeSource, /cacheInactiveWorkspaceConversation\(previousScopeId, previousCollection\)/);
  assert.match(storeSource, /if \(cachedTarget\)[\s\S]*?conversations\.value = cachedTarget\.conversations/);
  assert.match(storeSource, /function conversationById[\s\S]*?inactiveWorkspaceConversations\.values/);
  assert.match(storeSource, /limits\.aiInactiveWorkspaceCacheCount/);
  assert.match(storeSource, /!entry\.running && !entry\.dirty/);
  assert.match(storeSource, /function pruneInactiveWorkspaceConversations\(\)/);
  assert.match(storeSource, /conversation\.running/);
});

test("工作区入口预取会话并复用在途或最近一次读取", () => {
  assert.match(storeSource, /function preloadWorkspaceScope\(scopeId: string\)/);
  assert.match(storeSource, /const existing = workspaceConversationLoads\.get\(scopeId\)/);
  assert.match(storeSource, /prefetchedWorkspaceConversation\?\.scopeId === scopeId/);
  assert.match(storeSource, /prefetchedWorkspaceConversation = \{ scopeId: targetScopeId, state \}/);
});

test("AI 任务固定使用启动时的工作区上下文和项目根目录", () => {
  assert.match(drawerSource, /interface ConversationWorkspaceContext[\s\S]*?workspaceId: string[\s\S]*?workspaceScopeId: string[\s\S]*?projectRoot: string/);
  assert.match(drawerSource, /workspaceContextForConversation\(conversationId\)/);
  assert.match(drawerSource, /workspaceId: context\.workspaceId[\s\S]*?workspaceScopeId: context\.workspaceScopeId/);
  assert.match(drawerSource, /call\.name === "terminal\.run_command" && projectRoot/);
  assert.match(drawerSource, /!parsedArgs\.cwd \|\| policy\.forceProjectRoot/);
  assert.match(drawerSource, /initialCwd: input\.initialCwd[\s\S]*?conversationWorkspaceContexts\.get\(input\.conversationId\)\?\.projectRoot/);
  assert.match(drawerSource, /loadMcpServerTools\(conversationId, runProjectRoot, mcpPlan\.preload\)/);
});
