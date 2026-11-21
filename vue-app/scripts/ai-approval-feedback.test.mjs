import assert from "node:assert/strict";
import test from "node:test";
import { declinedToolResult } from "../src/domain/ai/approval-feedback.ts";

test("declined result includes the user's instruction, or falls back without one", () => {
  const labels = { declined: "declined", declinedWithFeedback: (f) => `declined: ${f}` };
  assert.deepEqual(declinedToolResult("use staging", labels), {
    content: "declined: use staging",
    status: "cancelled",
    redirect: true,
  });
  assert.deepEqual(declinedToolResult(undefined, labels), { content: "declined", status: "cancelled" });
});
