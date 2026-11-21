import assert from "node:assert/strict";
import test from "node:test";

import {
  appendWorkspaceGroupKeys,
  applyVisibleWorkspaceGroupOrder,
  moveWorkspaceGroupKey,
  resolveWorkspaceGroupDropTarget,
  normalizeWorkspaceGroupOrder,
} from "../src/domain/ai/workspace-group-order.ts";

test("工作区顺序会去重并只追加首次出现的分组", () => {
  assert.deepEqual(normalizeWorkspaceGroupOrder(["a", "", "a", 1, "b"]), ["a", "b"]);
  assert.deepEqual(appendWorkspaceGroupKeys(["b", "a"], ["a", "c"]), ["b", "a", "c"]);
});

test("发送消息造成会话更新时间变化时不会改变工作区固定顺序", () => {
  const persistedOrder = ["workspace:min", "workspace:admin"];
  const groupsDiscoveredByRecentConversation = ["workspace:admin", "workspace:min"];
  assert.deepEqual(
    appendWorkspaceGroupKeys(persistedOrder, groupsDiscoveredByRecentConversation),
    persistedOrder,
  );
});

test("拖拽支持放到目标前后且保留当前筛选隐藏的工作区", () => {
  const movedVisible = moveWorkspaceGroupKey(["a", "b", "c"], "c", "a", "before");
  assert.deepEqual(movedVisible, ["c", "a", "b"]);
  assert.deepEqual(applyVisibleWorkspaceGroupOrder(["a", "hidden", "b", "c"], movedVisible), [
    "c",
    "hidden",
    "a",
    "b",
  ]);
  assert.deepEqual(moveWorkspaceGroupKey(["a", "b", "c"], "a", "b", "after"), ["b", "a", "c"]);
});

test("拖拽落点按最近分组标题的中线计算并忽略源分组", () => {
  const candidates = [
    { key: "a", top: 10, bottom: 50 },
    { key: "b", top: 70, bottom: 110 },
    { key: "c", top: 130, bottom: 170 },
  ];
  assert.deepEqual(resolveWorkspaceGroupDropTarget(candidates, "b", 20), {
    key: "a",
    position: "before",
  });
  assert.deepEqual(resolveWorkspaceGroupDropTarget(candidates, "b", 55), {
    key: "a",
    position: "after",
  });
  assert.equal(resolveWorkspaceGroupDropTarget(candidates, "b", 90), undefined);
  assert.deepEqual(resolveWorkspaceGroupDropTarget(candidates, "b", 160), {
    key: "c",
    position: "after",
  });
  assert.equal(resolveWorkspaceGroupDropTarget([{ key: "a", top: 0, bottom: 40 }], "a", 20), undefined);
});
