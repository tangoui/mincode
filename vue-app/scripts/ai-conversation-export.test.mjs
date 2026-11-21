import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { conversationExportFileName, conversationToMarkdown } from "../src/domain/ai/conversation-export.ts";

const labels = { user: "用户", assistant: "AI", error: "请求失败", attachments: "附件", exportedAt: "导出时间" };

test("exports visible messages as Markdown with roles, times, errors and attachments", () => {
  const markdown = conversationToMarkdown({
    title: "部署问题",
    exportedAt: new Date(0),
    formatTime: (timestamp) => `T${timestamp}`,
    labels,
    messages: [
      { id: "u1", role: "user", content: "怎么重启？", createdAt: 1, attachments: [{ id: "a", name: "log.txt", kind: "text", mimeType: "text/plain", size: 1, data: "x" }] },
      { id: "e1", role: "assistant", content: "", eventAnchor: true },
      { id: "a1", role: "assistant", content: "运行 `systemctl restart nginx`。", createdAt: 2 },
      { id: "x1", role: "assistant", content: "429 Too Many Requests\nretry later", presentation: "error" },
    ],
  });
  assert.equal(markdown, [
    "# 部署问题",
    "",
    "> 导出时间: T0",
    "",
    "## 用户 · T1",
    "",
    "怎么重启？",
    "",
    "附件: log.txt",
    "",
    "## AI · T2",
    "",
    "运行 `systemctl restart nginx`。",
    "",
    "## AI",
    "",
    "> 请求失败: 429 Too Many Requests",
    "> retry later",
    "",
  ].join("\n"));
});

test("export file names drop characters no desktop OS allows", () => {
  assert.equal(conversationExportFileName('a/b:c*?"<>|\u0001 d'), "a b c d.md");
  assert.equal(conversationExportFileName("   "), "ai-conversation.md");
  assert.equal(conversationExportFileName("x".repeat(200)).length, 83);
});

test("the sidebar searches saved message text and exports conversations", async () => {
  const [drawer, sidebar] = await Promise.all([
    readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/components/ai/AiConversationSidebar.vue", import.meta.url), "utf8"),
  ]);
  assert.match(drawer, /const matches = await searchAiConversationMessages\(trimmed, scopeIds\)\.catch\(\(\) => \[\]\);\s*if \(sequence === messageSearchSequence\) messageMatches\.value = matches;/);
  assert.match(drawer, /const scopeIds = conversationPreferences\.globalMode \? \[\] : \[assistant\.activeWorkspaceScopeId\];/);
  assert.match(drawer, /async function exportConversation\(conversation: AiConversation\) \{\s*if \(!\(await assistant\.ensureConversationDetails\(conversation\.id\)\)\) \{/);
  assert.match(sidebar, /@click="emit\('open-message', match\)"/);
  assert.match(sidebar, /@click="emit\('export', conversation\)"/);
});
