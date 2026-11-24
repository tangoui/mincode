import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const [threadSource, bubbleSource, previewSource, drawerSource] = await Promise.all([
  readFile(new URL("../src/components/ai/AiMessageThread.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ai/AiMessageBubble.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ui/MarkdownPreview.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8"),
]);

test("正在流式增长的最后一条助手消息按块增量渲染 Markdown，结束时不换组件", () => {
  assert.match(threadSource, /function isStreamingAssistantMessage/);
  assert.match(threadSource, /:streaming="isStreamingAssistantMessage\(item\.message, item\.index\)"/);
  // 正在生成取决于会话是否在运行，而不是“等待首个输出”（首段正文一到它就变为 false）。
  assert.match(threadSource, /if \(!props\.generating\) return -1;/);
  assert.match(drawerSource, /:generating="assistant\.loading"/);
  assert.match(bubbleSource, /streaming\?: boolean/);
  // 同一个 MarkdownPreview 实例覆盖流式与完成两种状态；流式时传原文，不做元数据拆分。
  assert.match(
    bubbleSource,
    /<MarkdownPreview\s+v-if="streaming \? message\.content : presentation\.content"[\s\S]*?:streaming="streaming"[\s\S]*?:content="streaming \? message\.content : presentation\.content"/,
  );
  assert.doesNotMatch(bubbleSource, /ai-message-card__streaming-text/);
  assert.match(previewSource, /props\.streaming \? streamedHtml\(props\.content\)/);
  assert.match(previewSource, /const MIN_RENDER_INTERVAL_MS = 120;/);
});

test("流式消息不重复解析元数据、构建富文本剪贴板或主动读取布局", () => {
  assert.match(bubbleSource, /const canCopyMessage = computed/);
  assert.doesNotMatch(bubbleSource, /const clipboardPayload = computed/);
  assert.match(bubbleSource, /const clipboardPayload = createAiMessageClipboardPayload/);
  assert.doesNotMatch(bubbleSource, /\(\) => \[props\.message\.content/);
  assert.match(bubbleSource, /setBodyMeasurementObserved\(!streaming\)/);
  assert.match(bubbleSource, /v-if="!streaming && presentation\.metadata\.length"/);
});
