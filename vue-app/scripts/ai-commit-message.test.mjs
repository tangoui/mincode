import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

const service = await readFile(
  new URL("../src/services/ai-commit-message.ts", import.meta.url),
  "utf8",
);
const failoverImport = 'import { streamAiTurnWithFailover } from "@/services/ai-model-failover";';
assert.ok(service.includes(failoverImport));
// Run the real service with failover reduced to a direct call of the injected stream.
const compiled = ts.transpileModule(
  service.replace(
    failoverImport,
    "const streamAiTurnWithFailover = (o) => o.streamTurn({ ...o.config, model: o.models[0] }, o.messages, o.tools, o.onEvent, o.signal, o.idleTimeoutMs);",
  ),
  { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 } },
).outputText;
const { generateAiCommitMessage, commitMessagePrompt } = await import(
  `data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`
);

function recorder(responses) {
  const calls = [];
  const streamTurn = async (config, messages, tools, onEvent, signal, idleTimeoutMs) => {
    calls.push({ config, messages, tools, idleTimeoutMs });
    const next = responses.shift();
    if (next instanceof Error) throw next;
    // Strings are visible text; { reasoning } items are a thinking model's reasoning stream.
    for (const item of next) onEvent(typeof item === "string" ? { type: "chunk", text: item } : { type: "reasoning", text: item.reasoning });
  };
  return { calls, streamTurn };
}

const baseConfig = { baseUrl: "", apiKey: "k", model: "m", apiFormat: "openai", maxOutputTokens: 0 };

test("OpenAI-style models are asked for low reasoning with room for thinking tokens", async () => {
  for (const apiFormat of ["openai", "openai-responses"]) {
    const { calls, streamTurn } = recorder([["feat(ai): ", "add cache"]]);
    const deltas = [];
    const message = await generateAiCommitMessage({
      config: { ...baseConfig, apiFormat, reasoningEffort: "high" },
      patch: "diff --git a/x b/x",
      streamTurn,
      onDelta: (delta) => deltas.push(delta),
    });
    assert.equal(message, "feat(ai): add cache");
    assert.deepEqual(deltas, ["feat(ai): ", "add cache"]);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].config.reasoningEffort, "low");
    assert.equal(calls[0].config.maxOutputTokens, 2048);
    assert.deepEqual(calls[0].tools, []);
  }
});

test("Anthropic runs without thinking and respects a smaller profile cap", async () => {
  const { calls, streamTurn } = recorder([["fix: typo"]]);
  await generateAiCommitMessage({
    config: { ...baseConfig, apiFormat: "anthropic", reasoningEffort: "high", maxOutputTokens: 300 },
    patch: "diff",
    streamTurn,
  });
  assert.equal(calls[0].config.reasoningEffort, undefined);
  assert.equal(calls[0].config.maxOutputTokens, 300);
});

test("models that reject the reasoning parameter are retried once without it", async () => {
  const { calls, streamTurn } = recorder([
    new Error("HTTP 400: Unsupported parameter: 'reasoning_effort' is not supported with this model."),
    ["chore: bump"],
  ]);
  const message = await generateAiCommitMessage({ config: baseConfig, patch: "diff", streamTurn });
  assert.equal(message, "chore: bump");
  assert.deepEqual(calls.map((call) => call.config.reasoningEffort), ["low", undefined]);
});

test("the caller's model order is used, first model first", async () => {
  const { calls, streamTurn } = recorder([["feat: x"]]);
  await generateAiCommitMessage({ config: baseConfig, models: ["picked", "backup"], patch: "diff", streamTurn });
  assert.equal(calls[0].config.model, "picked");
});

test("other failures are not retried", async () => {
  const { calls, streamTurn } = recorder([new Error("HTTP 401: invalid api key"), ["never"]]);
  await assert.rejects(
    generateAiCommitMessage({ config: baseConfig, patch: "diff", streamTurn }),
    /401/,
  );
  assert.equal(calls.length, 1);
});

test("commit message generation bounds the diff and strips Markdown fences", async () => {
  const prompt = commitMessagePrompt("x".repeat(20_000));
  assert.ok(prompt[1].content.length < 12_100);
  assert.match(prompt[0].content, /只输出 commit message/);
  const { streamTurn } = recorder([["```\nfeat: a\n```"]]);
  assert.equal(await generateAiCommitMessage({ config: baseConfig, patch: "diff", streamTurn }), "feat: a");
  await assert.rejects(generateAiCommitMessage({ config: baseConfig, patch: "  ", streamTurn }), /empty/);
});

test("a thinking model's reasoning is reported separately and never written into the message", async () => {
  const { streamTurn } = recorder([[{ reasoning: "look at the diff" }, { reasoning: " it edits docs" }, "docs: update readme"]]);
  const deltas = [];
  const reasoning = [];
  const message = await generateAiCommitMessage({
    config: baseConfig,
    patch: "diff",
    streamTurn,
    onDelta: (delta) => deltas.push(delta),
    onReasoning: () => reasoning.push(1),
  });
  assert.equal(message, "docs: update readme");
  assert.deepEqual(deltas, ["docs: update readme"]);
  assert.equal(reasoning.length, 2);
});

test("the no-activity timeout is passed to the stream, so reasoning keeps a slow model alive", async () => {
  const { calls, streamTurn } = recorder([["feat: x"]]);
  await generateAiCommitMessage({ config: baseConfig, patch: "diff", streamTurn, idleTimeoutMs: 30_000 });
  assert.equal(calls[0].idleTimeoutMs, 30_000);
});
