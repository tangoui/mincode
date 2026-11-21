import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

function readSource(path) {
  return readFile(new URL(`../${path}`, import.meta.url), "utf8");
}

// 正在生成回复时，附件只进入下一条消息的草稿，不应禁用文件选择器。
test("the attachment picker stays usable while a conversation is running", async () => {
  const composer = await readSource("src/components/ai/AiPromptComposer.vue");
  const start = composer.indexOf(':icon="Paperclip"');
  assert.ok(start > 0, "attachment picker button not found");
  const end = composer.indexOf("/>", start);
  const picker = composer.slice(start, end);

  assert.ok(
    !/:disabled="loading"/.test(picker),
    "the attachment picker must not be disabled while loading",
  );
  assert.match(picker, /:loading="addingAttachments"/);
  assert.match(picker, /@click="attachmentInput\?\.click\(\)"/);
});

// 文件读取结果仍写入会话草稿；发送时才截取附件，运行中的请求不会被改写。
test("attachments selected while running remain in the draft until the next send", async () => {
  const drawer = await readSource("src/components/ai/AiAssistantDrawer.vue");

  assert.match(drawer, /get: \(\) => assistant\.draftAttachments/);
  assert.match(drawer, /appendDraftAttachments\(result\.attachments\)/);
  assert.match(drawer, /function appendDraftAttachments[\s\S]*?attachments\.value = \[\.\.\.attachments\.value, \.\.\.added\]/);
  assert.match(
    drawer,
    /const selectedAttachments = sanitizeAiAttachments\(request\?\.attachments \?\? attachments\.value\);/,
  );
  assert.match(drawer, /if \(targetConversation\?\.running && !request\) \{[\s\S]*?consumeComposerInput\(text, selectedAttachments\)/);
});

test("image attachments use Element Plus image and compact close controls", async () => {
  const [composer, styles] = await Promise.all([
    readSource("src/components/ai/AiPromptComposer.vue"),
    readSource("src/styles/workspaces/ai/drawer.css"),
  ]);

  assert.match(composer, /<el-image[^>]+fit="cover"/);
  assert.match(composer, /<el-button class="ai-attachment-chip__remove"[^>]+:icon="Close"/);
  assert.match(
    styles,
    /\.ai-attachment-chip\.is-image \.ai-attachment-chip__remove\.el-button\s*\{[^}]*width:\s*16px;[^}]*height:\s*16px;[^}]*border-radius:\s*50%;/s,
  );
  assert.match(styles, /\.ai-attachment-chip__remove svg\s*\{[^}]*width:\s*11px;/s);
  assert.doesNotMatch(styles, /background:\s*rgb\(17 24 39 \/ 82%\)/);
});
