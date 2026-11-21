import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

import { runWithAbort } from "../src/domain/async/abort.ts";

const { AbortController } = globalThis;

async function source(path) {
  return readFile(new URL(path, import.meta.url), "utf8");
}

test("abort releases a caller waiting on a stalled task", async () => {
  const controller = new AbortController();
  let started;
  const taskStarted = new Promise((resolve) => {
    started = resolve;
  });
  const pending = runWithAbort(controller.signal, () => {
    started();
    return new Promise(() => {});
  });

  await taskStarted;
  controller.abort();
  await assert.rejects(pending, (error) => error?.name === "AbortError");
});

test("an operation does not start when its signal is already aborted", async () => {
  const controller = new AbortController();
  controller.abort();
  let started = false;

  await assert.rejects(
    runWithAbort(controller.signal, () => {
      started = true;
    }),
    (error) => error?.name === "AbortError",
  );
  assert.equal(started, false);
});

test("the AI loop and capability registry propagate cancellation", async () => {
  const [loopSource, capabilitySource] = await Promise.all([
    source("../src/services/ai-agent-loop.ts"),
    source("../src/domain/ai/capability-registry.ts"),
  ]);

  assert.match(
    loopSource,
    /runWithAbort\(options\.signal, \(\) =>\s*options\.executeTool\(/,
  );
  assert.match(capabilitySource, /signal\?: AbortSignal/);
  assert.match(capabilitySource, /const signal = dependencies\.signal \?\?/);
  assert.match(
    capabilitySource,
    /const cancelOperation = \(\) => operation\.cancel\(\)/,
  );
  assert.match(
    capabilitySource,
    /dependencies\.signal\?\.addEventListener\("abort", cancelOperation/,
  );
  assert.match(
    capabilitySource,
    /runWithAbort\(dependencies\.signal, \(\) => operation\.done\)/,
  );
});

test("the stop handler aborts before reporting progress", async () => {
  const drawerSource = await source(
    "../src/components/ai/AiAssistantDrawer.vue",
  );
  const start = drawerSource.indexOf("async function cancelConversation");
  const end = drawerSource.indexOf("async function cancelCurrent", start);
  const cancelSource = drawerSource.slice(start, end);

  assert.ok(cancelSource.indexOf("controller.abort()") >= 0);
  assert.ok(
    cancelSource.indexOf("controller.abort()") <
      cancelSource.indexOf(
        'reportConversationRun(conversationId, "cancelling"',
      ),
  );
  assert.match(
    drawerSource,
    /executeTool: \(call, signal\) => mcpLoader\.length && call\.name === MCP_LOAD_TOOL_NAME\s*\?\s*loadMcpToolsForModel\([^)]*\bsignal\)\s*:\s*executeAgentTool\(/,
  );
  assert.match(drawerSource, /signal,\s*\.\.\.\(authorized/);
});

test("manual stop cancels stale runs without automatically consuming the queue", async () => {
  const drawerSource = await source(
    "../src/components/ai/AiAssistantDrawer.vue",
  );
  const cancelStart = drawerSource.indexOf("async function cancelConversation");
  const cancelEnd = drawerSource.indexOf("async function cancelCurrent", cancelStart);
  const cancelSource = drawerSource.slice(cancelStart, cancelEnd);
  const recoveryStart = drawerSource.indexOf("async function recoverStalledQueueInterruption");
  const recoveryEnd = drawerSource.indexOf("async function interruptConversationAndRunQueue", recoveryStart);
  const recoverySource = drawerSource.slice(recoveryStart, recoveryEnd);

  assert.match(cancelSource, /if \(!controller\) \{[\s\S]*?recoverStalledQueueInterruption\(conversationId, options\.resumeQueue \?\? false\)/);
  assert.ok(
    cancelSource.indexOf("const cancelTarget =") < cancelSource.lastIndexOf("if (!controller)"),
    "ACP fallback target must be resolved before the missing-controller branch",
  );
  assert.match(recoverySource, /activeAcpPrompts\.get\(conversationId\) \?\? acpSessionTarget\(conversation\)[\s\S]*?invokeBackend\("acp_cancel", prompt\)/);
  assert.match(
    recoverySource,
    /if \(runQueue\) \{\s*await runNextQueuedMessage\(conversationId\)/,
  );
  assert.match(
    drawerSource,
    /const shouldResumeQueue = !abortController\.signal\.aborted[\s\S]*?if \(shouldResumeQueue\) \{\s*await runNextQueuedMessage\(conversationId\)/,
  );
  assert.match(
    drawerSource,
    /cancelConversation\(conversationId, \{ resumeQueue: true \}\)/,
  );
  assert.match(drawerSource, /if \(targetConversation\?\.running && !request\)/);
  assert.match(drawerSource, /if \(activeConversation\?\.running && !request\)/);
});

test("abort releases a model turn without waiting for a transport close event", async () => {
  const loopSource = await source("../src/services/ai-agent-loop.ts");
  const streamStart = loopSource.indexOf(
    "async function streamAiLoopTurnViaStream",
  );
  const streamEnd = loopSource.indexOf("function parseCompletion", streamStart);
  const streamSource = loopSource.slice(streamStart, streamEnd);
  const cancelStart = streamSource.indexOf("const cancel = () => {");
  const cancelEnd = streamSource.indexOf("};", cancelStart);
  const cancelSource = streamSource.slice(cancelStart, cancelEnd);

  assert.match(
    loopSource,
    /await runWithAbort\(options\.signal, \(\) =>\s*\(options\.streamTurn \?\? streamAiLoopTurn\)\(/,
  );
  assert.match(loopSource, /if \(options\.signal\?\.aborted\) return;/);
  assert.ok(cancelSource.indexOf("completion.cancel()") >= 0);
  assert.ok(
    cancelSource.indexOf("completion.cancel()") < cancelSource.indexOf("cancelProvider()"),
  );
  assert.ok(cancelSource.indexOf("closeSocket()") > cancelSource.indexOf("cancelProvider()"));
  assert.match(
    streamSource,
    /const cancelProvider = \(\) => \{[\s\S]*invokeBackend\("ai_stream_cancel"/,
  );
});

test("the response indicator follows visible replies and later agent activity", async () => {
  const [storeSource, styles] = await Promise.all([
    source("../src/stores/ai-assistant.ts"),
    source("../src/styles/workspaces/ai/drawer.css"),
  ]);

  assert.match(
    storeSource,
    /function applyConversationRunningProjection[\s\S]*setConversationAwaitingResponse\(id, running\)/,
  );
  assert.match(
    storeSource,
    /function updateLastAssistantForConversation[\s\S]*if \(content\.trim\(\)\) setConversationAwaitingResponse\(id, false\)/,
  );
  assert.match(
    storeSource,
    /function appendAssistantMessageForConversation[\s\S]*if \(content\.trim\(\)\) setConversationAwaitingResponse\(id, false\)/,
  );
  assert.match(
    storeSource,
    /function upsertAgentEventForConversation[\s\S]*if \(conversation\.running\) setConversationAwaitingResponse\(id, true\)/,
  );
  assert.match(styles, /\.ai-thinking-card > \.el-card__body\s*\{[^}]*padding:\s*5px 8px;/s);
  assert.match(styles, /\.ai-thinking-card__content\s*\{[^}]*font-size:\s*11px;/s);
});

test("ACP prompts abort immediately and await authoritative terminal progress", async () => {
  const [drawerSource, threadSource] = await Promise.all([
    source("../src/components/ai/AiAssistantDrawer.vue"),
    source("../src/components/ai/AiMessageThread.vue"),
  ]);

  assert.match(
    drawerSource,
    /const acpPromptAborts = new Map<string, AbortController>\(\)/,
  );
  assert.match(
    drawerSource,
    /runWithAbort\(abortController\.signal, \(\) =>\s*invokeBackend\("acp_prompt"/,
  );
  assert.match(drawerSource, /controller\.abort\(\)/);
  assert.doesNotMatch(drawerSource, /if \(aborted\) void runUpdate;/);
  assert.doesNotMatch(drawerSource, /if \(cancelled\) void runUpdate;/);
  assert.equal(drawerSource.match(/await runUpdate;/g)?.length, 2);
  // 状态行贯穿整个运行（设计 2026-10-28）：内容随阶段变化（等待模型、生成回复、运行某个工具、等待审批），
  // 不再只在等待首个输出时出现，因而也不会每来一个事件就闪一次。
  assert.match(drawerSource, /:running="assistant\.loading"/);
  assert.match(drawerSource, /:running-label="runStatusText"/);
  assert.match(threadSource, /v-if="running"/);
  assert.match(threadSource, /class="ai-thinking-card__content"/);
  // 文字随每个工具变化，不作为实时播报区域；播报只经由 liveAnnouncement。
  assert.doesNotMatch(threadSource, /class="ai-thinking-card"[\s\S]{0,120}role="status"/);
});

test("backend AI Stream cancels the provider when frontend delivery fails", async () => {
  const backendSource = await readFile(
    new URL("../../backend/ai/stream.go", import.meta.url),
    "utf8",
  );
  const handlerStart = backendSource.indexOf("func aiChatStreamHandler");
  const handlerEnd = backendSource.indexOf("type aiStreamCancelArgs", handlerStart);
  const handlerSource = backendSource.slice(handlerStart, handlerEnd);

  assert.match(
    handlerSource,
    /if err := conn\.SendJSON\(payload\); err != nil \{\s*cancel\(\)/,
  );
});
