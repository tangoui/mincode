import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const [store, sidebar, drawer, progressButton, runNavigation, styles, zh] = await Promise.all([
  readFile(new URL("../src/stores/ai-assistant.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ai/AiConversationSidebar.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/layout/AiTaskProgressButton.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/composables/use-agent-run-navigation.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/styles/workspaces/ai/drawer.css", import.meta.url), "utf8"),
  readFile(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8"),
]);

test("后台或不可见时完成的会话会持久化未读状态", () => {
  assert.match(store, /unread\?: boolean/);
  assert.match(
    store,
    /previousStatus !== "completed"[\s\S]*?conversation\.unread = !conversationIsVisible\(id\)/,
  );
  assert.match(
    store,
    /function conversationIsVisible\(id: string\)[\s\S]*?open\.value[\s\S]*?document\.visibilityState === "visible"[\s\S]*?document\.hasFocus\(\)/,
  );
});

test("打开目标会话后清除未读提醒", () => {
  assert.match(store, /function markConversationRead\(id: string\)/);
  assert.match(store, /!conversation\?\.unread \|\| !conversationIsVisible\(id\)/);
  assert.match(
    store,
    /async function switchConversation\(id: string\)[\s\S]*?setConversationUnread\(id, false\)[\s\S]*?return true/,
  );
  assert.match(drawer, /window\.addEventListener\("focus", markVisibleConversationRead\)/);
  assert.match(drawer, /document\.addEventListener\("visibilitychange", markVisibleConversationRead\)/);
  assert.match(progressButton, /const navigation = useAgentRunNavigation\(\)[\s\S]*?await navigation\.openRun\(run\)/);
  assert.match(
    runNavigation,
    /await assistant\.switchToConversation\([\s\S]*?run\.navigation\.conversationId,[\s\S]*?run\.workspaceScopeId,?\s*\)[\s\S]*?assistant\.show\(/,
  );
});

test("会话列表突出显示已完成会话的未读状态", () => {
  assert.match(sidebar, /'is-unread': conversation\.unread/);
  assert.match(sidebar, /conversation\.status === "completed" \? labels\.unreadCompleted : labels\.unread/);
  assert.match(sidebar, /ai-conversation-item__unread-dot/);
  assert.match(drawer, /ai\.conversation\.unreadCompleted/);
  assert.match(styles, /\.ai-conversation-item\.is-unread/);
  assert.match(styles, /\.ai-conversation-item__meta \{[\s\S]*?min-height: 20px/);
  assert.match(styles, /\.ai-conversation-item__meta \.el-tag \{[\s\S]*?display: inline-flex;[\s\S]*?height: 20px;[\s\S]*?align-items: center/);
  assert.match(styles, /\.ai-conversation-item__meta \.el-tag__content \{[\s\S]*?line-height: 18px/);
  assert.match(zh, /"ai\.conversation\.unreadCompleted": "未读完成"/);
});
