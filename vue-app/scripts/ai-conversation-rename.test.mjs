import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const sidebarPath = new URL("../src/components/ai/AiConversationSidebar.vue", import.meta.url);
const drawerPath = new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url);
const stylePath = new URL("../src/styles/workspaces/ai/drawer.css", import.meta.url);

test("AI 会话列表提供可见的重命名入口并提交到 store", async () => {
  const [sidebar, drawer] = await Promise.all([
    readFile(sidebarPath, "utf8"),
    readFile(drawerPath, "utf8"),
  ]);

  assert.match(
    sidebar,
    /:icon="EditPen"[\s\S]*?:aria-label="labels\.rename"[\s\S]*?@click="startRenaming\(conversation\)"/,
  );
  assert.match(sidebar, /@keydown\.enter="handleRenameEnter\(\$event, conversation\)"/);
  assert.match(sidebar, /function handleRenameEnter[\s\S]*?isImeComposing\(event\)[\s\S]*?submitRename\(conversation\)/);
  assert.match(sidebar, /return event\.isComposing \|\| event\.keyCode === 229;/);
  assert.match(drawer, /@rename="renameConversation"/);
  assert.match(drawer, /assistant\.renameConversation\(conversation\.id, title\)/);
});

test("AI 会话操作区能完整容纳重命名等六个按钮", async () => {
  const style = await readFile(stylePath, "utf8");

  assert.match(
    style,
    /\.ai-conversation-item__action-button\.el-button\s*\{[\s\S]*?flex:\s*0 0 22px;/,
  );
  assert.match(
    style,
    /\.ai-conversation-item__action-button\.el-button\s*\{[\s\S]*?margin:\s*0;/,
  );
});
