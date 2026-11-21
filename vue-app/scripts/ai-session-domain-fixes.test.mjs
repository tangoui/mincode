import assert from "node:assert/strict";
import test from "node:test";

import { createOperationManager } from "../src/domain/ai/operation-manager.ts";
import { normalizedProjectRoot, stagePendingEdit } from "../src/domain/ai/pending-edits.ts";

function idFactory(prefix) {
  let next = 0;
  return () => `${prefix}-${++next}`;
}

test("stagePendingEdit 延续未处理的修改并保留最初的 before", () => {
  const createId = idFactory("edit");
  const first = stagePendingEdit([], { path: "a.ts", projectRoot: "/repo/", before: "v0", after: "v1" }, createId, () => 1);
  const second = stagePendingEdit(first.edits, { path: "a.ts", projectRoot: "/repo", before: "v1", after: "v2" }, createId, () => 2);

  assert.equal(second.edits.length, 1);
  assert.equal(second.edit.id, first.edit.id);
  assert.equal(second.edit.before, "v0");
  assert.equal(second.edit.after, "v2");
  assert.equal(second.edit.projectRoot, "/repo");
});

test("stagePendingEdit 用新条目替换已拒绝或失败的旧修改", () => {
  const createId = idFactory("edit");
  for (const status of ["rejected", "failed"]) {
    const staged = stagePendingEdit([], { path: "a.ts", projectRoot: "/repo", before: "v0", after: "v1" }, createId);
    const finished = [{ ...staged.edit, status, error: "boom" }];
    const next = stagePendingEdit(finished, { path: "a.ts", projectRoot: "/repo", before: "disk", after: "v2" }, createId);

    assert.equal(next.edits.length, 1);
    assert.notEqual(next.edit.id, staged.edit.id);
    assert.equal(next.edit.before, "disk");
    assert.equal(next.edit.status, "pending");
    assert.equal(next.edit.error, undefined);
  }
});

test("normalizedProjectRoot 统一分隔符并去掉末尾斜杠", () => {
  assert.equal(normalizedProjectRoot(" C:\\repo\\ "), "C:/repo");
  assert.equal(normalizedProjectRoot("/"), "/");
});

test("操作管理器在操作结束或排队取消后不再保留记录", async () => {
  const manager = createOperationManager({ createId: idFactory("op") });
  let release;
  const blocker = new Promise((resolve) => {
    release = resolve;
  });
  const running = manager.start({ resourceId: "file", capability: "write", mode: "write", run: () => blocker });
  const queued = manager.start({ resourceId: "file", capability: "write", mode: "write", run: () => "unused" });
  assert.equal(manager.list().length, 2);

  queued.cancel();
  await queued.done;
  assert.equal(queued.snapshot().state, "cancelled");
  assert.deepEqual(manager.list().map((item) => item.id), [running.snapshot().id]);

  release("ok");
  await running.done;
  assert.equal(running.snapshot().state, "succeeded");
  assert.equal(running.snapshot().result, "ok");
  assert.equal(manager.list().length, 0);
});
