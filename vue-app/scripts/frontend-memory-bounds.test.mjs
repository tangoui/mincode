import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  deleteTerminalSnapshot,
  readTerminalSnapshot,
  saveTerminalSnapshot,
} from "../src/services/terminal-session-cache.ts";

const [
  snapshotSource,
  sessionsSource,
  localTerminalSource,
  sshTerminalSource,
  assistantSource,
  workspaceScopeSource,
  terminalWorkspaceSource,
] = await Promise.all([
  readFile(new URL("../src/services/terminal-session-cache.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/views/SessionsWorkspaceView.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/terminal/LocalTerminal.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/terminal/SshTerminal.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/stores/ai-assistant.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/domain/workspace/state-scope.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/stores/terminal-workspace.ts", import.meta.url), "utf8"),
]);
const lspSource = lspRuntimeSource;

test("终端快照同时受过期时间、条目数和总字节限制", () => {
  assert.match(snapshotSource, /MAX_SNAPSHOT_AGE_MS/);
  assert.match(snapshotSource, /MAX_SNAPSHOT_ENTRIES = 96/);
  assert.match(snapshotSource, /MAX_SNAPSHOT_BYTES = 4 \* 1024 \* 1024/);
  assert.match(snapshotSource, /function pruneTerminalSnapshots/);
  assert.match(snapshotSource, /snapshots\.size > MAX_SNAPSHOT_ENTRIES \|\| totalBytes > MAX_SNAPSHOT_BYTES/);
});

test("会话工作区隐藏后暂停状态轮询和活动时钟", () => {
  assert.match(sessionsSource, /onActivated\(startBackgroundRefresh\)/);
  assert.match(sessionsSource, /onDeactivated\(stopBackgroundRefresh\)/);
  assert.match(sessionsSource, /function stopBackgroundRefresh\(\)/);
  assert.match(sessionsSource, /onBeforeUnmount\(\(\) => \{[\s\S]*?stopBackgroundRefresh\(\)/);
});


test("终端快照运行时按 LRU 淘汰并拒绝单个超限快照", () => {
  const ids = Array.from({ length: 97 }, (_, index) => `memory-bound-${index}`);
  for (const id of ids) saveTerminalSnapshot(id, id);
  assert.equal(readTerminalSnapshot(ids[0]), "");
  assert.equal(readTerminalSnapshot(ids.at(-1)), ids.at(-1));

  const oversizedId = "memory-bound-oversized";
  saveTerminalSnapshot(oversizedId, "x".repeat(2 * 1024 * 1024 + 1));
  assert.equal(readTerminalSnapshot(oversizedId), "");

  for (const id of ids) deleteTerminalSnapshot(id);
  deleteTerminalSnapshot(oversizedId);
});


test("隐藏终端释放 WebGL，非活动分屏延迟释放", () => {
  for (const source of [localTerminalSource, sshTerminalSource]) {
    assert.match(source, /TERMINAL_INACTIVE_WEBGL_DELAY_MS = 30_000/);
    assert.match(source, /onDeactivated\(\(\) => \{\s*deactivated = true;\s*syncTerminalRenderActivity\(false, true\);\s*\}\)/);
    assert.match(source, /webglAddonHandle\?\.dispose\(\)/);
    // Only visible panes get WebGL at mount; an inactive one releases it after the delay.
    assert.match(source, /if \(props\.active \|\| props\.visible\) ensureFitRenderer\(\)/);
    assert.match(source, /inactiveWebglTimer = setTimeout\(releaseTerminalWebgl, TERMINAL_INACTIVE_WEBGL_DELAY_MS\)/);
  }
});

test("KeepAlive 缓存支持扩大上限并用 0 表示无限制", () => {
  assert.match(workspaceScopeSource, /DEFAULT_WORKSPACE_CACHE_LIMIT = 6/);
  assert.match(workspaceScopeSource, /MIN_WORKSPACE_CACHE_LIMIT = 0/);
  assert.match(workspaceScopeSource, /MAX_WORKSPACE_CACHE_LIMIT = 20/);
  assert.match(workspaceScopeSource, /parsed < 0/);
});

test("AI 非活动工作区缓存同时受数量和文本体积约束", () => {
  assert.match(assistantSource, /function inactiveWorkspaceCacheLimits/);
  assert.match(assistantSource, /limits\.aiInactiveWorkspaceCacheCount/);
  assert.match(assistantSource, /limits\.aiInactiveWorkspaceCacheMiB \* 1024 \* 1024/);
  assert.match(assistantSource, /function estimatedTextBytes/);
  assert.match(assistantSource, /totalBytes > limits\.textBytes/);
  assert.match(assistantSource, /!entry\.running/);
});


test("终端布局恢复缓存使用小型 LRU 上限", () => {
  assert.match(terminalWorkspaceSource, /terminalLayoutCacheLimit/);
  assert.match(terminalWorkspaceSource, /runtimePerformanceSettingsFromSnapshot/);
  assert.match(terminalWorkspaceSource, /function pruneStoredLayouts/);
  assert.match(terminalWorkspaceSource, /function rememberLayout/);
  assert.match(terminalWorkspaceSource, /Map 插入顺序作为 LRU/);
});
