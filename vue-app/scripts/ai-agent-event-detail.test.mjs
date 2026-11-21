import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { presentAiAgentEventDetail } from "../src/domain/ai/agent-event-detail.ts";

function event(overrides = {}) {
  return {
    kind: "tool",
    title: "Tool call",
    detail: "",
    status: "completed",
    ...overrides,
  };
}

test("renders structured objects and arrays as formatted JSON", () => {
  assert.deepEqual(presentAiAgentEventDetail(event({
    detail: '{"result":{"written":true},"items":[1,2]}',
  })), {
    kind: "json",
    language: "json",
    content: '{\n  "result": {\n    "written": true\n  },\n  "items": [\n    1,\n    2\n  ]\n}',
  });
});

test("renders Markdown file content with the safe Markdown preview", () => {
  assert.deepEqual(presentAiAgentEventDetail(event({
    toolName: "workspace.read_file",
    toolInput: '{"path":"docs/guide.md"}',
    detail: "# Guide\n\n- First\n- Second",
  })), {
    kind: "markdown",
    language: "markdown",
    content: "# Guide\n\n- First\n- Second",
  });
});

test("detects source language from file path and preserves code whitespace", () => {
  assert.deepEqual(presentAiAgentEventDetail(event({
    toolName: "workspace.read_file",
    toolInput: '{"path":"backend/main.go"}',
    detail: 'package main\n\nfunc main() {\n\tfmt.Println("ok")\n}',
  })), {
    kind: "code",
    language: "go",
    content: 'package main\n\nfunc main() {\n\tfmt.Println("ok")\n}',
  });
});

test("keeps command output verbatim instead of interpreting it as Markdown", () => {
  assert.deepEqual(presentAiAgentEventDetail(event({
    toolName: "workspace.run_command",
    toolInput: '{"command":"go test ./..."}',
    detail: "PASS\n# output that is not a heading",
  })), {
    kind: "terminal",
    language: "terminal",
    content: "PASS\n# output that is not a heading",
  });
});

test("does not render a failed file read as source code based only on its extension", () => {
  assert.equal(presentAiAgentEventDetail(event({
    toolName: "workspace.read_file",
    toolInput: '{"path":"missing.go"}',
    detail: "No such file",
    status: "failed",
  })).kind, "markdown");
});

test("uses Markdown for human-readable tool text and never treats scalar JSON as structured output", () => {
  assert.equal(presentAiAgentEventDetail(event({ detail: "**Done**\n\nNext step" })).kind, "markdown");
  assert.equal(presentAiAgentEventDetail(event({ detail: '"plain text"' })).kind, "markdown");
});


test("classifies the extracted presentation text instead of a structured envelope", () => {
  const source = event({
    toolName: "workspace.read_file",
    toolInput: '{"path":"README.md"}',
    detail: '{"detail":"# Read me","artifacts":[{"title":"a","content":"b"}]}',
  });

  assert.deepEqual(presentAiAgentEventDetail(source, "# Read me"), {
    kind: "markdown",
    language: "markdown",
    content: "# Read me",
  });
});

test("tool detail component uses typed renderers without injecting raw HTML", async () => {
  const source = await readFile(
    new URL("../src/components/ai/AiAgentEventDetail.vue", import.meta.url),
    "utf8",
  );

  assert.match(source, /presentAiAgentEventDetail/);
  assert.match(source, /<MarkdownPreview/);
  assert.match(source, /<pre><code/);
  assert.doesNotMatch(source, /v-html/);
});
