import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";
import {
  attentionFromActivity,
  classifyNotification,
  isTerminalReport,
  clearAttention,
  parseTerminalAttentionEvent,
  raiseAttention,
  retainAttention,
  strongestAttention,
  withCodexNotifications,
} from "../src/domain/terminal/terminal-attention.ts";

const attention = (paneId, reason, at = 1) => ({ paneId, reason, at });

test("backend events are validated and notifications classified", () => {
  assert.deepEqual(parseTerminalAttentionEvent({ paneId: "p-1", kind: "bell", at: 5 }), { paneId: "p-1", reason: "bell", at: 5 });
  assert.deepEqual(parseTerminalAttentionEvent({ paneId: "p-1", kind: "notify", message: "Approval requested: rm -rf build", at: 5 }), {
    paneId: "p-1",
    reason: "waiting_approval",
    message: "Approval requested: rm -rf build",
    at: 5,
  });
  assert.equal(parseTerminalAttentionEvent({ paneId: "p-1", kind: "notify", message: "Agent turn complete", at: 5 }).reason, "completed");
  assert.equal(parseTerminalAttentionEvent({ paneId: "p-1", kind: "notify", message: "Deploy ready", at: 5 }).reason, "notify");
  assert.equal(parseTerminalAttentionEvent({ paneId: "p-1", kind: "notify", at: 5 }).reason, "notify");
  // Secrets in a notification text are redacted before they reach a tooltip or system notification.
  assert.match(parseTerminalAttentionEvent({ paneId: "p", kind: "notify", message: "password=hunter2 done" }).message, /\[REDACTED\]/);
  for (const bad of [null, "x", { paneId: "../x", kind: "bell" }, { paneId: "p", kind: "exec" }, { paneId: 3, kind: "bell" }, { paneId: "p".repeat(200), kind: "bell" }]) {
    assert.equal(parseTerminalAttentionEvent(bad), undefined, JSON.stringify(bad));
  }
  assert.equal(typeof parseTerminalAttentionEvent({ paneId: "p", kind: "quiet" }).at, "number");
});

test("notification wording maps to the most useful reason", () => {
  assert.equal(classifyNotification("Codex wants permission to run tests"), "waiting_approval");
  assert.equal(classifyNotification("需要你批准命令"), "waiting_approval");
  assert.equal(classifyNotification("Plan mode: please answer the question"), "waiting_input");
  assert.equal(classifyNotification("Agent turn complete: fixed the failing test"), "completed");
  assert.equal(classifyNotification("Build failed"), "failed");
  assert.equal(classifyNotification("Hello"), "notify");
});

test("only finished or waiting tool states become attentions", () => {
  assert.deepEqual(attentionFromActivity({ paneId: "cc-live-1", phase: "waiting_approval", summary: "Bash: npm test", updatedAt: 9 }), {
    paneId: "cc-live-1",
    reason: "waiting_approval",
    message: "Bash: npm test",
    at: 9,
  });
  assert.equal(attentionFromActivity({ paneId: "cc-live-1", phase: "completed", updatedAt: 9 }).reason, "completed");
  assert.equal(attentionFromActivity({ paneId: "cc-live-1", phase: "active", updatedAt: 9 }), undefined);
  assert.equal(attentionFromActivity({ paneId: "cc-live-1", phase: "quiet", updatedAt: 9 }), undefined);
  assert.equal(attentionFromActivity({ phase: "failed" }), undefined);
});

test("raising respects what the user is looking at and reason priority", () => {
  const empty = new Map();
  assert.equal(raiseAttention(empty, attention("a", "bell"), true).items, empty, "a seen pane is never marked");

  const first = raiseAttention(empty, attention("a", "quiet"), false);
  assert.equal(first.raised, true);
  assert.equal(first.items.get("a").reason, "quiet");

  const upgraded = raiseAttention(first.items, attention("a", "waiting_approval", 2), false);
  assert.equal(upgraded.raised, true, "a more important reason notifies again");
  assert.equal(upgraded.items.get("a").reason, "waiting_approval");

  const lower = raiseAttention(upgraded.items, attention("a", "quiet", 3), false);
  assert.equal(lower.items, upgraded.items, "a weaker signal does not hide the stronger one");

  const same = raiseAttention(upgraded.items, attention("a", "waiting_approval", 4), false);
  assert.equal(same.raised, false, "the same reason updates silently");
  assert.equal(same.items.get("a").at, 4);
});

test("clearing, pruning and picking the strongest attention", () => {
  const items = new Map([
    ["a", attention("a", "completed", 1)],
    ["b", attention("b", "failed", 1)],
    ["c", attention("c", "bell", 3)],
  ]);
  assert.equal(clearAttention(items, ["x"]), items, "unchanged maps keep their identity");
  assert.deepEqual([...clearAttention(items, ["a", "x", "b"]).keys()], ["c"]);
  assert.equal(retainAttention(items, () => true), items);
  assert.deepEqual([...retainAttention(items, (id) => id !== "b").keys()], ["a", "c"]);
  assert.equal(strongestAttention(items, ["a", "c"]).paneId, "c");
  assert.equal(strongestAttention(items, ["a", "b", "c"]).reason, "failed");
  assert.equal(strongestAttention(items, ["z"]), undefined);
});

