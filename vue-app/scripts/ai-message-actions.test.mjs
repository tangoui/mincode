import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const read = (path) => readFile(new URL(path, import.meta.url), "utf8");
const [drawer, thread, bubble, store, markdown, zh, en] = await Promise.all([
  read("../src/components/ai/AiAssistantDrawer.vue"),
  read("../src/components/ai/AiMessageThread.vue"),
  read("../src/components/ai/AiMessageBubble.vue"),
  read("../src/stores/ai-assistant.ts"),
  read("../src/components/ui/MarkdownPreview.vue"),
  read("../src/i18n/locales/zh.ts"),
  read("../src/i18n/locales/en.ts"),
]);

test("failed requests are stored as error presentations and rendered as error cards", () => {
  assert.match(store, /export type AiAssistantMessagePresentation = "task-summary" \| "error" \| "loop-limit";/);
  assert.match(store, /value === "task-summary" \|\| value === "error" \|\| value === "loop-limit"/);
  assert.match(drawer, /content: streamTimedOut \? message : t\("ai\.agentic\.failed", \{ error: message \}\),\s*presentation: "error",/);
  assert.match(thread, /<AiErrorCard\s+v-else-if="message\.role === 'assistant' && message\.presentation === 'error' && errorLabels"/);
  assert.match(thread, /:can-retry="Boolean\(canRewind\) && isLastMessage\(index\)"/);
  assert.match(drawer, /@retry="resendTurnOf"/);
  assert.match(drawer, /@open-settings="emit\('open-settings'\)"/);
});

test("error cards and event anchors never enter the rebuilt model context", () => {
  assert.match(drawer, /\.filter\(\(message\) => message\.presentation !== "error" && !message\.eventAnchor\)/);
});

test("the last reply can be regenerated and any sent message edited in idle non-ACP conversations", () => {
  assert.match(drawer, /const canRewindActiveConversation = computed\(\(\) =>\s*assistant\.mode !== "agent" && !assistant\.activeConversation\?\.running && !assistant\.awaitingResponse,/);
  assert.match(thread, /:regenerable="Boolean\(canRewind\) && message\.role === 'assistant' && isLastMessage\(index\)"/);
  assert.match(thread, /:editable="message\.role === 'user' && \(canRewind \|\| \(failed && index === pinnedUserMessageIndex\)\)"/);
  assert.match(bubble, /v-if="regenerable"[\s\S]*?@click="emit\('regenerate', message\)"/);
  assert.match(drawer, /const removed = assistant\.rewindConversationTo\(conversationId, messageId\);\s*if \(!removed\) \{[\s\S]*?return;\s*\}\s*await sendAgentic\(\{/);
  assert.match(drawer, /function send\(\) \{\s*if \(activeParallelDelegationPending\.value\) return;\s*const budget = pluginContextBudgetState\.value;[\s\S]{0,240}?\}\s*if \(editingMessageId\.value\) return resendEditedMessage\(\);/);
  assert.match(drawer, /:editing-queue="!!editingQueueId \|\| !!editingMessageId"/);
  assert.match(store, /function rewindConversationTo\(id: string, messageId: string\) \{\s*const conversation = conversationById\(id\);\s*if \(!conversation \|\| conversation\.running \|\| conversation\.detailsLoaded === false\) return undefined;/);
});

test("AI markdown code blocks get a language label and a delegated copy button", async () => {
  for (const file of [bubble, await read("../src/components/ai/AiTaskSummaryCard.vue")]) {
    assert.match(file, /<MarkdownPreview[^>]*\bcode-copy\b/);
  }
  assert.match(markdown, /codeCopy\?: boolean;/);
  assert.match(markdown, /if \(!props\.codeCopy \|\| !root\) return;/);
  assert.match(markdown, /if \(pre\.parentElement\?\.classList\.contains\("markdown-code-block"\)\) continue;/);
  assert.match(markdown, /closest<HTMLButtonElement>\("\.markdown-code-block__copy"\)/);
  assert.match(markdown, /await copyText\(code\);/);
});

test("new message action labels exist in both locales", () => {
  for (const key of ["ai.message.edit", "ai.message.regenerate", "ai.message.resend", "ai.message.cancelEdit", "ai.errorCard.title", "ai.errorCard.hint", "ai.errorCard.retry", "ai.errorCard.settings", "project.markdown.copyCode", "project.markdown.codeCopied", "project.markdown.copyCodeLabel", "project.markdown.copyCodeFailed", "project.markdown.codeBlock"]) {
    assert.ok(zh.includes(`"${key}":`), `zh is missing ${key}`);
    assert.ok(en.includes(`"${key}":`), `en is missing ${key}`);
  }
});

test("streamed text is not a live region; run state changes are announced once", () => {
  assert.doesNotMatch(thread, /class="ai-message-thread" aria-live/);
  assert.match(drawer, /<p class="sr-only" role="status" aria-live="polite">\{\{ liveAnnouncement \}\}<\/p>/);
  assert.match(drawer, /announce\(t\("ai\.live\.replying"\)\)/);
  assert.match(drawer, /announce\(t\("ai\.live\.approvalNeeded"\)\);[\s\S]*?if \(focusInPanel\) panel\?\.querySelector<HTMLElement>\("\.ai-inline-tool-approval-card button"\)\?\.focus\(\);/);
  for (const key of ["ai.live.replying", "ai.live.completed", "ai.live.failed", "ai.live.stopped", "ai.live.approvalNeeded", "ai.context.usage", "ai.context.usageDetail", "ai.context.usageEstimateDetail", "ai.message.tokenUsagePartial", "ai.message.answeredBy"]) {
    assert.ok(zh.includes(`"${key}":`) && en.includes(`"${key}":`), `missing ${key}`);
  }
});

test("the composer shows context usage and bubbles show the answering model", () => {
  assert.match(drawer, /const estimate = estimateAiContextUsage\(assistant\.agentHistory, config\);/);
  assert.match(drawer, /:context-usage="contextUsage"/);
  assert.match(bubble, /const replyModel = computed\(\(\) => aiReplyModel\(props\.message\.tokenUsage\)\);/);
  assert.match(drawer, /const answeredBy = attemptedModels\[attemptedModels\.length - 1\];/);
});
