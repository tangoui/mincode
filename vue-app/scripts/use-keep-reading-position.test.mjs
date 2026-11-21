import assert from "node:assert/strict";
import test from "node:test";
import { effectScope, nextTick, ref } from "vue";

const observers = [];
globalThis.ResizeObserver = class {
  constructor(callback) { this.callback = callback; observers.push(this); }
  observe(target) { this.target = target; }
  disconnect() { this.disconnected = true; }
};

// The host shows y = scrollTop … scrollTop + clientHeight of the content; the element sits at `top`.
function setup({ top, height, scrollTop }) {
  const host = { scrollTop, clientHeight: 400, getBoundingClientRect: () => ({ top: 0 }) };
  const element = {
    height,
    get offsetHeight() { return this.height; },
    closest: () => host,
    getBoundingClientRect() { return { top: top - host.scrollTop, bottom: top + this.height - host.scrollTop }; },
  };
  return { host, element };
}
const { useKeepReadingPosition } = await import("../src/composables/use-keep-reading-position.ts");

async function mounted(element) {
  const target = ref(element);
  const scope = effectScope();
  scope.run(() => useKeepReadingPosition(target));
  await nextTick();
  return { scope, resize: () => observers.at(-1).callback([]) };
}

test("元素在可视区上方变矮时，滚动位置随之上移，正在读的内容不动", async () => {
  const { host, element } = setup({ top: 100, height: 300, scrollTop: 900 });
  const { scope, resize } = await mounted(element);
  element.height = 40;
  resize();
  assert.equal(host.scrollTop, 640);
  scope.stop();
});

test("元素在可视区内或下方时不调整", async () => {
  const { host, element } = setup({ top: 950, height: 300, scrollTop: 900 });
  const { scope, resize } = await mounted(element);
  element.height = 40;
  resize();
  assert.equal(host.scrollTop, 900);
  scope.stop();
});

test("上方元素变高时同样补偿", async () => {
  const { host, element } = setup({ top: 100, height: 40, scrollTop: 900 });
  const { scope, resize } = await mounted(element);
  element.height = 300;
  resize();
  assert.equal(host.scrollTop, 1160);
  scope.stop();
});

test("停在底部跟随时不补偿，交给贴底逻辑", async () => {
  const { host, element } = setup({ top: 100, height: 300, scrollTop: 900 });
  host.scrollHeight = 1300; // 900 + 400：正好在底部（内容变矮后浏览器已把位置压回底部）
  const { scope, resize } = await mounted(element);
  element.height = 40;
  resize();
  assert.equal(host.scrollTop, 900);
  scope.stop();
});
