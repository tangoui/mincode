import assert from "node:assert/strict";
import test from "node:test";
import { createTerminalResizeSync } from "../src/domain/terminal/resize-sync.ts";

function createHarness(initial = { cols: 80, rows: 24 }) {
  const frames = new Map();
  let nextFrame = 1;
  const state = { size: initial, ready: true, measures: 0, sent: [], failNext: false };
  const sync = createTerminalResizeSync({
    measure() {
      state.measures += 1;
      return state.size;
    },
    canSend: () => state.ready,
    async send(dimensions) {
      if (state.failNext) {
        state.failNext = false;
        throw new Error("resize failed");
      }
      state.sent.push(dimensions);
    },
    requestFrame(callback) {
      const handle = nextFrame++;
      frames.set(handle, callback);
      return handle;
    },
    cancelFrame: (handle) => frames.delete(handle),
  });
  const runFrames = () => {
    const pending = [...frames.values()];
    frames.clear();
    for (const callback of pending) callback();
  };
  return { sync, state, runFrames, frames };
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

test("continuous resize notifications fit once per frame", async () => {
  const { sync, state, runFrames, frames } = createHarness();
  for (let index = 0; index < 10; index += 1) sync.schedule();
  assert.equal(frames.size, 1);
  assert.equal(state.measures, 0);
  runFrames();
  await settle();
  assert.equal(state.measures, 1);
  assert.deepEqual(state.sent, [{ cols: 80, rows: 24 }]);
});

test("unchanged cols and rows are not re-sent", async () => {
  const { sync, state, runFrames } = createHarness();
  sync.flush();
  sync.flush();
  sync.schedule();
  runFrames();
  await settle();
  assert.equal(state.sent.length, 1);
  state.size = { cols: 100, rows: 24 };
  sync.flush();
  await settle();
  assert.deepEqual(state.sent, [{ cols: 80, rows: 24 }, { cols: 100, rows: 24 }]);
});

test("reset re-sends the current size after connect or reconnect", async () => {
  const { sync, state } = createHarness();
  sync.flush();
  sync.reset();
  sync.flush();
  await settle();
  assert.equal(state.sent.length, 2);
});

test("nothing is recorded as sent before the backend is ready or without a layout", async () => {
  const { sync, state } = createHarness();
  state.ready = false;
  sync.flush();
  state.ready = true;
  state.size = null;
  sync.flush();
  state.size = { cols: 90, rows: 30 };
  sync.flush();
  await settle();
  assert.deepEqual(state.sent, [{ cols: 90, rows: 30 }]);
});

test("a failed send is retried on the next flush", async () => {
  const { sync, state } = createHarness();
  state.failNext = true;
  sync.flush();
  await settle();
  sync.flush();
  await settle();
  assert.deepEqual(state.sent, [{ cols: 80, rows: 24 }]);
});

test("flush cancels a pending frame and dispose stops further work", async () => {
  const { sync, state, frames } = createHarness();
  sync.schedule();
  sync.flush();
  assert.equal(frames.size, 0);
  sync.dispose();
  state.size = { cols: 120, rows: 40 };
  sync.schedule();
  sync.flush();
  await settle();
  assert.equal(frames.size, 0);
  assert.deepEqual(state.sent, [{ cols: 80, rows: 24 }]);
});
