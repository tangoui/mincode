import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { createPinia, setActivePinia } from "pinia";

const read = (path) => readFile(new URL(path, import.meta.url), "utf8");
const [app, sidebar, tabBar, commandPalette, taskButton, shell, store] = await Promise.all([
  read("../src/App.vue"),
  read("../src/components/layout/AppSidebar.vue"),
  read("../src/components/layout/AppTabBar.vue"),
  read("../src/composables/use-command-palette.ts"),
  read("../src/components/layout/AiTaskProgressButton.vue"),
  read("../src/stores/ai-assistant-shell.ts"),
  read("../src/stores/ai-assistant.ts"),
]);

const staticFullStoreImport = /^import\s+\{[^}]*\}\s+from\s+"@\/stores\/ai-assistant";/m;

test("启动壳层入口不静态导入完整 AI 会话 Store", () => {
  for (const [name, source] of Object.entries({ app, sidebar, tabBar, commandPalette, taskButton })) {
    assert.doesNotMatch(source, staticFullStoreImport, name);
  }
  assert.doesNotMatch(app, /^import\s+\{[^}]*\}\s+from\s+"@\/stores\/ai-pending-edits";/m);
  assert.match(shell, /import\("@\/stores\/ai-assistant"\)/);
  assert.match(shell, /^import type \{[^}]*\} from "@\/stores\/ai-assistant";/m);
});

test("完整 Store 与壳层共享抽屉 UI 状态并登记自身实例", () => {
  assert.match(store, /registerAiAssistantStore\(useAiAssistantStore\)/);
  assert.match(
    store,
    /const \{ open, fullscreen, layoutMode, placement, width \} = storeToRefs\(useAiAssistantShellStore\(\)\)/,
  );
  assert.doesNotMatch(store, /localStorage\.getItem\(AI_ASSISTANT_LAYOUT_MODE_KEY\)/);
});

test("壳层 show/toggle 委托给已登记的完整 Store，peek 不会主动加载", async () => {
  setActivePinia(createPinia());
  const module = await import("../src/stores/ai-assistant-shell.ts");
  const shellStore = module.useAiAssistantShellStore();
  assert.equal(shellStore.open, false);
  assert.equal(shellStore.layoutMode, "overlay");
  // 默认以顶部标签页打开 AI；用户选过抽屉时保留其选择。
  assert.equal(shellStore.placement, "tab");
  assert.equal(shellStore.width, 420);
  assert.equal(module.peekAiAssistantStore(), undefined);

  const calls = [];
  const fake = {
    show: (mode) => calls.push(["show", mode]),
    toggle: () => calls.push(["toggle"]),
  };
  module.registerAiAssistantStore(() => fake);
  assert.equal(module.peekAiAssistantStore(), fake);
  assert.equal(await module.loadAiAssistantStore(), fake);
  await shellStore.show("agent");
  await shellStore.show();
  await shellStore.toggle();
  assert.deepEqual(calls, [["show", "agent"], ["show", undefined], ["toggle"]]);

  shellStore.open = true;
  shellStore.fullscreen = true;
  shellStore.close();
  assert.equal(shellStore.open, false);
  assert.equal(shellStore.fullscreen, false);
});

test("壳层启动时读取 JSON 编码的布局模式和位置，并兼容旧版原始字符串", async () => {
  const module = await import("../src/stores/ai-assistant-shell.ts");
  const previous = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  const values = new Map();
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: (key) => values.get(key) ?? null },
  });
  try {
    // savePersistedValue writes JSON.stringify(value).
    values.set("app_ai_layout_mode", JSON.stringify("docked"));
    values.set("app_ai_assistant_placement", JSON.stringify("tab"));
    values.set("app_ai_drawer_width", JSON.stringify(512));
    setActivePinia(createPinia());
    let shellStore = module.useAiAssistantShellStore();
    assert.equal(shellStore.layoutMode, "docked");
    assert.equal(shellStore.placement, "tab");
    assert.equal(shellStore.width, 512);

    values.set("app_ai_layout_mode", "docked");
    values.set("app_ai_assistant_placement", "tab");
    setActivePinia(createPinia());
    shellStore = module.useAiAssistantShellStore();
    assert.equal(shellStore.layoutMode, "docked");
    assert.equal(shellStore.placement, "tab");

    values.set("app_ai_layout_mode", JSON.stringify("unknown"));
    values.set("app_ai_assistant_placement", "{broken");
    setActivePinia(createPinia());
    shellStore = module.useAiAssistantShellStore();
    assert.equal(shellStore.layoutMode, "overlay");
    assert.equal(shellStore.placement, "tab");

    values.set("app_ai_assistant_placement", "drawer");
    setActivePinia(createPinia());
    shellStore = module.useAiAssistantShellStore();
    assert.equal(shellStore.placement, "drawer");
  } finally {
    if (previous) Object.defineProperty(globalThis, "localStorage", previous);
    else delete globalThis.localStorage;
  }
});
