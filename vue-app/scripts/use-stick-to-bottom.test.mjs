import assert from "node:assert/strict";
import test from "node:test";
import { effectScope, nextTick, ref } from "vue";

// No layout in Node: a fake scroll host and a ResizeObserver whose callbacks the test fires.
const observers = [];
globalThis.ResizeObserver = class {
  constructor(callback) {
    this.callback = callback;
    this.targets = new Set();
    observers.push(this);
  }
  observe(target) { this.targets.add(target); }
  disconnect() { this.targets.clear(); this.disconnected = true; }
};
function fakeHost({ scrollHeight = 1000, clientHeight = 400, scrollTop = 600 } = {}) {
  const content = { nodeName: "VIEW" };
  return { scrollHeight, clientHeight, scrollTop, firstElementChild: content };
}
const resize = () => observers.filter((observer) => !observer.disconnected).forEach((observer) => observer.callback([]));

const { useStickToBottom } = await import("../src/composables/use-stick-to-bottom.ts");

test("贴底时内容变高会保持贴底，并观察内容与视口两者", async () => {
  observers.length = 0;
  const host = ref(fakeHost());
  let aligned = 0;
  const scope = effectScope();
  scope.run(() => useStickToBottom({ host, following: () => true, onAligned: () => { aligned += 1; } }));
  await nextTick();
  const observer = observers.at(-1);
  assert.ok(observer.targets.has(host.value));
  assert.ok(observer.targets.has(host.value.firstElementChild));
  host.value.scrollHeight = 1300;
  resize();
  assert.equal(host.value.scrollTop, 900);
  assert.equal(aligned, 1);
  scope.stop();
  assert.equal(observer.disconnected, true);
});

test("用户已往上翻时不跟随", async () => {
  observers.length = 0;
  const host = ref(fakeHost({ scrollTop: 100 }));
  const scope = effectScope();
  scope.run(() => useStickToBottom({ host, following: () => false }));
  await nextTick();
  host.value.scrollHeight = 1500;
  resize();
  assert.equal(host.value.scrollTop, 100);
  scope.stop();
});

test("滚动容器替换后改为观察新的容器", async () => {
  observers.length = 0;
  const host = ref(fakeHost());
  const scope = effectScope();
  scope.run(() => useStickToBottom({ host, following: () => true }));
  await nextTick();
  const first = observers.at(-1);
  host.value = fakeHost({ scrollHeight: 2000, scrollTop: 0 });
  await nextTick();
  assert.equal(first.disconnected, true);
  resize();
  assert.equal(host.value.scrollTop, 1600);
  scope.stop();
});
