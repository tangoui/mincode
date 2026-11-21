import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const toolbarUrl = new URL("../src/components/ai/AiImagePreviewToolbar.vue", import.meta.url);
const bubbleUrl = new URL("../src/components/ai/AiMessageBubble.vue", import.meta.url);
const threadUrl = new URL("../src/components/ai/AiQueuedMessages.vue", import.meta.url);
const drawerUrl = new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url);
const clipboardUrl = new URL("../src/services/clipboard.ts", import.meta.url);
const [toolbarSource, bubbleSource, threadSource, drawerSource, clipboardSource] = await Promise.all([
  readFile(toolbarUrl, "utf8"),
  readFile(bubbleUrl, "utf8"),
  readFile(threadUrl, "utf8"),
  readFile(drawerUrl, "utf8"),
  readFile(clipboardUrl, "utf8"),
]);

test("AI image preview toolbar copies only the active image", () => {
  assert.match(toolbarSource, /const attachment = props\.attachments\[props\.activeIndex\]/);
  assert.match(toolbarSource, /createAiMessageClipboardPayload\("", \[attachment\]\)/);
  assert.match(toolbarSource, /await copyImage\(image\)/);
  assert.match(toolbarSource, /@keydown\.enter\.prevent="copyActiveImage"/);
});

test("persisted message images derive their preview source from attachment data", () => {
  assert.match(bubbleSource, /import \{ getAttachmentImageSrc \} from "@\/domain\/ai\/attachments";/);
  assert.match(bubbleSource, /previewImageAttachments\.value\.map\(getAttachmentImageSrc\)/);
  assert.match(bubbleSource, /:src="getAttachmentImageSrc\(attachment\)"/);
  assert.doesNotMatch(bubbleSource, /attachment\.kind === "image" && attachment\.previewUrl/);
});

test("message and queued image previews install the copy toolbar", () => {
  assert.match(bubbleSource, /#toolbar="\{ actions, reset, activeIndex \}"/);
  assert.match(bubbleSource, /<AiImagePreviewToolbar[\s\S]*:attachments="previewImageAttachments"/);
  assert.match(threadSource, /function previewImageAttachments\(message: AiQueuedMessage\)/);
  assert.match(threadSource, /<AiImagePreviewToolbar[\s\S]*:attachments="previewImageAttachments\(message\)"/);
  assert.match(drawerSource, /:copy-image-label="t\('ai\.image\.copy'\)"/);
});

test("desktop image copy normalizes supported images before invoking the native clipboard", () => {
  assert.match(clipboardSource, /async function nativeClipboardImage\(image: ClipboardImage\)/);
  assert.match(clipboardSource, /mimeType:\s*"image\/png"/);
  assert.match(clipboardSource, /await invokeBackend<void>\("clipboard_write_rich"/);
  assert.match(clipboardSource, /imageMimeType:\s*image\.mimeType/);
});
