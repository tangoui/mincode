import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import ts from "typescript";

const source = await readFile(
  new URL("../src/domain/ssh/workspace-session-layout.ts", import.meta.url),
  "utf8"
);
const workspaceViewSource = await readFile(
  new URL("../src/views/SshWorkspaceView.vue", import.meta.url),
  "utf8"
);
const sshTerminalSource = await readFile(
  new URL("../src/components/terminal/SshTerminal.vue", import.meta.url),
  "utf8"
);
const sshTerminalPaneTreeSource = await readFile(
  new URL("../src/components/ssh/SshTerminalPaneTree.vue", import.meta.url),
  "utf8"
);
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText;
const layout = await import(
  `data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`
);

function pane(id) {
  return {
    id,
    type: "terminal",
    profile: { id: `profile-${id}` },
    reloadVersion: 0,
    status: "connecting",
  };
}

test("restores saved credentials into every SSH workspace pane", () => {
  const profiles = new Map([
    ["profile-one", { id: "profile-one", authType: "password" }],
    ["profile-two", { id: "profile-two", authType: "password" }],
  ]);
  const credentials = new Map([
    ["profile-one", { password: "secret-one" }],
    ["profile-two", { password: "secret-two" }],
  ]);
  const restored = layout.deserializeSshWorkspaceSessionTab(
    {
      id: "session",
      workspaceScopeId: "scope",
      activePaneId: "two",
      root: {
        id: "split",
        type: "split",
        direction: "horizontal",
        sizes: [0.5, 0.5],
        children: [
          { id: "one", type: "terminal", profileId: "profile-one" },
          { id: "two", type: "terminal", profileId: "profile-two" },
        ],
      },
    },
    (id) => profiles.get(id),
    (id) => credentials.get(id)
  );

  assert.equal(layout.findSshWorkspacePane(restored.root, "one").credentials, credentials.get("profile-one"));
  assert.equal(layout.findSshWorkspacePane(restored.root, "two").credentials, credentials.get("profile-two"));
});

test("SSH workspace hydrates credentials before restoring saved sessions", () => {
  const mounted = workspaceViewSource.slice(
    workspaceViewSource.indexOf("onMounted(async () =>"),
    workspaceViewSource.indexOf("onBeforeUnmount", workspaceViewSource.indexOf("onMounted(async () =>"))
  );
  assert.ok(mounted.indexOf("connections.hydrate()") < mounted.indexOf("deserializeSshWorkspaceSessionTab("));
  assert.match(mounted, /\(id\) => connections\.credentialsFor\(id\)/);
});

