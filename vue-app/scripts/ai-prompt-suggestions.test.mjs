import assert from "node:assert/strict";
import test from "node:test";

import {
  buildAiPromptSuggestions,
  parseAiPromptSuggestionQuery,
  skillSuggestionText,
} from "../src/domain/ai/tool-command.ts";

const tools = Array.from({ length: 12 }, (_, index) => ({
  name: `workspace.tool_${index + 1}`,
  description: `Tool ${index + 1}`,
}));
const skills = [
  { id: "imagegen", name: "imagegen", description: "Generate images" },
  { id: "documents", name: "documents", description: "Edit documents" },
];

test("lists every enabled skill and tool without an eight-item limit", () => {
  const suggestions = buildAiPromptSuggestions("/", tools, skills);
  assert.equal(suggestions.length, 14);
  assert.deepEqual(suggestions.slice(0, 2).map((item) => item.kind), ["skill", "skill"]);
  assert.equal(suggestions.at(-1)?.name, "workspace.tool_12");
});

test("supports explicit skill and tool filters", () => {
  assert.deepEqual(parseAiPromptSuggestionQuery("/skill"), { kind: "skill", query: "" });
  assert.deepEqual(parseAiPromptSuggestionQuery("/tool search"), {
    kind: "tool",
    query: "search",
  });
  assert.deepEqual(
    buildAiPromptSuggestions("/skill image", tools, skills).map((item) => item.name),
    ["imagegen"]
  );
  assert.ok(buildAiPromptSuggestions("/tool tool_12", tools, skills).every((item) => item.kind === "tool"));
});

test("inserts an explicit skill invocation", () => {
  assert.equal(skillSuggestionText("imagegen"), "/skill imagegen ");
});
