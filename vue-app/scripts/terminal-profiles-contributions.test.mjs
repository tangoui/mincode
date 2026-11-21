import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";
import { URL } from "node:url";
import {
  TERMINAL_PROFILE_ARG_MAX_RUNES,
  TERMINAL_PROFILE_ARGS_MAX,
  TERMINAL_PROFILE_COMMAND_MAX_LENGTH,
  TERMINAL_PROFILE_ENV_MAX,
  TERMINAL_PROFILE_LOCALES_MAX,
  TERMINAL_PROFILE_MAX_ENTRIES,
  TERMINAL_PROFILE_TITLE_MAX_RUNES,
  formatTerminalArgv,
  formatTerminalEnv,
  parseTerminalProfileEntry,
  parseTerminalProfileKey,
  parseTerminalProfileLaunch,
  quoteTerminalArg,
  replaceProfilePaneWithDefaultShell,
  resolveTerminalProfiles,
  restoreTerminalProfileRef,
  terminalProfileErrorCode,
  terminalProfileKey,
  terminalProfilePlatform,
  terminalProfilePreviewSummary,
  terminalProfileTitle,
} from "../src/domain/terminal/profiles.ts";

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");
const FINGERPRINT = "a".repeat(64);

function contribution(pluginId, entries, overrides = {}) {
  return {
    pluginId,
    pluginName: `${pluginId} name`,
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { entries, issues: [] },
    ...overrides,
  };
}

test("domain constants mirror the contribution contract", async () => {
  const contract = JSON.parse(await readSource("../../plugins/api/contribution-points.json"));
  const point = contract.points.terminalProfiles;
  assert.equal(point.status, "enabled");
  assert.deepEqual(point.kinds, ["process"]);
  assert.deepEqual(point.requiresCapabilities, ["shell"]);
  assert.equal(point.maxEntries, TERMINAL_PROFILE_MAX_ENTRIES);
  assert.equal(point.limits.titleMaxRunes, TERMINAL_PROFILE_TITLE_MAX_RUNES);
  assert.equal(point.limits.localesMax, TERMINAL_PROFILE_LOCALES_MAX);
  assert.equal(point.limits.argsMax, TERMINAL_PROFILE_ARGS_MAX);
  assert.equal(point.limits.argMaxRunes, TERMINAL_PROFILE_ARG_MAX_RUNES);
  assert.equal(point.limits.envMax, TERMINAL_PROFILE_ENV_MAX);
  assert.equal(point.limits.commandMaxLength, TERMINAL_PROFILE_COMMAND_MAX_LENGTH);
  const vueCopy = JSON.parse(await readSource("../src/domain/plugins/contribution-points.json"));
  assert.deepEqual(vueCopy.points.terminalProfiles, point);
});

test("entries are re-validated with contract defaults", () => {
  const plugin = { pluginId: "acme.tools", pluginName: "Acme\u202e Tools" };
  const profile = parseTerminalProfileEntry(
    {
      id: "repl",
      title: " REPL\u0007 ",
      titleLocalized: { zh: "交互", "bad locale!": "x" },
      icon: "terminal",
      command: "${pluginRoot}/bin/repl",
      args: ["--dir", "${workspaceFolder}"],
      env: { MODE: "dev", A_B: "x" },
      specFingerprint: FINGERPRINT,
    },
    plugin,
  );
  assert.equal(profile.key, "acme.tools.repl");
  assert.equal(profile.title, "REPL");
  assert.equal(profile.pluginName, "Acme Tools");
  assert.deepEqual(profile.titleLocalized, { zh: "交互" });
  assert.equal(profile.cwd, "${workspaceFolder}");
  assert.deepEqual(profile.platforms, ["darwin", "linux", "windows"]);
  assert.deepEqual(profile.envNames, ["A_B", "MODE"]);
  assert.equal(terminalProfileTitle(profile, ["zh"]), "交互");
  assert.equal(terminalProfileTitle(profile, ["en"]), "REPL");

  for (const invalid of [
    { id: "Bad", title: "x", command: "python3" },
    { id: "a", title: "x" },
    { id: "a", title: "x", command: "python3", args: ["a\u001bb"] },
    { id: "a", title: "x", command: "python3", args: "ls" },
    { id: "a", title: "x", command: "python3", args: Array.from({ length: 33 }, () => "a") },
    { id: "a", title: "x", command: "python3", env: { "A-B": "x" } },
    { id: "a", title: "x", command: "python3", platforms: [] },
    { id: "a", title: "x", command: "python3", platforms: ["freebsd"] },
    { id: "a", title: "x", command: "x".repeat(TERMINAL_PROFILE_COMMAND_MAX_LENGTH + 1) },
  ]) {
    assert.equal(parseTerminalProfileEntry(invalid, plugin), undefined, JSON.stringify(invalid));
  }
});

