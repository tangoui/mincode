import assert from "node:assert/strict";
import test from "node:test";

import { parseSessionDeepLink } from "../src/domain/ai/session-deep-link.ts";

test("session deep links require both a supported provider and stable session id", () => {
  assert.deepEqual(parseSessionDeepLink({}), { kind: "none" });
  assert.deepEqual(parseSessionDeepLink({ provider: "codex" }), {
    kind: "invalid",
  });
  assert.deepEqual(
    parseSessionDeepLink({ provider: "unknown", sessionId: "session-1" }),
    {
      kind: "invalid",
    },
  );
  assert.deepEqual(
    parseSessionDeepLink({ provider: " codex ", sessionId: " session-1 " }),
    {
      kind: "valid",
      provider: "codex",
      sessionId: "session-1",
    },
  );
  assert.deepEqual(
    parseSessionDeepLink({ provider: "pi", sessionId: "pi-session-1" }),
    {
      kind: "valid",
      provider: "pi",
      sessionId: "pi-session-1",
    },
  );
  assert.deepEqual(
    parseSessionDeepLink({ provider: "grok", sessionId: " grok-session " }),
    {
      kind: "valid",
      provider: "grok",
      sessionId: "grok-session",
    },
  );
  assert.deepEqual(
    parseSessionDeepLink({ provider: "qodercn", sessionId: "qoder-cn-session" }),
    {
      kind: "valid",
      provider: "qodercn",
      sessionId: "qoder-cn-session",
    },
  );
});

test("ambiguous repeated query values are rejected", () => {
  assert.deepEqual(
    parseSessionDeepLink({
      provider: ["codex", "claude"],
      sessionId: "session-1",
    }),
    {
      kind: "invalid",
    },
  );
});
