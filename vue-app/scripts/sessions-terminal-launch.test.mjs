import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";
import { URL } from "node:url";

const viewUrl = new URL(
  "../src/views/SessionsWorkspaceView.vue",
  import.meta.url,
);
const zhUrl = new URL("../src/i18n/locales/zh.ts", import.meta.url);
const enUrl = new URL("../src/i18n/locales/en.ts", import.meta.url);
const tabActivityUrl = new URL("../src/components/sessions/SessionLiveTabActivity.vue", import.meta.url);

const [viewSource, zhSource, enSource, tabActivitySource] = await Promise.all([
  readFile(viewUrl, "utf8"),
  readFile(zhUrl, "utf8"),
  readFile(enUrl, "utf8"),
  readFile(tabActivityUrl, "utf8"),
]);

test("session history exposes an explicit embedded terminal action", () => {
  assert.match(viewSource, /:icon="Monitor"\s+@click="openEmbeddedTerminal"/);
  assert.match(
    viewSource,
    /<span>\{\{ t\("sessions\.openTerminal"\) \}\}<\/span>/,
  );
  assert.doesNotMatch(
    viewSource,
    /@click="openEmbeddedTerminal"[\s\S]{0,240}\{\{ providerLabel\(selected\.provider\) \}\}/,
  );
});

test("embedded terminal starts the selected CLI without navigating away", () => {
  assert.match(
    viewSource,
    /prepareLivePane\(current\.provider, terminalId, cwd \|\| undefined, command\)/,
  );
  assert.match(viewSource, /paneRoot: prepared\.pane/);
  assert.match(viewSource, /activePanel\.value = "terminal";/);
  assert.match(viewSource, /@click="resumeSelectedSession"/);
  assert.match(viewSource, /t\('sessions\.openInNewTerminal'\)/);
});

test("terminal tab bar can launch a blank PTY in the selected working directory", () => {
  assert.match(viewSource, /:icon="Plus"/);
  assert.match(viewSource, /@click="openNewEmbeddedTerminal"/);
  assert.match(
    viewSource,
    /const key = `\$\{sessionTerminalKey\}:\$\{terminalId\}`;/,
  );
  assert.match(
    viewSource,
    /prepareLivePane\(current\.provider, terminalId, cwd \|\| undefined, ""\)/,
  );
  assert.match(viewSource, /initialCommand: "",\s+paneRoot: prepared\.pane/);
  assert.match(viewSource, /@click="activateLiveTerminal\(entry\)"/);
});

test("terminal tabs can be renamed without changing the history session", () => {
  assert.match(viewSource, /trigger="contextmenu"/);
  assert.match(
    viewSource,
    /@dblclick\.stop="renameLiveTerminal\(entry\.key\)"/,
  );
  assert.match(
    viewSource,
    /terminal\.key === key \? \{ \.\.\.terminal, label, manualLabelOverride: true \} : terminal/,
  );
  assert.match(viewSource, /restoreAutomaticLiveTerminalLabel/);
  assert.match(viewSource, /command="restore-auto-label"/);
  assert.match(viewSource, /t\("sessions\.renameTerminalPrompt"\)/);
});

test("terminal tab menu closes tabs to the left, right, others or all with their shells", () => {
  assert.match(viewSource, /command="close-others"[^>]*:disabled="liveTerminals\.length < 2"/);
  assert.match(viewSource, /command="close-all"/);
  assert.match(viewSource, /command="close-left"[^>]*:disabled="entry\.key === liveTerminals\[0\]\?\.key"/);
  assert.match(viewSource, /command="close-right"[^>]*:disabled="entry\.key === liveTerminals\[liveTerminals\.length - 1\]\?\.key"/);
  assert.match(viewSource, /command === "close-left" \? i < index : command === "close-right" \? i > index : i !== index/);
  assert.match(viewSource, /t\("tab\.closeLeft"\)/);
  assert.match(viewSource, /t\("tab\.closeRight"\)/);
  assert.match(viewSource, /t\("tab\.closeOther"\)/);
  assert.match(viewSource, /t\("tab\.closeAll"\)/);
  // Every closed tab releases its panes' shells; the right-clicked tab stays active.
  assert.match(viewSource, /async function closeLiveTerminals\(keys: string\[\], keep\?: string\)/);
  assert.match(viewSource, /if \(!closing\.has\(entry\.key\)\) continue;\s+for \(const paneId of collectLeafIds\(entry\.paneRoot\)\)/);
  assert.match(viewSource, /nextTabs\.find\(\(item\) => item\.key === keep\)/);
});