test("profiles resolve for process plugins active in the workspace on this platform", () => {
  const entries = [
    { id: "zed", title: "Zed", command: "zsh" },
    { id: "alpha", title: "Alpha", command: "python3", platforms: ["linux", "darwin"] },
    { id: "win", title: "Win", command: "pwsh.exe", platforms: ["windows"] },
    { id: "alpha", title: "Duplicate", command: "python3" },
  ];
  const resolved = resolveTerminalProfiles(
    [
      contribution("b.plugin", entries),
      contribution("a.plugin", [{ id: "one", title: "One", command: "node" }]),
      contribution("declarative.plugin", entries, { runtime: "declarative" }),
      contribution("inactive.plugin", entries, { activeInWorkspace: false }),
    ],
    "darwin",
  );
  assert.deepEqual(resolved.map((profile) => profile.key), ["a.plugin.one", "b.plugin.alpha", "b.plugin.zed"]);
  assert.equal(resolved.find((profile) => profile.key === "b.plugin.alpha").title, "Alpha");
  assert.deepEqual(resolveTerminalProfiles([contribution("b.plugin", entries)], "windows").map((p) => p.id), ["win", "zed"]);
  const many = Array.from({ length: 20 }, (_, index) => ({ id: `p${index}`, title: "x", command: "sh" }));
  assert.equal(resolveTerminalProfiles([contribution("c.plugin", many)], "linux").length, TERMINAL_PROFILE_MAX_ENTRIES);
});

test("keys, platforms and persisted references", () => {
  assert.equal(terminalProfileKey("acme.tools", "repl"), "acme.tools.repl");
  assert.deepEqual(parseTerminalProfileKey("acme.tools.repl"), { pluginId: "acme.tools", profileId: "repl" });
  assert.equal(parseTerminalProfileKey("nodot"), undefined);
  assert.equal(parseTerminalProfileKey("acme.Bad"), undefined);
  assert.equal(terminalProfilePlatform("MacIntel"), "darwin");
  assert.equal(terminalProfilePlatform("Win32"), "windows");
  assert.equal(terminalProfilePlatform("Linux x86_64"), "linux");

  assert.deepEqual(restoreTerminalProfileRef({ pluginId: "acme.tools", profileId: "repl", title: "REPL", workspaceFolder: "/w" }), {
    pluginId: "acme.tools",
    profileId: "repl",
    title: "REPL",
    workspaceFolder: "/w",
  });
  // A persisted reference never carries a launch spec: extra fields are dropped.
  const restored = restoreTerminalProfileRef({ pluginId: "acme.tools", profileId: "repl", title: "", argv: ["rm"], env: { A: "b" } });
  assert.deepEqual(restored, { pluginId: "acme.tools", profileId: "repl", title: "repl" });
  assert.equal(restoreTerminalProfileRef({ pluginId: "../x", profileId: "repl" }), undefined);
  assert.equal(restoreTerminalProfileRef({ pluginId: "acme", profileId: "Repl" }), undefined);
  assert.equal(restoreTerminalProfileRef("acme.repl"), undefined);
});

test("host launch specs are validated before display", () => {
  const raw = {
    pluginId: "acme.tools",
    pluginName: "Acme",
    profileId: "repl",
    title: "REPL",
    argv: ["/opt/acme/bin/repl", "--dir", "/work space"],
    env: { MODE: "dev" },
    cwd: "/work space",
    pluginExecutable: true,
    executableSha256: FINGERPRINT,
    fingerprint: FINGERPRINT,
    trusted: false,
    previouslyTrusted: true,
  };
  const launch = parseTerminalProfileLaunch(raw);
  assert.equal(launch.trusted, false);
  assert.equal(launch.previouslyTrusted, true);
  assert.equal(launch.executableSha256, FINGERPRINT);
  assert.equal(parseTerminalProfileLaunch({ ...raw, fingerprint: "short" }), undefined);
  assert.equal(parseTerminalProfileLaunch({ ...raw, argv: [] }), undefined);
  assert.equal(parseTerminalProfileLaunch({ ...raw, env: { "A-B": "x" } }), undefined);
  assert.equal(parseTerminalProfileLaunch({ ...raw, cwd: "" }), undefined);
  assert.equal(parseTerminalProfileLaunch({ ...raw, env: null }).env.MODE, undefined);
});

