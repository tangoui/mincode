import assert from "node:assert/strict";
import test from "node:test";

const { aiRunStatusText } = await import("../src/domain/ai/run-status.ts");

const labels = {
  processing: "AI 正在处理，请稍候…",
  waitingApproval: "等待操作审批",
  runningTool: (tool) => `正在运行工具：${tool}`,
};

test("等待批准优先于其他状态", () => {
  assert.equal(aiRunStatusText({ waitingApproval: true, live: { phase: "tool_running", summary: "正在运行工具：写入" } }, labels), "等待操作审批");
});

test("本地 Agent 没有工具阶段上报，用正在运行的工具事件", () => {
  assert.equal(aiRunStatusText({ waitingApproval: false, runningToolTitle: "读取文件", live: { phase: "thinking", summary: "正在思考" } }, labels), "正在运行工具：读取文件");
});

test("运行中的阶段显示其说明；结束或没有说明时显示处理中", () => {
  assert.equal(aiRunStatusText({ waitingApproval: false, live: { phase: "streaming", summary: "正在生成回复" } }, labels), "正在生成回复");
  assert.equal(aiRunStatusText({ waitingApproval: false, live: { phase: "completed", summary: "任务已完成" } }, labels), "AI 正在处理，请稍候…");
  assert.equal(aiRunStatusText({ waitingApproval: false, live: { phase: "thinking", summary: "" } }, labels), "AI 正在处理，请稍候…");
  assert.equal(aiRunStatusText({ waitingApproval: false }, labels), "AI 正在处理，请稍候…");
});
