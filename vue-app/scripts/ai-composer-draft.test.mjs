import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const [store, drawer] = await Promise.all([
  readFile(new URL("../src/stores/ai-assistant.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8"),
]);

test("待发送附件随会话快照保存并恢复", () => {
  assert.match(store, /draftAttachments\?: AiAttachment\[];/);
  assert.match(store, /draftAttachments: draftAttachments\.value\.map\(compactAiAttachment\)/);
  assert.match(
    store,
    /draftAttachments\.value = Array\.isArray\(snapshotValue\.draftAttachments\)[\s\S]*?sanitizeAiAttachments\(snapshotValue\.draftAttachments\)/,
  );
  assert.match(store, /function setDraftAttachments[\s\S]*?markConversationDirty\(activeConversationId\.value, "meta"\)/);
  assert.match(store, /draftAttachments: draftAttachments\.value\.map\(compactAiAttachment\)/);
});

test("编辑器附件直接绑定当前会话草稿，切换会话不再清空", () => {
  assert.match(
    drawer,
    /const attachments = computed\(\{[\s\S]*?get: \(\) => assistant\.draftAttachments,[\s\S]*?assistant\.setDraftAttachments\(value\)/,
  );
  const switchBody = drawer.match(
    /async function switchConversation\(id: string\) \{(?<body>[\s\S]*?)\n  \}/,
  )?.groups?.body;
  assert.ok(switchBody, "switchConversation implementation moved");
  assert.doesNotMatch(switchBody, /attachments\.value = \[]/);
});

test("发送和手动删除仍清理当前会话附件草稿", () => {
  assert.match(drawer, /function removeAttachment\(id: string\)[\s\S]*?attachments\.value = attachments\.value\.filter/);
  assert.match(drawer, /function takeAttachments\(\)[\s\S]*?attachments\.value = \[]/);
});
