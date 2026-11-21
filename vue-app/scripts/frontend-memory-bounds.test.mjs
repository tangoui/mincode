import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  deleteTerminalSnapshot,
  readTerminalSnapshot,
  saveTerminalSnapshot,
} from "../src/services/terminal-session-cache.ts";

const [
  projectSource,
  lspWrapperSource,
  lspRuntimeSource,
  snapshotSource,
  sessionsSource,
  localTerminalSource,
  sshTerminalSource,
  assistantSource,
  workspaceScopeSource,
  terminalWorkspaceSource,
] = await Promise.all([
  readFile(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/composables/use-project-language-servers.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/composables/project-language-server-runtime.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/services/terminal-session-cache.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/views/SessionsWorkspaceView.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/terminal/LocalTerminal.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/terminal/SshTerminal.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/stores/ai-assistant.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/domain/workspace/state-scope.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/stores/terminal-workspace.ts", import.meta.url), "utf8"),
]);
const lspSource = lspRuntimeSource;
// The Language servers dialog moved out of ProjectWorkspaceView (Phase G).
const lspDialogSource = await readFile(new URL("../src/components/projects/ProjectLanguageServersDialog.vue", import.meta.url), "utf8");

test("项目打开时不再全量启用已安装语言服务器", () => {
  assert.doesNotMatch(projectSource, /Auto-enable all installed servers/);
  assert.doesNotMatch(projectSource, /installedIds[\s\S]*?enabledServers/);
  assert.doesNotMatch(lspDialogSource, /installedIds[\s\S]*?enabledServers/);
  assert.match(lspDialogSource, /function toggleLspServer/);
});

test("已启用语言服务器也只为当前打开文件的语言启动", () => {
  assert.match(lspSource, /const openLanguages = new Set/);
  assert.match(lspSource, /languages\??\.some\(\(language\) => openLanguages\.has\(language\)\)/);
  assert.doesNotMatch(lspSource, /!neededNow && info\.heavy/);
});

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


test("项目工作区后台停留超过宽限期后停止语言服务器", () => {
  assert.match(lspSource, /limits\.lspBackgroundStopSeconds \* 1000/);
  assert.match(lspWrapperSource, /watch\(settingsSnapshotVersion, \(\) => controller\?\.settingsChanged\(\)\)/);
  assert.match(lspWrapperSource, /onDeactivated\(\(\) => \{[\s\S]*?controller\?\.deactivate\(\)/);
  assert.match(lspWrapperSource, /onActivated\(\(\) => \{[\s\S]*?controller\?\.activate\(\)/);
  assert.match(lspSource, /function settingsChanged\(\)[\s\S]*?if \(!active\) scheduleBackgroundStop\(\)/);
  assert.match(lspSource, /function deactivate\(\)[\s\S]*?scheduleBackgroundStop\(\)/);
  assert.match(lspSource, /setTimeout\(\(\) => \{[\s\S]*?stopSession/);
  assert.match(lspSource, /function activate\(\)[\s\S]*?activateSessions/);
  assert.match(lspSource, /if \(!session \|\| session\.generation !== generation\) return/);
  assert.match(lspSource, /currentWorkspaceActive/);
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


test("没有匹配文档的语言服务器按类型延迟回收", () => {
  assert.match(lspSource, /runtimePerformanceSettingsFromSnapshot/);
  assert.match(lspSource, /limits\.lspIdleStopSeconds \* 1000/);
  assert.match(lspSource, /limits\.lspAuxiliaryIdleStopSeconds \* 1000/);
  assert.match(lspSource, /function scheduleIdleSessionStop/);
  assert.match(lspSource, /openDocuments\.get\(key\)\?\.size/);
  assert.match(lspSource, /function attachModel[\s\S]*?clearIdleStopTimer/);
  assert.match(lspSource, /function detachModel[\s\S]*?scheduleIdleSessionStop/);
});

test("终端布局恢复缓存使用小型 LRU 上限", () => {
  assert.match(terminalWorkspaceSource, /terminalLayoutCacheLimit/);
  assert.match(terminalWorkspaceSource, /runtimePerformanceSettingsFromSnapshot/);
  assert.match(terminalWorkspaceSource, /function pruneStoredLayouts/);
  assert.match(terminalWorkspaceSource, /function rememberLayout/);
  assert.match(terminalWorkspaceSource, /Map 插入顺序作为 LRU/);
});
