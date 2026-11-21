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

test("composer and drawer wire @ file suggestions", async () => {
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  const composer = await readFile(new URL("../src/components/ai/AiPromptComposer.vue", import.meta.url), "utf8");
  assert.match(drawer, /:suggestions="composerSuggestions"/);
  assert.match(drawer, /:suggestions-visible="toolSuggestionsVisible \|\| mentionSuggestionsActive"/);
  assert.match(drawer, /const mentionSuggestionsActive = computed\(\(\) => Boolean\(fileMention\.value\) \|\|/);
  assert.match(drawer, /@dismiss-suggestions="dismissComposerSuggestions"/);
  assert.match(drawer, /searchProjectFileIndex\(\[root\], query, 12\)/);
  assert.match(drawer, /"local_fs_read_ai_context_text_file"/);
  assert.match(drawer, /if \(suggestion\.kind === "file"\) \{\s*void attachMentionedFile\(suggestion\);/);
  // The watch reads the project root eagerly, so it must be declared after it.
  assert.ok(drawer.indexOf("const pendingEditsProjectRoot") < drawer.indexOf("const fileMention = computed"));
  assert.match(composer, /emit\("dismiss-suggestions"\)/);
  assert.match(composer, /suggestion\.kind === ["']file["'] \? Document/);
});
