import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const [store, sidebar, drawer, styles, zh, en] = await Promise.all([
  readFile(new URL("../src/stores/ai-assistant.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ai/AiConversationSidebar.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/styles/workspaces/ai/drawer.css", import.meta.url), "utf8"),
  readFile(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/i18n/locales/en.ts", import.meta.url), "utf8"),
]);

test("会话未读状态使用单一字段，并只在读取时兼容旧完成未读字段", () => {
  assert.match(store, /unread\?: boolean/);
  assert.match(store, /type LegacyAiConversation = AiConversation & \{ completionUnread\?: boolean \}/);
  assert.match(
    store,
    /conversation\.unread = conversation\.unread === true \|\| legacyConversation\.completionUnread === true;[\s\S]*?delete legacyConversation\.completionUnread;/,
  );
  assert.match(store, /function persistedConversation\([\s\S]*?completionUnread: _legacyCompletionUnread/);
  assert.match(store, /function setConversationUnread\(id: string, unread: boolean\)/);
  assert.match(store, /conversation\.unread = unread/);
  assert.match(store, /markConversationDirty\(id, "meta"\)/);
  assert.match(store, /setConversationUnread,/);
});

test("打开会话自动标记已读，任务完成自动标记未读", () => {
  assert.match(
    store,
    /async function switchConversation\(id: string\)[\s\S]*?setConversationUnread\(id, false\)[\s\S]*?return true/,
  );
  assert.match(
    store,
    /previousStatus !== "completed"[\s\S]*?conversation\.unread = !conversationIsVisible\(id\)/,
  );
  assert.match(store, /function markConversationRead\(id: string\)/);
  assert.doesNotMatch(store, /markConversationCompletionRead/);
});

test("会话列表提供手动标记已读和未读入口", () => {
  assert.match(sidebar, /markRead: string; markUnread: string/);
  assert.match(sidebar, /readState: \[conversation: AiConversation, unread: boolean\]/);
  assert.match(sidebar, /class="ai-conversation-item__action-button ai-conversation-item__read-state-action"/);
  // 会话条目只有一行，标记已读/未读是悬停浮出的图标按钮，文字放在提示与无障碍名称里。
  assert.match(sidebar, /:aria-label="conversation\.unread \? labels\.markRead : labels\.markUnread"/);
  assert.match(sidebar, /@click\.stop="emit\('readState', conversation, !conversation\.unread\)"/);
  assert.match(styles, /\.ai-conversation-item__read-state-action\.el-button \{[\s\S]*?color: var\(--app-accent\)/);
  assert.match(drawer, /@read-state="setConversationReadState"/);
  assert.match(drawer, /assistant\.setConversationUnread\(conversation\.id, unread\)/);
  assert.match(zh, /"ai\.conversation\.markRead": "标记为已读"/);
  assert.match(zh, /"ai\.conversation\.markUnread": "标记为未读"/);
  assert.match(en, /"ai\.conversation\.markRead": "Mark as read"/);
  assert.match(en, /"ai\.conversation\.markUnread": "Mark as unread"/);
});

test("列表未读样式使用通用未读状态", () => {
  assert.match(sidebar, /'is-unread': conversation\.unread/);
  assert.match(sidebar, /v-if="conversation\.unread" class="ai-conversation-item__unread-dot"/);
});
