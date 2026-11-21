import assert from "node:assert/strict";
import test from "node:test";

import {
  DEFAULT_AI_TOOL_MODULE_KEYWORDS,
  isComputerUseIntent,
  isDirectConversationIntent,
  selectAiTools,
} from "../src/domain/ai/tool-selection.ts";
import { planMcpToolLoading } from "../src/domain/ai/mcp-tool-loading.ts";

const registeredTools = [
  {
    name: "workspace.describe_current_project",
    module: "workspace",
    discovery: true,
  },
  { name: "workspace.open_view", module: "workspace" },
  { name: "browser.navigate", module: "browser" },
  { name: "computer.get_status", module: "computer", discovery: true },
  { name: "computer.capture_screen", module: "computer" },
  { name: "computer.open_app", module: "computer" },
  { name: "computer.open_url", module: "computer" },
  { name: "computer.execute", module: "computer" },
];

const availableModules = ["workspace", "browser", "computer"];

function selectedToolNames(text) {
  return selectAiTools(registeredTools, {
    text,
    availableModules,
    moduleKeywords: DEFAULT_AI_TOOL_MODULE_KEYWORDS,
  }).map((tool) => tool.name);
}

test("selects desktop Computer Use tools for explicit local Chrome requests", () => {
  for (const text of [
    "我希望打开电脑上的谷歌浏览器并打开百度",
    "我希望打开电脑上的谷歌流览器并打开百度",
    "打开电脑上的 Chrome",
    "启动本地 Chrome 应用",
    "Open Google Chrome on my computer",
  ]) {
    assert.equal(isComputerUseIntent(text), true, text);
    assert.ok(
      selectedToolNames(text).includes("computer.capture_screen"),
      text,
    );
    assert.ok(selectedToolNames(text).includes("computer.open_app"), text);
    assert.ok(selectedToolNames(text).includes("computer.open_url"), text);
    assert.ok(selectedToolNames(text).includes("computer.execute"), text);
  }
});

test("does not expose desktop actions for an internal-browser-only request", () => {
  const names = selectedToolNames("在 MinCode 内置浏览器打开百度网页");
  assert.ok(names.includes("browser.navigate"));
  assert.ok(names.includes("computer.get_status"));
  assert.equal(names.includes("computer.capture_screen"), false);
  assert.equal(names.includes("computer.open_app"), false);
  assert.equal(names.includes("computer.open_url"), false);
  assert.equal(names.includes("computer.execute"), false);
});

test("current-project questions expose the side-effect-free project descriptor", () => {
  for (const text of [
    "现在是什么项目",
    "我当前在哪个项目",
    "what project am I in",
  ]) {
    const names = selectedToolNames(text);
    assert.ok(names.includes("workspace.describe_current_project"), text);
  }
});

test("does not treat a generic start request as desktop control", () => {
  const names = selectedToolNames("启动自动化任务");
  assert.equal(names.includes("computer.capture_screen"), false);
  assert.equal(names.includes("computer.open_app"), false);
  assert.equal(names.includes("computer.open_url"), false);
  assert.equal(names.includes("computer.execute"), false);
});

test("does not expose tools for direct conversational turns", () => {
  for (const text of ["你好", "您好！", "hello", "Hi there!", "谢谢你"]) {
    assert.equal(isDirectConversationIntent(text), true, text);
    const selected = selectAiTools(registeredTools, {
      text,
      currentModule: "workspace",
      availableModules,
      historicalModules: ["browser"],
      moduleKeywords: DEFAULT_AI_TOOL_MODULE_KEYWORDS,
    });
    assert.deepEqual(selected, [], text);
    assert.deepEqual(
      planMcpToolLoading({ text, serverIds: ["github"], historyServerIds: ["github"] }),
      { offerLoader: false, preload: [] },
      text,
    );
  }
});

test("keeps tools for greetings that also contain an actionable request", () => {
  const text = "你好，请帮我打开当前项目文件";
  assert.equal(isDirectConversationIntent(text), false);
  const names = selectedToolNames(text);
  assert.ok(names.includes("workspace.open_view"));
});
