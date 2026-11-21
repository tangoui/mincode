import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const overviewSource = await readFile(
  new URL("../src/components/ssh/SshServerOverview.vue", import.meta.url),
  "utf8"
);
const workspaceSource = await readFile(
  new URL("../src/views/SshWorkspaceView.vue", import.meta.url),
  "utf8"
);
const stylesSource = await readFile(
  new URL("../src/styles/workspaces/ssh/server-overview.css", import.meta.url),
  "utf8"
);

test("SSH overview only samples ready sessions with bounded concurrency", () => {
  assert.match(overviewSource, /const REFRESH_INTERVAL_MS = 10_000/);
  assert.match(overviewSource, /const MAX_REFRESH_CONCURRENCY = 4/);
  assert.match(overviewSource, /if \(session\.status !== "ready"\) return/);
  assert.match(overviewSource, /loadSshServerSnapshot\(session\.sessionId\)/);
  assert.match(overviewSource, /Math\.min\(MAX_REFRESH_CONCURRENCY, sessions\.length\)/);
  assert.match(overviewSource, /if \(refreshing\.value \|\| document\.hidden\) return/);
  assert.match(overviewSource, /function stopRefreshTimer\(\) \{[\s\S]*?clearInterval\(refreshTimer\)/);
  assert.match(overviewSource, /onBeforeUnmount\(stopRefreshTimer\)/);
  // The SSH workspace is kept alive, so polling must also pause while the view is hidden.
  assert.match(overviewSource, /onDeactivated\(stopRefreshTimer\)/);
  assert.match(overviewSource, /onActivated\(\(\) => \{[\s\S]*?startRefreshTimer\(\)/);
});

test("SSH overview maps existing pane IDs to native session IDs", () => {
  assert.match(workspaceSource, /const SSH_OVERVIEW_TAB_ID = "__ssh-server-overview__"/);
  assert.match(workspaceSource, /const overviewSessions = computed<SshOverviewSession\[\]>/);
  assert.match(workspaceSource, /sessionId: pane\.id/);
  assert.match(workspaceSource, /status: pane\.status/);
  assert.match(workspaceSource, /<SshServerOverview/);
  assert.match(workspaceSource, /@connect="openSavedProfile"/);
  assert.match(workspaceSource, /@open-session="activeSessionId = \$event"/);
});

test("SSH overview is built around quick connect with a grouped host table", () => {
  assert.match(overviewSource, /groupedProfiles/);
  assert.match(overviewSource, /<ConnectionRecentStrip/);
  assert.match(overviewSource, /connection-overview__table/);
  // "In use" cards are the only place that shows load metrics.
  assert.match(overviewSource, /class="ssh-server-live"/);
  assert.match(overviewSource, /useOverviewSearchKeyboard\(/);
  assert.match(overviewSource, /parseSshAddress\(/);
  assert.match(overviewSource, /pickRecent\(/);
  assert.doesNotMatch(overviewSource, /ssh-server-card__cpu/);
  assert.doesNotMatch(stylesSource, /\.ssh-server-card__cpu/);
  assert.doesNotMatch(stylesSource, /conic-gradient/);
  assert.match(stylesSource, /@media \(max-width: 680px\)/);
});

test("SSH workspace wires the overview's recent history and header actions", () => {
  assert.match(workspaceSource, /:opened-at="sshRecent\.openedAt"/);
  assert.match(workspaceSource, /:recent-cleared-at="sshRecent\.clearedAt"/);
  assert.match(workspaceSource, /@clear-recent="sshRecent\.clearRecent\(\)"/);
  assert.match(workspaceSource, /@new-connection-for-address="newConnectionForAddress"/);
  assert.match(workspaceSource, /function newConnectionForAddress\(address: string\)/);
  // The typed address prefills the new-host dialog and is dropped once the dialog closes.
  const prefill = workspaceSource.slice(workspaceSource.indexOf("function newConnectionForAddress"));
  assert.match(prefill.slice(0, prefill.indexOf("\n}\n")), /editingDraft\.value = undefined;[\s\S]*pendingAddress\.value = address;[\s\S]*dialogVisible\.value = true;/);
  assert.match(workspaceSource, /:initial-address="pendingAddress"/);
  assert.match(workspaceSource, /watch\(dialogVisible, \(visible\) => \{\s*if \(!visible\) pendingAddress\.value = "";/);
  assert.match(workspaceSource, /@import="chooseImport"/);
  assert.match(workspaceSource, /@known-hosts="knownHostsVisible = true"/);
  assert.match(workspaceSource, /@file-transfer="openSavedFileTransfer"/);
  assert.match(workspaceSource, /@new-connection="newConnection"/);
});

test("SSH overview isolates the dashboard layout from the legacy server drawer", () => {
  assert.match(overviewSource, /<section class="ssh-server-dashboard">/);
  assert.match(stylesSource, /\.ssh-server-dashboard \{[\s\S]*?display: flex;/);
  assert.doesNotMatch(overviewSource, /<section class="ssh-server-overview">/);
});

test("SSH overview uses Element Plus scrollbar instead of native scrolling", () => {
  // Other attributes (e.g. the ref used for windowed host rows) may sit on the same element.
  assert.match(overviewSource, /<el-scrollbar\b[^>]*\bclass="ssh-server-overview__scroll"[^>]*>/);
  assert.match(stylesSource, /\.ssh-server-overview__scroll \{[\s\S]*?flex: 1;/);
  assert.doesNotMatch(stylesSource, /\.ssh-server-overview__scroll \{[\s\S]*?overflow: auto;/);
});

test("SSH workspace records recently opened hosts and prunes deleted ones after hydration", () => {
  assert.match(workspaceSource, /import \{ useSshRecentStore \} from "@\/stores\/ssh-recent"/);
  assert.match(workspaceSource, /sshRecent\.hydrate\(\)/);
  // Only the transition into "ready" is recorded, read before the pane is updated.
  assert.match(
    workspaceSource,
    /function updateWorkspaceSessionStatus\([\s\S]*?previousPane\.status !== "ready" && status === "ready"\) \{\s*sshRecent\.markOpened\(previousPane\.profile\.id\)[\s\S]*?updateSshWorkspacePane\(/
  );
  assert.match(workspaceSource, /sshRecent\.markOpened\(/);
  assert.match(workspaceSource, /sshRecent\.prune\(/);
  assert.match(workspaceSource, /connections\.hydrated && sshRecent\.hydrated/);
});
