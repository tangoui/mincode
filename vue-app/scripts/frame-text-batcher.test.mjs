import assert from "node:assert/strict";
import test from "node:test";

import { createFrameTextBatcher } from "../src/domain/async/frame-text-batcher.ts";

function manualFrames() {
  let nextId = 1;
  const callbacks = new Map();
  return {
    schedule(callback) {
      const id = nextId++;
      callbacks.set(id, callback);
      return id;
    },
    cancel(id) {
      callbacks.delete(id);
    },
    run() {
      const current = [...callbacks.values()];
      callbacks.clear();
      current.forEach((callback) => callback());
    },
    get size() {
      return callbacks.size;
    },
  };
}

test("同一渲染帧内的文本增量只触发一次更新", () => {
  const frames = manualFrames();
  const applied = [];
  const batcher = createFrameTextBatcher(
    (key, text) => applied.push([key, text]),
    (callback) => frames.schedule(callback),
    (id) => frames.cancel(id),
  );

  batcher.append("message", "你");
  batcher.append("message", "好");
  batcher.append("message", "！");

  assert.equal(frames.size, 1);
  assert.deepEqual(applied, []);
  frames.run();
  assert.deepEqual(applied, [["message", "你好！"]]);
});

test("主动冲刷保持交错片段顺序并取消空帧", () => {
  const frames = manualFrames();
  const applied = [];
  const batcher = createFrameTextBatcher(
    (key, text) => applied.push([key, text]),
    (callback) => frames.schedule(callback),
    (id) => frames.cancel(id),
  );

  batcher.append("thought", "分析");
  batcher.append("message", "结果");
  batcher.append("thought", "中");
  batcher.flush();

  assert.equal(frames.size, 0);
  assert.deepEqual(applied, [
    ["thought", "分析"],
    ["message", "结果"],
    ["thought", "中"],
  ]);
});


test("大量连续 chunk 在一帧内仍只保留一次响应式提交", () => {
  const frames = manualFrames();
  const applied = [];
  const batcher = createFrameTextBatcher(
    (key, text) => applied.push([key, text]),
    (callback) => frames.schedule(callback),
    (id) => frames.cancel(id),
  );

  for (let index = 0; index < 10_000; index += 1) {
    batcher.append("message", "x");
  }
  frames.run();

  assert.equal(applied.length, 1);
  assert.equal(applied[0][1].length, 10_000);
});

test("销毁默认丢弃未提交增量且不再接收新文本", () => {
  const frames = manualFrames();
  const applied = [];
  const batcher = createFrameTextBatcher(
    (key, text) => applied.push([key, text]),
    (callback) => frames.schedule(callback),
    (id) => frames.cancel(id),
  );

  batcher.append("message", "pending");
  batcher.dispose();
  batcher.append("message", "ignored");
  frames.run();

  assert.deepEqual(applied, []);
});
