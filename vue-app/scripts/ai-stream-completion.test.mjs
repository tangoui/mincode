import assert from "node:assert/strict";
import test from "node:test";

import {
  AiStreamConnectionError,
  createAiStreamCompletion,
} from "../src/domain/ai/stream-completion.ts";

test("AI stream requires a terminal frame before disconnect", async () => {
  const completion = createAiStreamCompletion(() => {}, 1_000);
  completion.opened();
  completion.disconnected();

  await assert.rejects(
    completion.completed,
    (error) => error instanceof AiStreamConnectionError && error.reason === "interrupted",
  );
  completion.dispose();
});

test("AI stream connection timeout fails and cancels the provider", async () => {
  let cancelled = 0;
  const completion = createAiStreamCompletion(() => {
    cancelled += 1;
  }, 10);

  await assert.rejects(
    completion.completed,
    (error) => error instanceof AiStreamConnectionError && error.reason === "connect_timeout",
  );
  assert.equal(cancelled, 1);
  completion.dispose();
});

test("AI stream done terminal frame completes successfully", async () => {
  const completion = createAiStreamCompletion(() => {}, 1_000);
  completion.opened();
  completion.terminal();

  await completion.completed;
  completion.disconnected();
  completion.dispose();
});

test("AI stream preserves backend business errors", async () => {
  const completion = createAiStreamCompletion(() => {}, 1_000);
  const backendError = new Error("provider rejected the request");
  completion.opened();
  completion.terminal(backendError);

  await assert.rejects(completion.completed, (error) => error === backendError);
  completion.dispose();
});

test("AI stream cancellation releases the caller without a transport error", async () => {
  const completion = createAiStreamCompletion(() => {}, 1_000);
  completion.cancel();

  await completion.completed;
  completion.dispose();
});

test("pre-open disconnect is distinct from timeout", async () => {
  const completion = createAiStreamCompletion(() => {}, 1000);
  completion.disconnected();
  await assert.rejects(completion.completed, { reason: "connect_closed" });
  assert.equal(completion.opened(), false);
});

test("late or duplicate open cannot send a request", async () => {
  const timedOut = createAiStreamCompletion(() => {}, 5);
  await assert.rejects(timedOut.completed, { reason: "connect_timeout" });
  assert.equal(timedOut.opened(), false);
  const cancelled = createAiStreamCompletion(() => {}, 1000);
  cancelled.cancel();
  assert.equal(cancelled.opened(), false);
  const connected = createAiStreamCompletion(() => {}, 1000);
  assert.equal(connected.opened(), true);
  assert.equal(connected.opened(), false);
  connected.terminal();
  await connected.completed;
});

test("handshake retries once but never replays an opened or business-failed request", async () => {
  const { retryAiStreamConnection } = await import("../src/domain/ai/stream-completion.ts");
  for (const reason of ["connect_timeout", "connect_closed"]) {
    let calls = 0;
    const diagnostics = [];
    await retryAiStreamConnection(async () => {
      if (++calls === 1) throw new AiStreamConnectionError(reason);
    }, undefined, (error, attempt) => diagnostics.push([error.reason, attempt]));
    assert.equal(calls, 2);
    assert.deepEqual(diagnostics, [[reason, 1]]);
    calls = 0;
    await assert.rejects(retryAiStreamConnection(async () => {
      calls++;
      throw new AiStreamConnectionError(reason);
    }), { reason });
    assert.equal(calls, 2);
  }
  for (const error of [new AiStreamConnectionError("interrupted"), new Error("provider error")]) {
    let calls = 0;
    await assert.rejects(retryAiStreamConnection(async () => { calls++; throw error; }), e => e === error);
    assert.equal(calls, 1);
  }
});

test("cancellation stops handshake retry", async () => {
  const { retryAiStreamConnection } = await import("../src/domain/ai/stream-completion.ts");
  const controller = new AbortController();
  let calls = 0;
  await assert.rejects(retryAiStreamConnection(async () => {
    calls++;
    controller.abort();
    throw new AiStreamConnectionError("connect_timeout");
  }, controller.signal), { name: "AbortError" });
  assert.equal(calls, 1);
});
