import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import ts from "typescript";

async function importRenderWindow() {
  const source = await readFile(
    new URL("../src/domain/ai/message-render-window.ts", import.meta.url),
    "utf8",
  );
  const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  return import(`data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`);
}

test("renders only the latest batch for a long conversation", async () => {
  const window = await importRenderWindow();
  assert.equal(window.initialAiMessageRenderStart(20), 0);
  assert.equal(window.initialAiMessageRenderStart(200), 120);
  assert.equal(window.earlierAiMessageRenderStart(120), 40);
  assert.equal(window.earlierAiMessageRenderStart(40), 0);
});

test("keeps following new messages until the user loads older history", async () => {
  const window = await importRenderWindow();
  assert.equal(window.nextAiMessageRenderStart(120, 200, 201), 121);
  assert.equal(window.nextAiMessageRenderStart(40, 200, 201), 40);
  assert.equal(window.nextAiMessageRenderStart(40, 200, 30), 0);
});

test("the rendered window starts at the user message that opens its turn", async () => {
  const window = await importRenderWindow();
  const messages = [
    { role: "user" }, { role: "assistant" }, { role: "assistant" },
    { role: "user" }, { role: "assistant" }, { role: "assistant" }, { role: "assistant" },
  ];
  assert.equal(window.alignAiMessageRenderStartToTurn(messages, 5), 3);
  assert.equal(window.alignAiMessageRenderStartToTurn(messages, 3), 3);
  assert.equal(window.alignAiMessageRenderStartToTurn(messages, 0), 0);
  assert.equal(window.alignAiMessageRenderStartToTurn(messages, 2), 0);
});

test("a very long turn does not pull the whole history into the window", async () => {
  const window = await importRenderWindow();
  const messages = [{ role: "user" }, ...Array.from({ length: 500 }, () => ({ role: "assistant" }))];
  assert.equal(window.alignAiMessageRenderStartToTurn(messages, 450), 450);
  assert.equal(window.alignAiMessageRenderStartToTurn(messages, 150), 0);
});

test("paints the newest messages first and fills the regular window in one step", async () => {
  const window = await importRenderWindow();
  assert.equal(window.firstPaintAiMessageRenderStart(10), 0);
  assert.equal(window.firstPaintAiMessageRenderStart(200), 184);
  assert.equal(window.filledAiMessageRenderStart(184, 200), 120);
  assert.equal(window.filledAiMessageRenderStart(120, 200), 120);
  // A window the user already widened (load earlier, reveal) is never narrowed.
  assert.equal(window.filledAiMessageRenderStart(40, 200), 40);
  assert.equal(window.filledAiMessageRenderStart(5, 10), 0);
});
