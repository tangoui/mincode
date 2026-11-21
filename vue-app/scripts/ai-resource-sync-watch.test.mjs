import assert from "node:assert/strict";
import test from "node:test";
import { nextTick, reactive, ref, watch } from "vue";

/**
 * 锁定 AI 原生资源同步监听的契约：
 * - 整体替换引用的源（终端 root、sessionIds 等）浅监听即可感知；
 * - 原地 push/splice/索引替换的源（connections/profiles/sessions）
 *   getter 浅监听感知不到，必须保留 deep。
 */

test("shallow watch fires when the source reference is replaced wholesale", async () => {
  const root = ref({ id: "terminal-1" });
  let fired = 0;
  const stop = watch(() => root.value, () => {
    fired += 1;
  });

  root.value = { id: "terminal-2" };
  await nextTick();
  assert.equal(fired, 1);
  stop();
});

test("shallow watch misses in-place push, deep watch catches it", async () => {
  const list = reactive([]);
  let shallowFired = 0;
  let deepFired = 0;
  const stopShallow = watch(() => list, () => {
    shallowFired += 1;
  });
  const stopDeep = watch(() => list, () => {
    deepFired += 1;
  }, { deep: true });

  list.push({ id: "profile-1" });
  await nextTick();
  assert.equal(shallowFired, 0, "getter shallow watch cannot see in-place push");
  assert.equal(deepFired, 1, "deep watch must catch in-place push");
  stopShallow();
  stopDeep();
});

test("shallow watch misses index replacement, deep watch catches it", async () => {
  const list = reactive([{ id: "connection-1" }]);
  let shallowFired = 0;
  let deepFired = 0;
  const stopShallow = watch(() => list, () => {
    shallowFired += 1;
  });
  const stopDeep = watch(() => list, () => {
    deepFired += 1;
  }, { deep: true });

  list[0] = { id: "connection-1", name: "updated" };
  await nextTick();
  assert.equal(shallowFired, 0, "getter shallow watch cannot see index replacement");
  assert.equal(deepFired, 1, "deep watch must catch index replacement");
  stopShallow();
  stopDeep();
});