test("provider groups can start their CLI in the embedded terminal without sessions", () => {
  assert.match(
    viewSource,
    /@click="createProviderTerminal\(provider\.id\)"/,
  );
  assert.match(
    viewSource,
    /const cwd =\s*projectPath \|\|\s*workspaceProjectRoot\.value \|\|\s*\(await chooseNativeDirectory\(/,
  );
  assert.match(
    viewSource,
    /function buildNewSessionCommand\(provider: ProviderId, cwd\?: string\) \{\s*return buildSessionToolNewCommand\(toolPrefs\.value, provider, cwd\);/,
  );
  assert.match(viewSource, /const command = buildNewSessionCommand\(provider, cwd\);/);
  assert.match(
    viewSource,
    /prepareLivePane\(provider, terminalId, cwd, command\)/,
  );
  assert.match(viewSource, /paneRoot: prepared\.pane/);
  assert.match(viewSource, /activePanel\.value = "terminal";/);
  const providerTerminalSource =
    viewSource.match(
      /async function createProviderTerminal[\s\S]*?\n}\n\nasync function openSession/,
    )?.[0] ?? "";
  assert.ok(providerTerminalSource);
  assert.doesNotMatch(providerTerminalSource, /router\.push\(\{ name: "terminal" \}\)/);
});

test("provider terminal action is explicit in both supported locales", () => {
  assert.match(zhSource, /"sessions\.newCliTerminal": "新建 \{provider\} 终端"/);
  assert.match(enSource, /"sessions\.newCliTerminal": "New \{provider\} terminal"/);
  assert.match(zhSource, /"sessions\.chooseTerminalFolder": "选择 \{provider\} 的工作目录"/);
  assert.match(enSource, /"sessions\.chooseTerminalFolder": "Choose a working folder for \{provider\}"/);
});

test("project rows start the matching CLI on the right in that folder", () => {
  assert.match(
    viewSource,
    /@click="createProviderTerminal\(provider\.id, project\.decodedPath\)"/,
  );
  assert.match(
    viewSource,
    /@click="createProviderTerminal\(provider\.id, group\.path\)"/,
  );
  assert.match(
    viewSource,
    /function openProjectTerminal\(project: ClaudeProject\)[\s\S]*createProviderTerminal\("claude", project\.decodedPath\)/,
  );
  const projectTerminalSource =
    viewSource.match(
      /async function createProviderTerminal[\s\S]*?\n}\n\nasync function openSession/,
    )?.[0] ?? "";
  assert.match(projectTerminalSource, /activePanel\.value = "terminal";/);
  assert.doesNotMatch(projectTerminalSource, /router\.push/);
});

test("project CLI terminal action is explicit in both supported locales", () => {
  assert.match(
    zhSource,
    /"sessions\.newProjectCliTerminal": "在此文件夹新建 \{provider\} 终端"/,
  );
  assert.match(
    enSource,
    /"sessions\.newProjectCliTerminal": "New \{provider\} terminal in this folder"/,
  );
});

test("terminal activity is lifted from panes without turning status clicks into tab selection", () => {
  assert.match(viewSource, /@activity="\(paneId, snapshot\) => updateLivePaneActivity\(entry\.key, paneId, snapshot\)"/);
  assert.match(viewSource, /listenAppEvent<ClaudeActivityEvent>\("terminal-activity", applyClaudeActivity\)/);
  assert.match(tabActivitySource, /class="sessions-live-tab__activity"[\s\S]{0,500}@click\.stop/);
  assert.match(viewSource, /<SessionLiveTabActivity\s+:activities="entry\.paneActivities"/);
  const selectButton = viewSource.match(
    /<button\s+type="button"\s+class="sessions-live-tab__select"[\s\S]*?<\/button>/,
  )?.[0] ?? "";
  assert.ok(selectButton);
  assert.doesNotMatch(selectButton, /sessions-live-tab__activity|SessionLiveTabActivity/);
});

test("the live tab shows only running and failure states; the attention bell covers the rest", () => {
  // Finished, idle and waiting-for-input tabs carry no status icon: the bell marks
  // "done, not looked at yet", and no icon means not running.
  assert.match(tabActivitySource, /if \(phase === "failed" \|\| phase === "waiting_approval"\) return WarningFilled;/);
  assert.match(tabActivitySource, /if \(isTerminalActivityActive\(phase\)\) return Loading;\s*return undefined;/);
  assert.match(tabActivitySource, /<el-tooltip v-if="icon"/);
  assert.doesNotMatch(tabActivitySource, /CircleCheck|Minus/);
  assert.match(tabActivitySource, /const spinning = computed\(\(\) => isTerminalActivityActive\(activity\.value\.phase\)\)/);
  assert.match(tabActivitySource, /'is-spinning': spinning/);
});

test("each split pane gets independent Claude activity preparation and cleanup", () => {
  assert.match(
    viewSource,
    /const paneIdValue = nextLiveTerminalId\(\);\s+const prepared = await prepareLivePane\(entry\.provider, paneIdValue, entry\.cwd, entry\.initialCommand\)/,
  );
  assert.match(viewSource, /unregisterLivePane\(paneIdValue\)/);
  assert.match(viewSource, /unregisterLivePane\(paneId\)/);
});

test("terminal action is explicit in both supported locales", () => {
  assert.match(zhSource, /"sessions\.openTerminal": "在此终端继续"/);
  assert.match(
    enSource,
    /"sessions\.openTerminal": "Continue here in terminal"/,
  );
  assert.match(
    zhSource,
    /"sessions\.openTerminalHint":\s*"在当前页面启动 \{provider\} CLI 并继续此会话"/,
  );
  assert.match(
    enSource,
    /"sessions\.openTerminalHint":\s*"Start the \{provider\} CLI on this page and continue this session"/,
  );
  assert.match(zhSource, /"sessions\.newTerminal": "新建空白终端"/);
  assert.match(enSource, /"sessions\.newTerminal": "New blank terminal"/);
  assert.match(zhSource, /"sessions\.renameTerminal": "重命名终端"/);
  assert.match(enSource, /"sessions\.renameTerminal": "Rename terminal"/);
});

test("terminal tabs are persisted per workspace scope", () => {
  assert.match(
    viewSource,
    /useWorkspaceScopedPersistence<PersistedLiveTerminals>\(\s*"app_sessions_live_terminals_v1",/,
  );
  assert.match(viewSource, /interface PersistedLiveTerminal \{/);
  // paneActivities 每秒变化，标签集合必须显式保存，不能用深监听自动写盘。
  assert.doesNotMatch(viewSource, /watch\(\s*liveTerminals\s*,/);
  for (const site of [
    /activePanel\.value = "terminal";\s+persistLiveTerminals\(\);/,
    /manualLabelOverride: true \} : terminal,\s+\);\s+persistLiveTerminals\(\);/,
    /manualLabelOverride: false \}\s+: terminal,\s+\);\s+persistLiveTerminals\(\);/,
  ]) {
    assert.match(viewSource, site);
  }
  assert.match(viewSource, /watch\(activeLiveTerminalKey, async \(\) => \{\s+persistLiveTerminals\(\);/);
});

test("restored terminal tabs open a blank shell in the original directory", () => {
  assert.match(
    viewSource,
    /const prepared = await prepareLivePane\(item\.provider, terminalId, item\.cwd, ""\);/,
  );
  assert.match(viewSource, /initialCommand: "",\s+paneRoot: prepared\.pane,\s+activePaneId: terminalId,/);
  // 恢复的标签必须同时选中会话并切到终端面板，否则标签栏仍然不可见。
  assert.match(
    viewSource,
    /selected\.value = \{ provider: active\.provider, session: active\.session \};\s+activePanel\.value = "terminal";/,
  );
  // 每条恢复记录都要拿新的 PTY ID，避免附着到上次进程遗留的 shell。
  assert.match(viewSource, /for \(const item of stored\) \{\s+[\s\S]{0,260}const terminalId = nextLiveTerminalId\(\);/);
  assert.match(viewSource, /\.then\(hydrateSessionListScope\)\s*\.then\(hydrateLiveTerminals\)/);
});

test("a stuck Claude state stops spinning once it goes stale", () => {
  // 每秒推进的时钟必须喂给聚合，过期的“正在执行”才会自己停下来。
  assert.match(
    tabActivitySource,
    /aggregateTerminalActivity\(props\.activities, props\.createdAt, now\.value\)/,
  );
  // 通用信号让位给 Claude 精确状态时，输出时间戳仍要落库，否则长任务途中会误判成静默。
  assert.match(
    viewSource,
    /if \(!shouldApplyTerminalActivity\(current, incoming\)\) \{[\s\S]*?\[paneId\] = \{ \.\.\.current, lastOutputAt: incoming\.lastOutputAt \};/,
  );
});

test("duplicate session terminals never reach the tab bar", () => {
  // 会话终端的 key 只由 provider + 会话 + 目录决定，连点两次会让两个调用都跑过
  // await 之前的查重；准备完 PTY 后必须再确认一次，重复的那个要回收掉。
  assert.match(
    viewSource,
    /const key = liveTerminalKey\(current\.provider, current\.session\);\s+if \(!liveTerminals\.value\.some\(\(entry\) => entry\.key === key\)\) \{[\s\S]*?const prepared = await prepareLivePane\([\s\S]*?if \(liveTerminals\.value\.some\(\(entry\) => entry\.key === key\)\) \{\s+void unregisterLivePane\(terminalId\);\s+\} else \{/,
  );
  // 早期版本已经把重复 key 写进了持久化数据，恢复时也要去重，否则标签栏 v-for 撞 key。
  assert.match(
    viewSource,
    /const restoredKeys = new Set<string>\(\);\s+for \(const item of stored\) \{[\s\S]*?if \(restoredKeys\.has\(item\.key\)\) continue;\s+restoredKeys\.add\(item\.key\);/,
  );
});

test("deep links take precedence over restored terminal tabs", () => {
  assert.match(
    viewSource,
    /if \(parseSessionDeepLink\(route\.query\)\.kind !== "none"\) \{\s+liveTerminalsScope = captured;\s+return;/,
  );
});

test("the activity clock lives in each tab indicator and only ticks while a pane is active", () => {
  // 整个会话工作区不再持有每秒时钟，否则每一秒都会重渲染侧边栏和消息列表。
  assert.doesNotMatch(viewSource, /activityClock/);
  assert.match(tabActivitySource, /watch\(\[spinning, paused\][\s\S]*?if \(!isActive \|\| isPaused\) return;/);
  assert.match(tabActivitySource, /onDeactivated\(/);
});

test("pane activity updates mutate one entry instead of replacing the tab array", () => {
  const update = viewSource.match(/function updateLivePaneActivity\([\s\S]*?\n\}/)?.[0] ?? "";
  const claude = viewSource.match(/function applyClaudeActivity\([\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(update && claude);
  assert.doesNotMatch(update, /liveTerminals\.value = /);
  assert.doesNotMatch(claude, /liveTerminals\.value = /);
  assert.match(update, /entry\.paneActivities\[paneId\] = incoming;/);
});
