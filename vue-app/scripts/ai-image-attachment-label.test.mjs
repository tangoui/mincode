import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const [bubble, thread] = await Promise.all([
  readFile(new URL("../src/components/ai/AiMessageBubble.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ai/AiQueuedMessages.vue", import.meta.url), "utf8"),
]);

test("已发送消息的有效图片只显示缩略图，名称仅作为可访问信息和失败兜底", () => {
  assert.match(bubble, /v-if="canShowImageAttachment\(attachment\)"/);
  assert.match(bubble, /:title="attachment\.name"/);
  assert.match(bubble, /:alt="attachment\.name"/);
  assert.match(bubble, /@error="markImageAttachmentFailed\(attachment\.id\)"/);
  assert.match(bubble, /!failedImageAttachmentIds\.value\.has\(attachment\.id\)/);
  assert.match(bubble, /<template v-else>[\s\S]*?<Document[\s\S]*?<span>\{\{ attachment\.name \}\}<\/span>/);
  assert.doesNotMatch(bubble, /<\/el-image>\s*<Document v-else[\s\S]*?<span>\{\{ attachment\.name \}\}<\/span>/);
});

test("排队消息采用相同的图片名称隐藏和失败兜底规则", () => {
  assert.match(thread, /canShowImageAttachment\(attachment\)/);
  assert.match(thread, /:alt="attachment\.name"/);
  assert.match(thread, /@error="markImageAttachmentFailed\(attachment\.id\)"/);
  assert.match(thread, /!failedImageAttachmentIds\.value\.has\(attachment\.id\)/);
  // 输入框上方的紧凑队列：文件与加载失败的图片合计为文件数，完整名称在编辑时可见。
  assert.match(thread, /\.filter\(\(attachment\) => !canShowImageAttachment\(attachment\)\)\.length/);
  assert.match(thread, /<Document \/>/);
});
