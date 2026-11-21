import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { applyAiFileMention, parseAiFileMention } from "../src/domain/ai/file-mention.ts";

test("detects the @mention being typed at the end of the prompt", () => {
  assert.deepEqual(parseAiFileMention("@"), { query: "", start: 0 });
  assert.deepEqual(parseAiFileMention("看看 @src/ma"), { query: "src/ma", start: 3 });
  assert.deepEqual(parseAiFileMention("first line\n@read"), { query: "read", start: 11 });
  assert.equal(parseAiFileMention("mail me@example.com"), null);
  assert.equal(parseAiFileMention("@src/main.ts "), null);
  assert.equal(parseAiFileMention("plain text"), null);
});

test("replaces the typed mention with the chosen path", () => {
  const input = "解释 @ma";
  const mention = parseAiFileMention(input);
  assert.equal(applyAiFileMention(input, mention, "src/main.ts"), "解释 @src/main.ts ");
  assert.equal(applyAiFileMention("@", parseAiFileMention("@"), "docs/my notes.md"), "@\"docs/my notes.md\" ");
});
