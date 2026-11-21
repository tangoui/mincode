import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const loopSource = await readFile(
  new URL("../src/services/ai-agent-loop.ts", import.meta.url),
  "utf8",
);

const TOOLS_FOR_REQUEST =
  /const tools = options\.currentTools\?\.\(\) \?\? options\.tools;\s*return tools\.length > 0 \? \[\.\.\.tools, completionTool\] : \[\];/;

test("plain conversational turns do not append task_complete", () => {
  assert.match(loopSource, TOOLS_FOR_REQUEST);
});

test("selected business tools still append task_complete", () => {
  assert.ok(loopSource.match(TOOLS_FOR_REQUEST));
});

test("tools are read again before every model request so tools loaded mid-turn are offered", () => {
  assert.match(
    loopSource,
    /const tools = toolsForRequest\(\);\s*const \{[^}]*\} = await streamRequest\(history, tools\);/,
  );
  assert.match(
    loopSource,
    /const streamRequest = async \([^]*?await runWithAbort\(options\.signal, \(\) =>\s*\(options\.streamTurn \?\? streamAiLoopTurn\)\(\s*options\.config,\s*requestMessages,\s*tools,/,
  );
});


test("assistant isolates pure social turns from persisted task history", async () => {
  const drawer = await readFile(
    new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url),
    "utf8",
  );
  // The selection itself is unit-tested in ai-turn-history.test.mjs.
  assert.match(
    drawer,
    /directConversation: selectedAttachments\.length === 0 && isDirectConversationIntent\(text\),/,
  );
  assert.match(drawer, /const conversationHistory = turnHistory\.messages;/);
  assert.match(drawer, /if \(!turnHistory\.persist\) return;/);
  // Both the per-turn and the final write go through the guarded helper.
  assert.match(drawer, /onHistoryChange: saveLoopHistory,/);
  assert.match(drawer, /saveLoopHistory\(result\.history\);/);
  assert.doesNotMatch(drawer, /setConversationAgentHistory\(\s*conversationId,\s*(?:result\.history|updatedHistory)/);
});

test("persisted unknown tool names are normalized before provider transport", () => {
  assert.match(
    loopSource,
    /const existing = providerByInternal\.get\(name\);[\s\S]*?const providerName = providerToolName\(name, used\);[\s\S]*?providerByInternal\.set\(name, providerName\);/,
  );
  assert.doesNotMatch(
    loopSource,
    /toProviderName:\s*\(name: string\) => providerByInternal\.get\(name\) \?\? name/,
  );
});

test("the request is saved to the loop history before the first model call", async () => {
  const drawerSource = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  assert.match(drawerSource, /saveLoopHistory\(history\);\s*const result = await runAiLoop\(\{/);
});
