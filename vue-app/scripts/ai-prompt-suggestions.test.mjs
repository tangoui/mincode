import assert from "node:assert/strict";
import test from "node:test";

import {
  buildAiPromptSuggestions,
  parseAiPromptSuggestionQuery,
} from "../src/domain/ai/tool-command.ts";

const tools = Array.from({ length: 12 }, (_, index) => ({
  name: `workspace.tool_${index + 1}`,
  description: `Tool ${index + 1}`,
}));
test("lists every enabled tool without an eight-item limit", () => {
  const suggestions = buildAiPromptSuggestions("/", tools);
  assert.equal(suggestions.length, 12);
  assert.equal(suggestions.at(-1)?.name, "workspace.tool_12");
});

test("supports an explicit tool filter", () => {
  assert.deepEqual(parseAiPromptSuggestionQuery("/tool search"), {
    kind: "tool",
    query: "search",
  });
  assert.ok(buildAiPromptSuggestions("/tool tool_12", tools).every((item) => item.kind === "tool"));
});
