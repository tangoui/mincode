import assert from "node:assert/strict";
import test from "node:test";
import { createAiTerminalSessionReuseRegistry } from "../src/domain/ai/terminal-session-reuse.ts";

test("reuses the live terminal remembered for the same AI conversation", () => {
  const registry = createAiTerminalSessionReuseRegistry();
  registry.remember("conversation-1", { tabId: "tab-1", paneId: "pane-1" });

  assert.deepEqual(
    registry.resolve("conversation-1", ({ paneId }) => paneId === "pane-1"),
    { tabId: "tab-1", paneId: "pane-1" }
  );
  assert.equal(registry.resolve("conversation-2", () => true), undefined);
});

test("forgets a terminal reference after its tab or pane is no longer available", () => {
  const registry = createAiTerminalSessionReuseRegistry();
  registry.remember("conversation-1", { tabId: "tab-1", paneId: "pane-1" });

  assert.equal(registry.resolve("conversation-1", () => false), undefined);
  assert.equal(registry.resolve("conversation-1", () => true), undefined);
});