test("focus, mouse and query replies sent by xterm are not keystrokes", () => {
  for (const report of ["\x1b[I", "\x1b[O", "\x1b[I\x1b[O", "\x1b[<0;12;5M", "\x1b[<35;80;20m", "\x1b[M !!", "\x1b[12;40R", "\x1b[?62;22c", "\x1b]11;rgb:0000/0000/0000\x1b\\", "\x1b]10;rgb:ffff/ffff/ffff\x07", "\x1b[?2026;2$y"]) {
    assert.equal(isTerminalReport(report), true, JSON.stringify(report));
  }
  for (const input of ["a", "\r", "\x1b[A", "\x1b", "\x03", "\x1b[200~paste\x1b[201~", "你好", ""]) {
    assert.equal(isTerminalReport(input), false, JSON.stringify(input));
  }
});

test("a Claude session starting is not an attention", () => {
  assert.equal(attentionFromActivity({ paneId: "cc-live-1", phase: "waiting_input", event: "SessionStart", updatedAt: 1 }), undefined);
  assert.equal(attentionFromActivity({ paneId: "cc-live-1", phase: "waiting_input", event: "Stop", updatedAt: 1 }).reason, "waiting_input");
});

test("codex is asked to report turn ends and approvals through OSC 9", () => {
  const flags = "-c tui.notifications=true -c tui.notification_method=osc9 -c tui.notification_condition=always";
  assert.equal(withCodexNotifications("codex"), `codex ${flags}`);
  assert.equal(withCodexNotifications("codex resume 019a --yolo"), `codex ${flags} resume 019a --yolo`);
  assert.equal(withCodexNotifications("codex -c tui.notification_method=bel"), "codex -c tui.notification_method=bel");
  for (const command of ["claude", "codexx", "npx codex", "echo codex", ""]) assert.equal(withCodexNotifications(command), command);
});

test("attention is wired into the tab bar, sessions, sidebar, badge and settings", async () => {
  const [app, tabBar, sessions, sidebar, settings, panel, zh, en, localTerminal, sshTerminal] = await Promise.all([
    "../src/App.vue",
    "../src/components/layout/AppTabBar.vue",
    "../src/views/SessionsWorkspaceView.vue",
    "../src/components/layout/AppSidebar.vue",
    "../src/services/settings-model.ts",
    "../src/components/settings/TerminalSettingsPanel.vue",
    "../src/i18n/locales/zh.ts",
    "../src/i18n/locales/en.ts",
    "../src/components/terminal/LocalTerminal.vue",
    "../src/components/terminal/SshTerminal.vue",
  ].map((path) => readFile(new URL(path, import.meta.url), "utf8")));
  // Typing into a pane acknowledges its marker.
  for (const terminal of [localTerminal, sshTerminal]) {
    assert.match(terminal, /<div class="terminal-pane-toolbar__actions">\s*<TerminalPaneAttention :pane-id="sessionId" \/>/);
    assert.match(terminal, /xterm\.onData\(\(input\) => \{[\s\S]{0,260}!isTerminalReport\(input\) && terminalAttention\.items\.has\(props\.sessionId\)\) terminalAttention\.dismiss\(\[props\.sessionId\]\)/);
  }
  assert.match(app, /notifications\.unreadCount \+ terminalAttentionStore\.count/);
  assert.match(app, /target\?\.kind === "terminal-pane"[\s\S]{0,80}focusPane/);
  assert.equal((tabBar.match(/<TerminalAttentionMarker/g) ?? []).length, 3);
  assert.match(tabBar, /withCodexNotifications\(aiCommand\)/);
  assert.match(sessions, /setSessionPanes\(panes\)/);
  assert.match(sessions, /setVisiblePanes\("sessions", paneIds\)/);
  assert.match(sessions, /withCodexNotifications\(requestedCommand\)/);
  assert.match(sessions, /<TerminalAttentionMarker v-if="liveTabAttention\(entry\)"/);
  // Session list rows (Claude projects and other tools) carry the bell of their live terminal.
  assert.match(sessions, /v-if="sessionAttention\('claude', session\)"/);
  assert.match(sessions, /v-if="sessionAttention\(provider\.id, session\)"/);
  // The recorded-status dot sits in front of the title, not over the text below it.
  assert.equal((sessions.match(/<span class="sessions-session-row__title">\s*<span\s+v-if="session\.status"\s+class="sessions-status-dot"/g) ?? []).length, 2);
  assert.match(sidebar, /attentionViews\.has\(item\.id\)/);
  assert.match(tabBar, /dismissTabAttention\(tab\);\s*selectTab/);
  assert.match(sessions, /async function activateLiveTerminal[^{]*\{\s*terminalAttention\.dismiss/);
  assert.match(settings, /terminalAttention: true,\n\s*terminalAttentionSystemNotify: true,/);
  assert.match(panel, /settings\.terminalAttentionSystemNotify/);
  for (const reason of ["failed", "waiting_approval", "waiting_input", "bell", "notify", "completed", "exited", "quiet"]) {
    assert.ok(zh.includes(`"terminal.attention.short.${reason}":`) && en.includes(`"terminal.attention.short.${reason}":`), reason);
  }
  const reasons = ["failed", "waiting_approval", "waiting_input", "bell", "notify", "completed", "exited", "quiet"];
  for (const key of [...reasons.map((reason) => `terminal.attention.reason.${reason}`), "terminal.attention.untitled", "settings.terminalAttention", "settings.terminalAttentionHint", "settings.terminalAttentionSystemNotify"]) {
    assert.ok(zh.includes(`"${key}":`), `zh ${key}`);
    assert.ok(en.includes(`"${key}":`), `en ${key}`);
  }
});