test("closing an asynchronously loaded SFTP panel cancels pending path reveal", () => {
  const closeStart = sshTerminalSource.indexOf("function closeSftpPanel() {");
  const closeEnd = sshTerminalSource.indexOf("\n}", closeStart);
  const closeBody = sshTerminalSource.slice(closeStart, closeEnd);
  const watcherStart = sshTerminalSource.indexOf("watch(sftpPanel, (panel) => {");
  const watcherEnd = sshTerminalSource.indexOf("\n});", watcherStart);
  const watcherBody = sshTerminalSource.slice(watcherStart, watcherEnd);

  assert.match(closeBody, /pendingRevealPath\.value = null/);
  assert.match(
    sshTerminalSource,
    /watch\(sftpVisible, \(visible\) => \{\s*if \(!visible\) pendingRevealPath\.value = null;/
  );
  assert.match(watcherBody, /if \(!panel \|\| !sftpVisible\.value\) return/);
});

test("SSH workspace eviction preserves native sessions for later reattachment", () => {
  const unmounted = workspaceViewSource.slice(workspaceViewSource.indexOf("onBeforeUnmount(() =>"));
  assert.doesNotMatch(unmounted, /for \(const session of sshSessionTabs\.value\)[\s\S]*closeWorkspaceSession\(session\.id\)/);
  assert.match(sshTerminalPaneTreeSource, /keep-alive-on-unmount/);
  assert.match(sshTerminalSource, /attachIfExists: true/);
});

test("splits, resizes, updates, and removes SSH terminal panes", () => {
  const first = pane("one");
  const second = pane("two");
  const root = layout.splitSshWorkspacePane(first, "one", "horizontal", second, "split-one");

  assert.equal(root.type, "split");
  assert.deepEqual(layout.sshWorkspacePaneIds(root), ["one", "two"]);
  assert.equal(layout.countSshWorkspacePanes(root), 2);
  assert.equal(layout.findSshWorkspacePane(root, "two")?.profile.id, "profile-two");

  const resized = layout.resizeSshWorkspaceSplit(root, "split-one", [3, 1]);
  assert.deepEqual(resized.sizes, [0.75, 0.25]);

  const updated = layout.updateSshWorkspacePane(resized, "two", (current) => ({
    ...current,
    status: "ready",
  }));
  assert.equal(layout.findSshWorkspacePane(updated, "two")?.status, "ready");

  const collapsed = layout.removeSshWorkspacePane(updated, "one");
  assert.equal(collapsed?.type, "terminal");
  assert.equal(collapsed?.id, "two");
});

test("does not alter a tree when target panes or splits do not exist", () => {
  const root = pane("one");
  assert.equal(layout.splitSshWorkspacePane(root, "missing", "vertical", pane("two"), "split"), undefined);
  assert.equal(layout.removeSshWorkspacePane(root, "missing"), root);
  assert.equal(layout.resizeSshWorkspaceSplit(root, "missing", [1]), root);
});

test("removes a nested SSH pane without discarding the changed child tree", () => {
  const first = pane("one");
  const second = pane("two");
  const third = pane("three");
  const fourth = pane("four");
  const horizontal = layout.splitSshWorkspacePane(first, "one", "horizontal", second, "split-horizontal");
  const leftSplit = layout.splitSshWorkspacePane(horizontal, "one", "vertical", third, "split-left");
  const grid = layout.splitSshWorkspacePane(leftSplit, "two", "vertical", fourth, "split-right");

  const closed = layout.removeSshWorkspacePane(grid, "two");

  assert.deepEqual(layout.sshWorkspacePaneIds(closed), ["one", "three", "four"]);
  assert.equal(layout.findSshWorkspacePane(closed, "two"), undefined);
  assert.equal(closed.type, "split");
  assert.equal(closed.children[1].type, "terminal");
  assert.equal(closed.children[1].id, "four");
});

test("closing a split SSH pane removes it from the layout before waiting for native cleanup", () => {
  const start = workspaceViewSource.indexOf("async function closeWorkspacePane");
  const end = workspaceViewSource.indexOf("async function reloadWorkspacePane", start);
  const closePane = workspaceViewSource.slice(start, end);

  assert.ok(closePane.indexOf("const root = removeSshWorkspacePane") < closePane.indexOf("void closeNativeSession(paneId)"));
});

test("SSH terminal hides connection diagnostics and keeps the final backend error", () => {
  assert.doesNotMatch(sshTerminalSource, /SshConnectionDiagnostics/);
  assert.doesNotMatch(sshTerminalSource, /listenSshConnectionProgress/);
  assert.doesNotMatch(sshTerminalSource, /v-if="status !== 'ready'"/);
  assert.match(sshTerminalSource, /v-if="status === 'error' \|\| status === 'credentials' \|\| status === 'unavailable'"/);
  assert.match(sshTerminalSource, /<code v-if="statusDetail">\{\{ statusDetail \}\}<\/code>/);
  assert.match(sshTerminalSource, /error instanceof Error && error\.message/);
  assert.doesNotMatch(
    sshTerminalSource,
    /ElMessage\.error\(\{\s*message: `\$\{t\("ssh\.startFailed"\)/
  );
});

test("splitting an SSH workspace keeps existing native sessions alive during tree remounts", () => {
  assert.match(sshTerminalPaneTreeSource, /\skeep-alive-on-unmount(?:\s|\/>)/);
  assert.doesNotMatch(sshTerminalPaneTreeSource, /:keep-alive-on-unmount="false"/);
  assert.match(sshTerminalSource, /attachIfExists: true/);

  const aliveCheck = sshTerminalSource.indexOf('invokeBackend<boolean>("shell_session_alive"');
  const connectingMessage = sshTerminalSource.indexOf('xterm.writeln(`\\x1b[36m[MinCode]');
  assert.ok(aliveCheck >= 0);
  assert.ok(connectingMessage > aliveCheck);
});
