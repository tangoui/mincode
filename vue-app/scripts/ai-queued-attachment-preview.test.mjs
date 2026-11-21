import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const threadUrl = new URL("../src/components/ai/AiQueuedMessages.vue", import.meta.url);
const styleUrl = new URL("../src/styles/workspaces/ai/drawer.css", import.meta.url);
const [threadSource, styleSource] = await Promise.all([
  readFile(threadUrl, "utf8"),
  readFile(styleUrl, "utf8"),
]);

test("queued image attachments use the shared full-screen preview behavior", () => {
  assert.match(threadSource, /import \{ getAttachmentImageSrc \} from "@\/domain\/ai\/attachments";/);
  assert.match(threadSource, /function queuedPreviewImageUrls\(message: AiQueuedMessage\)/);
  assert.match(
    threadSource,
    /<el-image[\s\S]*:preview-src-list="queuedPreviewImageUrls\(message\)"[\s\S]*preview-teleported[\s\S]*hide-on-click-modal/,
  );
  assert.doesNotMatch(threadSource, /<img v-if="attachment\.kind === 'image'/);
});

test("queued messages sit above the composer as compact lines with small thumbnails", () => {
  assert.match(threadSource, /:class="\{ 'is-editing': message\.id === editingId \}"/);
  assert.match(threadSource, /class="ai-queued-message__thumb"/);
  assert.match(
    styleSource,
    /\.ai-queued-message__thumb\.el-image \{[\s\S]*width: 22px;[\s\S]*height: 22px;/,
  );
  assert.match(styleSource, /\.ai-queued-message__text \{[\s\S]*text-overflow: ellipsis;/);
});
