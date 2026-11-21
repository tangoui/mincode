import assert from "node:assert/strict";
import test from "node:test";
import { openPluginViewTab } from "../src/domain/workspace/open-plugin-view.ts";

function fakeRouter(pushes) {
  return {
    push(target) {
      pushes.push(target);
      return Promise.resolve();
    },
  };
}

test("打开插件视图同时激活标签并推送插件路由", () => {
  const tab = {
    id: "workspace:plugin-view:prd-studio:main",
    workspaceId: "workspace",
    view: "plugin-view",
    titleKey: "plugins.viewTitle",
    closable: true,
    pluginView: { pluginId: "prd-studio", viewId: "main" },
  };
  const calls = [];
  const pushes = [];
  const result = openPluginViewTab(
    {
      openPluginView(pluginId, viewId, title) {
        calls.push([pluginId, viewId, title]);
        return tab;
      },
    },
    fakeRouter(pushes),
    "prd-studio",
    "main",
    "PRD 工作台",
  );
  assert.equal(result, tab);
  assert.deepEqual(calls, [["prd-studio", "main", "PRD 工作台"]]);
  assert.deepEqual(pushes, [
    { name: "plugin-view", params: { pluginId: "prd-studio", viewId: "main" } },
  ]);
});

test("标签创建失败时不推送路由", () => {
  const pushes = [];
  const result = openPluginViewTab(
    { openPluginView: () => undefined },
    fakeRouter(pushes),
    "",
    "",
    "无效",
  );
  assert.equal(result, undefined);
  assert.deepEqual(pushes, []);
});
