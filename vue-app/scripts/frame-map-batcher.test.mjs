import assert from "node:assert/strict";
import test from "node:test";

import { createFrameMapBatcher } from "../src/domain/async/frame-map-batcher.ts";

function manualQueue() {
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

function batcher() {
  const frames = manualQueue();
  const timers = manualQueue();
  const commits = [];
  const instance = createFrameMapBatcher((entries) => commits.push(entries), {
    scheduleFrame: frames.schedule,
    cancelFrame: frames.cancel,
    scheduleTimer: timers.schedule,
    cancelTimer: timers.cancel,
  });
  return { frames, timers, commits, instance };
}

test("同一帧内到达的多项结果合并为一次提交", () => {
  const { frames, commits, instance } = batcher();
  instance.set("a", 1);
  instance.set("b", 2);
  instance.set("a", 3);
  assert.equal(commits.length, 0);
  assert.equal(frames.size, 1);
  frames.run();
  assert.deepEqual(commits, [[["a", 3], ["b", 2]]]);
});

test("提交前可读到待提交的值，删除后不再提交", () => {
  const { frames, commits, instance } = batcher();
  instance.set("a", 1);
  instance.set("b", 2);
  assert.equal(instance.get("a"), 1);
  instance.delete("a");
  assert.equal(instance.get("a"), undefined);
  frames.run();
  assert.deepEqual(commits, [[["b", 2]]]);
});

test("渲染帧暂停（窗口隐藏）时由兜底计时器提交，并取消帧回调", () => {
  const { frames, timers, commits, instance } = batcher();
  instance.set("a", 1);
  timers.run();
  assert.deepEqual(commits, [[["a", 1]]]);
  assert.equal(frames.size, 0);
});

test("清空与销毁不提交已排队的值", () => {
  const { frames, timers, commits, instance } = batcher();
  instance.set("a", 1);
  instance.clear();
  frames.run();
  timers.run();
  instance.set("b", 2);
  instance.dispose();
  frames.run();
  timers.run();
  instance.set("c", 3);
  frames.run();
  assert.deepEqual(commits, []);
});

test("flush 立即提交并取消已排的回调", () => {
  const { frames, timers, commits, instance } = batcher();
  instance.set("a", 1);
  instance.flush();
  assert.deepEqual(commits, [[["a", 1]]]);
  assert.equal(frames.size, 0);
  assert.equal(timers.size, 0);
  instance.flush();
  assert.equal(commits.length, 1);
});