test("argv and env display quoting never hides characters", () => {
  assert.equal(quoteTerminalArg("--dir"), "--dir");
  assert.equal(quoteTerminalArg("/work space"), "'/work space'");
  assert.equal(quoteTerminalArg("it's"), `'it'\\''s'`);
  assert.equal(quoteTerminalArg(""), "''");
  assert.equal(quoteTerminalArg("$HOME"), "'$HOME'");
  assert.equal(quoteTerminalArg("a\u202eb"), "'a\\u{202e}b'");
  assert.equal(formatTerminalArgv(["python3", "-c", "print(1)"]), "python3 -c 'print(1)'");
  assert.deepEqual(formatTerminalEnv({ Z: "1", A: "x\u0007" }), ["A=x\\u{7}", "Z=1"]);
});

test("host error codes are recognised", () => {
  assert.equal(terminalProfileErrorCode(new Error("untrusted: terminal profile \"repl\" has not been confirmed")), "untrusted");
  assert.equal(terminalProfileErrorCode("changed: terminal profile changed"), "changed");
  assert.equal(terminalProfileErrorCode(new Error("IPC call failed: forbidden: missing shell")), "forbidden");
  assert.equal(terminalProfileErrorCode(new Error("unavailable: plugin disabled")), "unavailable");
  assert.equal(terminalProfileErrorCode(new Error("boom")), undefined);
  assert.equal(terminalProfileErrorCode(new Error("notchanged: x")), undefined);
});

test("install preview lists declared command lines", () => {
  const summary = terminalProfilePreviewSummary({
    terminalProfiles: [
      { id: "repl", title: "REPL", command: "${pluginRoot}/bin/repl", args: ["-i"], env: { MODE: "x" } },
      { id: "Bad", title: "x", command: "sh" },
    ],
  });
  assert.equal(summary.length, 1);
  assert.equal(summary[0].command, "${pluginRoot}/bin/repl");
  assert.deepEqual(summary[0].envNames, ["MODE"]);
  assert.deepEqual(terminalProfilePreviewSummary(undefined), []);
});

