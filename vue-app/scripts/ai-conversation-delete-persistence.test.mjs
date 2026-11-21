import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";

const [store, drawer, persistence, settings] = await Promise.all([
  readFile(new URL("../src/stores/ai-assistant.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/services/ai-conversation-persistence.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/views/SettingsWorkspaceView.vue", import.meta.url), "utf8"),
]);

const deleteConversationBody = store.match(
  /async function deleteConversation\(id: string[^\n]*\) \{(?<body>[\s\S]*?)\n  \}\n  function clearConversationMessages/,
)?.groups?.body;

test("会话删除使用原子增量事务，最后一个会话使用原子替换", () => {
  assert.ok(deleteConversationBody, "deleteConversation implementation moved");
  assert.match(deleteConversationBody, /if \(scopeCleared\)/);
  assert.match(deleteConversationBody, /await replaceAiConversation\(/);
  assert.match(deleteConversationBody, /await deleteAiConversation\(/);
  assert.match(deleteConversationBody, /activeConversationId\.value/);
  assert.match(deleteConversationBody, /conversations\.value\.map\(\(conversation\) => conversation\.id\)/);
});

test("持久化失败时恢复会话并把错误交给界面提示", () => {
  assert.ok(deleteConversationBody, "deleteConversation implementation moved");
  assert.match(deleteConversationBody, /conversations\.value = previousConversations;/);
  assert.match(deleteConversationBody, /activeConversationId\.value = previousActiveConversationId;/);
  assert.match(deleteConversationBody, /throw error;/);
  assert.match(drawer, /await assistant\.deleteConversation\(conversation\.id, family\.slice\(1\)/);
  assert.match(drawer, /ai\.conversation\.deleteFailed/);
  assert.ok(
    drawer.indexOf("await assistant.deleteConversation(conversation.id,")
      < drawer.indexOf("await releaseDeletedConversation(item.conversation)"),
    "external ACP state must only be cancelled after durable deletion",
  );
});

test("删除事务期间暂停自动持久化，避免失败回滚被排队写入覆盖", () => {
  assert.ok(deleteConversationBody, "deleteConversation implementation moved");
  assert.match(deleteConversationBody, /pauseAutoPersistence\(\)/);
  assert.match(deleteConversationBody, /resumeAutoPersistence\(\)/);
  assert.match(store, /if \(autoPersistencePauseDepth > 0\) autoPersistenceDirty = true;/);
  assert.match(store, /dirtyConversations = new Map/);
});


test("设置页清空历史会删除 SQLite 历史、退役快照并刷新当前 AI 状态", () => {
  assert.match(persistence, /deleteAllAiHistory: true/);
  assert.match(persistence, /isRetiredAiHistoryStorageKey/);
  assert.match(store, /conversationHistoryRevision \+= 1/);
  assert.match(store, /workspaceConversationPreloadSequence \+= 1/);
  assert.match(store, /prefetchedWorkspaceConversation = undefined/);
  assert.match(store, /workspaceConversationLoads\.clear\(\)/);
  assert.match(store, /inactiveWorkspaceConversations\.clear\(\)/);
  assert.match(store, /await clearAllAiConversationHistory\(\);[\s\S]*?resetWorkspaceConversationState\(\)/);
  assert.match(settings, /await ElMessageBox\.confirm\(/);
  assert.match(settings, /await aiAssistant\.clearConversationHistory\(\)/);
  assert.doesNotMatch(settings, /app_db_danger_audit/);
  assert.doesNotMatch(settings, /settings\.data\.auditEmpty/);
});
