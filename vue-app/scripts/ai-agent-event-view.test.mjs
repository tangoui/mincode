import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { aiAgentEventView } from "../src/domain/ai/agent-event-view.ts";

function event(overrides = {}) {
  return {
    id: "event-1",
    kind: "tool",
    title: "search",
    toolName: "workspace.grep",
    status: "completed",
    detail: '{"summary":"done","steps":[{"title":"Scan","status":"completed"}]}',
    ...overrides,
  };
}

test("event views are parsed once per event and reused across renders", () => {
  const item = event();
  const first = aiAgentEventView(item);
  assert.equal(aiAgentEventView(item), first);
  assert.equal(aiAgentEventView(item).presentation, first.presentation);
  assert.equal(first.presentation.text, "done");
  assert.equal(first.presentation.steps.length, 1);
  assert.equal(first.hasPresentationDetail, true);
  assert.equal(first.activity.kind, "searchCode");
});

test("in-place updates to streamed detail or status invalidate the cached view", () => {
  const item = event({ detail: "partial", status: "running" });
  const first = aiAgentEventView(item);
  item.detail = "partial output";
  const second = aiAgentEventView(item);
  assert.notEqual(second, first);
  assert.equal(second.presentation.text, "partial output");
  item.status = "failed";
  assert.notEqual(aiAgentEventView(item), second);
});

test("formatted detail is computed lazily and pretty-prints JSON results", () => {
  const view = aiAgentEventView(event({ detail: '{"a":1}' }));
  assert.equal(view.formattedDetail, '{\n  "a": 1\n}');
  assert.equal(view.formattedDetail, view.formattedDetail);
});

test("the event group reads cached views and leaves the per-second clock to a child", async () => {
  const [group, elapsed] = await Promise.all([
    readFile(new URL("../src/components/ai/AiAgentEventGroup.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/components/ai/AiAgentEventElapsed.vue", import.meta.url), "utf8"),
  ]);
  assert.doesNotMatch(group, /parseAiEventPresentation\(|describeAiAgentEventActivity\(|formatAiAgentEventDetail\(/);
  assert.doesNotMatch(group, /setInterval|timingNow/);
  assert.match(group, /aiAgentEventView\(event\)/);
  assert.match(elapsed, /watch\(running,[\s\S]*?setInterval/);
});
