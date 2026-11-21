import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

import {
  AI_STREAM_IDLE_TIMEOUT_MESSAGE,
  AI_STREAM_IDLE_TIMEOUT_MS,
  createAiStreamIdleWatchdog,
  isAiStreamIdleTimeoutError,
} from "../src/domain/ai/stream-idle-watchdog.ts";

function wait(ms) {
  return new Promise((resolve) => globalThis.setTimeout(resolve, ms));
}

test("AI stream idle watchdog expires and cancels the provider", async () => {
  let cancelled = 0;
  const watchdog = createAiStreamIdleWatchdog(() => {
    cancelled += 1;
  }, 10);

  await assert.rejects(watchdog.expired, isAiStreamIdleTimeoutError);
  assert.equal(cancelled, 1);
  watchdog.dispose();
});

test("AI stream activity resets the idle timeout", async () => {
  let cancelled = 0;
  const watchdog = createAiStreamIdleWatchdog(() => {
    cancelled += 1;
  }, 25);

  await wait(15);
  watchdog.touch();
  await wait(15);
  assert.equal(cancelled, 0);
  watchdog.dispose();
});

test("AI stream activity invalidates an already queued timeout callback", async () => {
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  const callbacks = [];
  globalThis.setTimeout = (callback) => {
    callbacks.push(callback);
    return callbacks.length;
  };
  globalThis.clearTimeout = () => {};

  let cancelled = 0;
  try {
    const watchdog = createAiStreamIdleWatchdog(() => {
      cancelled += 1;
    }, 25);
    void watchdog.expired.catch(() => undefined);

    watchdog.touch();
    callbacks[0]();
    await Promise.resolve();
    assert.equal(cancelled, 0);

    callbacks.at(-1)();
    await Promise.resolve();
    assert.equal(cancelled, 1);
    watchdog.dispose();
  } finally {
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

test("AI stream idle timeout uses a five-minute production threshold", () => {
  assert.equal(AI_STREAM_IDLE_TIMEOUT_MS, 300_000);
});

test("AI stream idle timeout recognizes the backend error message", () => {
  assert.equal(isAiStreamIdleTimeoutError(new Error(AI_STREAM_IDLE_TIMEOUT_MESSAGE)), true);
});

test("AI loop snapshots completed tool results before requesting the final response", async () => {
  const loopSource = await readFile(new URL("../src/services/ai-agent-loop.ts", import.meta.url), "utf8");
  const toolResultStart = loopSource.indexOf("for (const [index, call] of businessCalls.entries())");
  const nextTurnStart = loopSource.indexOf("if (completionSummary)", toolResultStart);
  const completedToolResultFlow = loopSource.slice(toolResultStart, nextTurnStart);

  assert.ok(toolResultStart >= 0);
  assert.ok(nextTurnStart > toolResultStart);
  assert.match(completedToolResultFlow, /history\.push\(\{[\s\S]*role:\s*"tool"/);
  assert.match(completedToolResultFlow, /notifyHistoryChange\(\)/);
});

test("AI event transport preserves canonical backend stream errors", async () => {
  const loopSource = await readFile(new URL("../src/services/ai-agent-loop.ts", import.meta.url), "utf8");
  const transportStart = loopSource.indexOf("async function streamAiLoopTurnViaEvents");
  const transportEnd = loopSource.indexOf("async function streamAiLoopTurnViaStream", transportStart);
  const eventTransport = loopSource.slice(transportStart, transportEnd);

  assert.match(eventTransport, /catch \(error\) \{[\s\S]*if \(eventError\) throw eventError;[\s\S]*throw error;/);
});
