import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const source = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
const selectBody = source.match(/async function switchConversation\(id: string\) \{([\s\S]*?)\n\}/)?.[1];
const workspaceBody = source.match(/function switchTopWorkspaceForConversation\(conversation: AiConversation\) \{([\s\S]*?)\n\}/)?.[1];
assert.ok(selectBody);
assert.ok(workspaceBody);

function setup({ available = true, success = true, exists = true } = {}) {
  const events = [];
  const conversation = { id: "chat", workspaceId: "target", workspaceScopeId: "scope" };
  const workbench = { switchWorkspace(id) { events.push(["workspace", id]); return { id: "tab" }; } };
  const router = { push(route) { events.push(["route", route]); } };
  const switchWorkspace = new Function("workbench", "router", "workspaceTabRoute", "conversationWorkspaceAvailable", "conversation", workspaceBody)
    .bind(null, workbench, router, (tab) => tab.id, () => available);
  const assistant = {
    allConversations: exists ? [conversation] : [],
    async switchToConversation(id, scope) { events.push(["conversation", id, scope]); return success; },
  };
  const select = new Function("assistant", "switchTopWorkspaceForConversation", "restoreConversationScroll", `return async function(id) {${selectBody}}`)(
    assistant, switchWorkspace, async (id) => events.push(["scroll", id]),
  );
  return { select, events };
}

test("点击会话后切换所属工作区并导航到该工作区标签", async () => {
  const { select, events } = setup();
  await select("chat");
  assert.deepEqual(events, [["conversation", "chat", "scope"], ["workspace", "target"], ["route", "tab"], ["scroll", "chat"]]);
});

test("所属工作区不可用时仍可查看会话，不导航", async () => {
  const { select, events } = setup({ available: false });
  await select("chat");
  assert.deepEqual(events, [["conversation", "chat", "scope"], ["scroll", "chat"]]);
});

test("会话加载失败或不存在时不切换工作区", async () => {
  for (const options of [{ success: false }, { exists: false }]) {
    const { select, events } = setup(options);
    await select("chat");
    assert.ok(events.every(([event]) => event === "conversation"));
  }
});

const sidebarSource = await readFile(
  new URL("../src/components/ai/AiConversationSidebar.vue", import.meta.url),
  "utf8",
);

test("点击会话卡片任意非操作区域都会切换会话和工作区", () => {
  const cardOpening = sidebarSource.match(
    /<el-card[\s\S]*?class="ai-conversation-item"[\s\S]*?>/,
  )?.[0];
  const contentOpening = sidebarSource.match(
    /<button\s+type="button"\s+class="ai-conversation-item__content"[^>]*>/,
  )?.[0];
  assert.ok(cardOpening);
  assert.ok(contentOpening);
  assert.match(cardOpening, /@click="emit\('select', conversation\.id\)"/);
  assert.doesNotMatch(contentOpening, /@click=/);
  assert.match(
    sidebarSource,
    /class="ai-conversation-item__actions" @click\.stop/,
  );
  // 可用工作区随卡片点击切换；只有工作区失效时才显示重新关联按钮。
  assert.match(
    sidebarSource,
    /v-if="globalMode && !workspaceAvailable\(conversation\)"[\s\S]*?class="ai-conversation-item__workspace is-unavailable"[\s\S]*?@click\.stop="emit\('relinkWorkspace', conversation\)"/,
  );
  assert.doesNotMatch(
    sidebarSource,
    /:disabled="workspaceAvailable\(conversation\) && conversation\.workspaceId === topWorkspaceId"/,
  );
});
