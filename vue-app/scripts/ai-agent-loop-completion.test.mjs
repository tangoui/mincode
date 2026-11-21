import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { aiLoopTurnCompletion } from "../src/domain/ai/agent-loop-completion.ts";

test("没有新工具调用的完整回复可以直接结束", () => {
  assert.deepEqual(aiLoopTurnCompletion("  配置已检查完成，不需要修改。  ", 0), {
    completed: true,
    requiresContinuation: false,
    summary: "配置已检查完成，不需要修改。",
  });
});

test("明确承诺后续动作的回复必须继续", () => {
  const samples = [
    "已找到相关代码，我接下来继续检查调用链。",
    "下一步我会修改实现并运行测试。",
    "让我继续读取相关文件。",
    "I found the handler. I'll continue checking the call chain.",
    "Next, I will run the relevant tests.",
  ];
  for (const content of samples) {
    const result = aiLoopTurnCompletion(content, 0);
    assert.equal(result.completed, false, content);
    assert.equal(result.requiresContinuation, true, content);
  }
});

test("给用户的后续建议不触发额外 AI 请求", () => {
  const samples = [
    "检查已完成。建议下一步补充集成测试。",
    "问题已修复，你可以重新启动应用验证。",
    "验证通过。如需进一步优化，我可以继续处理。",
    "The fix is complete. You can run the app to verify it.",
  ];
  for (const content of samples) {
    assert.equal(aiLoopTurnCompletion(content, 0).completed, true, content);
  }
});

test("以用户输入为前提的提议不触发继续执行", () => {
  const samples = [
    "你好！我可以帮你排查问题、修改代码。请直接说明需求，我会先分析再动手。",
    "如果你提供报错日志，我会继续定位原因。",
    "告诉我具体文件，我再帮你检查。",
    "Hi! Tell me what you need and I'll investigate it.",
    "If you share the logs, I will check them.",
  ];
  for (const content of samples) {
    const result = aiLoopTurnCompletion(content, 0);
    assert.equal(result.completed, true, content);
    assert.equal(result.requiresContinuation, false, content);
  }
  assert.equal(
    aiLoopTurnCompletion("请稍等，我接下来继续检查调用链。", 0).requiresContinuation,
    true,
  );
});

test("没有可用工具或继续指令已被忽略时直接交付回复，避免重复请求", async () => {
  const source = await readFile(
    new URL("../src/services/ai-agent-loop.ts", import.meta.url),
    "utf8",
  );
  assert.match(
    source,
    /const cannotContinue = turnCompletion\.requiresContinuation\s*&& \(tools\.length === 0 \|\| awaitingContinuation\);\s*if \(turnCompletion\.completed \|\| cannotContinue\)/,
  );
  assert.match(source, /awaitingContinuation = turnCompletion\.requiresContinuation;\s*if \(turnCompletion\.requiresContinuation\)/);
});

test("仍有工具调用或回复为空时继续循环", () => {
  assert.equal(aiLoopTurnCompletion("还要继续检查", 1).completed, false);
  assert.equal(aiLoopTurnCompletion("   ", 0).completed, false);
});

test("AI 循环仅在未完成承诺时注入继续执行指令", async () => {
  const source = await readFile(
    new URL("../src/services/ai-agent-loop.ts", import.meta.url),
    "utf8",
  );
  assert.match(source, /const turnCompletion = aiLoopTurnCompletion\(content, businessCalls\.length\)/);
  assert.match(source, /if \(turnCompletion\.completed \|\| cannotContinue\)[\s\S]*?outcome: "response"[\s\S]*?summary: turnCompletion\.summary/);
  assert.match(source, /if \(turnCompletion\.requiresContinuation\)[\s\S]*?Continue the promised work now/);
});

test("task_complete is accepted only alone; otherwise other tools still run and it gets a result", async () => {
  const { readFile } = await import("node:fs/promises");
  const loop = await readFile(new URL("../src/services/ai-agent-loop.ts", import.meta.url), "utf8");
  const execute = loop.indexOf("toolResults = await executeToolBatches(");
  const rejection = loop.indexOf("const completionRejection = invalidCompletion");
  assert.ok(execute > 0 && rejection > execute, "business tools must run before completion is judged");
  assert.match(loop, /: completionSummary && businessCalls\.length > 0\s*\? PREMATURE_COMPLETION_RESULT/);
  assert.match(
    loop,
    /if \(completionRejection\) \{\s*for \(const call of completionCalls\) \{\s*history\.push\(\{ role: "tool", toolCallId: call\.id, content: completionRejection \}\);/,
  );
  assert.doesNotMatch(loop, /role: "user",\s*content:\s*"The task_complete call was invalid/);
});