test("the WebView sends only a profile reference to the host", async () => {
  const service = await readSource("../src/services/terminal-profile-service.ts");
  for (const command of ["plugin_host_terminal_profile_prepare", "plugin_host_terminal_profile_trust", "plugin_host_terminal_profile_start"]) {
    assert.match(service, new RegExp(`"${command}"`));
  }
  const payloads = [...service.matchAll(/payload: \{([\s\S]*?)\n\s*\}/g)].map((match) => match[1]).join("\n");
  assert.ok(payloads.includes("pluginId"));
  assert.doesNotMatch(payloads, /\b(argv|args|command|env|extraEnv|customPath|cwd):/);
  const terminal = await readSource("../src/components/terminal/LocalTerminal.vue");
  assert.match(terminal, /if \(!props\.profile\) \{[\s\S]*?return invokeBackend<string>\("start_local_shell"/);
  // The profile path never falls back to the system shell on failure.
  assert.match(terminal, /if \(props\.profile\) \{\s*blockProfile\(error\);\s*return;/);
});

test("open default shell replaces only the profile pane, keeping its id and cwd", () => {
  const profile = { pluginId: "acme.tools", profileId: "repl", title: "REPL" };
  const profilePane = { id: "p1", type: "local", shellType: "system", initialCwd: "/work", profile, reloadVersion: 2 };
  const plainPane = { id: "p2", type: "local", shellType: "zsh", initialCwd: "/other" };
  const tree = { id: "split", type: "split", direction: "horizontal", children: [profilePane, plainPane], sizes: [0.5, 0.5] };

  const next = replaceProfilePaneWithDefaultShell(tree, "p1");
  assert.notEqual(next, tree);
  assert.deepEqual(next.children[0], {
    id: "p1",
    type: "local",
    shellType: "system",
    initialCwd: "/work",
    cwd: "/work",
    reloadVersion: 3,
  });
  assert.equal(next.children[1], plainPane);
  assert.deepEqual(next.sizes, [0.5, 0.5]);
  // Plain panes, unknown panes and leaves are left untouched (same reference).
  assert.equal(replaceProfilePaneWithDefaultShell(tree, "p2"), tree);
  assert.equal(replaceProfilePaneWithDefaultShell(tree, "missing"), tree);
  assert.equal(replaceProfilePaneWithDefaultShell(plainPane, "p2"), plainPane);
  assert.equal(replaceProfilePaneWithDefaultShell(profilePane, "p1").profile, undefined);
});

test("every view hosting terminal panes handles open default shell", async () => {
  const tree = await readSource("../src/components/terminal/TerminalPaneLayout.vue");
  assert.match(tree, /useDefaultShell: \[paneId: string\]/);
  const workspace = await readSource("../src/views/TerminalWorkspaceView.vue");
  assert.match(workspace, /@use-default-shell="useDefaultShell"/);
  assert.match(workspace, /await closeNativeSession\(paneId\);\s*terminalWorkspace\.replaceProfilePaneWithShell\(paneId\);/);
  const sessions = await readSource("../src/views/SessionsWorkspaceView.vue");
  assert.match(sessions, /@use-default-shell="useDefaultShellInSessionPane\(entry\.key, \$event\)"/);
  const handler = sessions.match(/async function useDefaultShellInSessionPane[\s\S]*?\n\}/)?.[0] ?? "";
  assert.match(handler, /"close_local_shell"/);
  assert.match(handler, /replaceProfilePaneWithDefaultShell\(item\.paneRoot, paneId\)/);
  // The store delegates to the same pure helper.
  const store = await readSource("../src/stores/terminal-workspace.ts");
  assert.match(store, /root: replaceProfilePaneWithDefaultShell\(current\.root, paneId\)/);
  // Every TerminalPaneLayout host wires the emit.
  for (const [name, source] of [["TerminalWorkspaceView", workspace], ["SessionsWorkspaceView", sessions]]) {
    const hosts = [...source.matchAll(/<TerminalPaneLayout\b[\s\S]*?\/>/g)].map((match) => match[0]);
    assert.ok(hosts.length > 0, name);
    for (const host of hosts) assert.match(host, /@use-default-shell=/, name);
  }
});

test("a profile pane never re-attaches without the host gate", async () => {
  const terminal = await readSource("../src/components/terminal/LocalTerminal.vue");
  // A failed prepare blocks the pane instead of trying to re-attach with an empty fingerprint.
  assert.doesNotMatch(terminal, /prepareTerminalProfile\(props\.profile\)\.catch/);
  assert.match(terminal, /fingerprint: launch\.fingerprint,\s*attachIfExists: true,/);
});

test("persisted layouts keep only the profile reference", async () => {
  const store = await readSource("../src/stores/terminal-workspace.ts");
  assert.match(store, /profile: restoreTerminalProfileRef\(value\.profile\)/);
  const types = await readSource("../src/domain/terminal/types.ts");
  assert.match(types, /profile: node\.profile \? \{ \.\.\.node\.profile \} : undefined/);
});

test("the consumer is registered and no AI tool can launch a profile", async () => {
  const index = await readSource("../src/services/contributions/index.ts");
  assert.match(index, /\/\/ terminalProfiles \(Phase D\)\n\s*registerTerminalProfileContributionConsumer,/);
  // v1: AI terminal tools cannot launch plugin profiles.
  const aiTools = await readSource("../src/services/ai-tools/terminal-tools.ts");
  assert.doesNotMatch(aiTools, /terminal_profile|terminal-profile|terminalProfile/);
  const zh = await readSource("../src/i18n/locales/zh.ts");
  const en = await readSource("../src/i18n/locales/en.ts");
  const keys = (source) => [...source.matchAll(/"(pluginContrib\.terminalProfiles\.[^"]+)"/g)].map((match) => match[1]).sort();
  assert.ok(keys(zh).length > 10);
  assert.deepEqual(keys(zh), keys(en));
});
