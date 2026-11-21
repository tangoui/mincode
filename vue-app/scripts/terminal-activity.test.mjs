import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import {
  aggregateTerminalActivity,
  isTerminalActivityActive,
  safeTerminalTaskLabel,
  shouldApplyTerminalActivity,
  staleActivityMs,
  terminalActivityDurationMs,
} from "../src/domain/terminal/terminal-activity.ts";

const startedAt = 1_000;

function activity(phase, updatedAt, extra = {}) {
  return {
    phase,
    source: "generic",
    startedAt,
    updatedAt,
    ...extra,
  };
}

test("terminal activity aggregation prioritizes failures and approval requests", () => {
  assert.equal(
    aggregateTerminalActivity({ a: activity("active", 3_000), b: activity("failed", 2_000) }, startedAt).phase,
    "failed",
  );
  assert.equal(
    aggregateTerminalActivity({ a: activity("active", 2_000), b: activity("waiting_approval", 3_000) }, startedAt).phase,
    "waiting_approval",
  );
});

test("terminal activity only spins while connecting or receiving output", () => {
  assert.equal(isTerminalActivityActive("connecting"), true);
  assert.equal(isTerminalActivityActive("active"), true);
  for (const phase of ["quiet", "waiting_approval", "waiting_input", "completed", "failed"]) {
    assert.equal(isTerminalActivityActive(phase), false, `${phase} must not spin`);
  }
});

test("terminal activity stays active while any split pane is active", () => {
  const result = aggregateTerminalActivity({
    a: activity("waiting_input", 3_000, { source: "claude-hook" }),
    b: activity("active", 2_000),
  }, startedAt, 3_000);
  assert.equal(result.phase, "active");
  assert.equal(result.paneCount, 2);
});

test("a stale active phase stops spinning instead of claiming precise Claude state", () => {
  const stuck = { a: activity("active", 2_000, { source: "claude-hook" }) };
  // Stop 钩子没送达时，最后一条 hook 事件会永远停在 active。
  assert.equal(aggregateTerminalActivity(stuck, startedAt, 2_000 + staleActivityMs).phase, "active");
  const decayed = aggregateTerminalActivity(stuck, startedAt, 2_001 + staleActivityMs);
  assert.equal(decayed.phase, "quiet");
  assert.equal(decayed.source, "generic");
  // 终端仍在输出时不算过期，长任务途中不会被误判成静默。
  assert.equal(
    aggregateTerminalActivity(
      { a: activity("active", 2_000, { source: "claude-hook", lastOutputAt: 40_000 }) },
      startedAt,
      2_001 + staleActivityMs,
    ).phase,
    "active",
  );
  // 从没有面板汇报过状态的标签同样不能一直转圈。
  assert.equal(aggregateTerminalActivity({}, startedAt, startedAt + 1).phase, "connecting");
  assert.equal(aggregateTerminalActivity({}, startedAt, startedAt + staleActivityMs + 1).phase, "quiet");
});

test("terminal activity completes only after every pane exits", () => {
  assert.equal(
    aggregateTerminalActivity({ a: activity("completed", 2_000), b: activity("quiet", 3_000) }, startedAt).phase,
    "quiet",
  );
  assert.equal(
    aggregateTerminalActivity({ a: activity("completed", 2_000), b: activity("completed", 3_000) }, startedAt).phase,
    "completed",
  );
});

test("safe task labels remove terminal controls, redact credentials, and truncate by characters", () => {
  assert.equal(safeTerminalTaskLabel("[31mFix login. Then deploy[0m"), "Fix login.");
  assert.equal(safeTerminalTaskLabel("use api_key=secret-value now"), "use api_key=[REDACTED] now");
  assert.equal(safeTerminalTaskLabel("密码 password=hunter2"), "密码 password=[REDACTED]");
  assert.equal(safeTerminalTaskLabel("中文任务名称", 5), "中文任务…");
});

test("terminal activity rejects stale snapshots and freezes final duration", () => {
  const current = activity("active", 3_000);
  assert.equal(shouldApplyTerminalActivity(current, activity("quiet", 2_000)), false);
  assert.equal(shouldApplyTerminalActivity(current, activity("quiet", 4_000)), true);
  assert.equal(terminalActivityDurationMs(activity("completed", 4_000, { endedAt: 5_000 }), 9_000), 4_000);
});

test("structured Claude state wins even when delayed behind generic output", () => {
  const genericOutput = activity("active", 4_000);
  const delayedClaudeIdle = activity("waiting_input", 3_000, { source: "claude-hook" });
  assert.equal(shouldApplyTerminalActivity(genericOutput, delayedClaudeIdle), true);
});

test("generic output does not overwrite structured Claude state", () => {
  const current = activity("waiting_approval", 3_000, { source: "claude-hook" });
  assert.equal(shouldApplyTerminalActivity(current, activity("active", 4_000)), false);
  assert.equal(shouldApplyTerminalActivity(current, activity("quiet", 4_000)), false);
  assert.equal(shouldApplyTerminalActivity(current, activity("completed", 4_000)), true);
});

const [localTerminalSource, sshTerminalSource, paneTreeSource] = await Promise.all([
  readFile(new URL("../src/components/terminal/LocalTerminal.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/terminal/SshTerminal.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/terminal/TerminalPaneLayout.vue", import.meta.url), "utf8"),
]);

test("a ready blank local shell is quiet until it produces output", () => {
  assert.match(
    localTerminalSource,
    /backendReady = true;\s+status\.value = "ready";\s+emitActivity\(\{ phase: "quiet", updatedAt: Date\.now\(\) \}\);/,
  );
  assert.doesNotMatch(
    localTerminalSource,
    /status\.value = "ready";\s+emitActivity\(\{ phase: "active", updatedAt: Date\.now\(\) \}\);/,
  );
  assert.match(localTerminalSource, /xterm\?\.write\(data\);\s+emitOutputActivity\(\);/);
});

test("ssh terminals report the same generic activity phases as local terminals", () => {
  assert.match(sshTerminalSource, /activity: \[snapshot: LocalTerminalActivity\];/);
  assert.match(sshTerminalSource, /emitActivity\(\{ phase: "connecting", updatedAt: Date\.now\(\) \}\);/);
  assert.match(sshTerminalSource, /appendRecordingFrame\(data\);\s+emitOutputActivity\(\);/);
  assert.match(sshTerminalSource, /\}, 3_000\);/);
  assert.match(sshTerminalSource, /now - lastActivityEmitAt >= 500/);
  assert.match(
    sshTerminalSource,
    /emitActivity\(\{ phase: "completed", summary: event\.reason, updatedAt: endedAt, endedAt \}\);/,
  );
  assert.match(sshTerminalSource, /emitActivity\(\{ phase: "failed", updatedAt: endedAt, endedAt \}\);/);
  // 断开重连仍在建立连接，不应留下静默或完成状态。
  assert.match(
    sshTerminalSource,
    /emitActivity\(\{ phase: "connecting", summary: event\.reason, updatedAt: endedAt \}\);/,
  );
  assert.match(sshTerminalSource, /onBeforeUnmount\(\(\) => \{[\s\S]{0,120}clearQuietActivityTimer\(\);/);
});

test("pane tree lifts activity from both local and ssh panes", () => {
  // TerminalPaneLayout renders every pane (the split tree only lays out their slots).
  const activityHandlers = paneTreeSource.match(/@activity="emit\('activity', pane\.id, \$event\)"/g);
  assert.equal(activityHandlers?.length, 2);
});
