import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";
import { URL } from "node:url";
import {
  COMMAND_SNIPPET_COMMAND_MAX_BYTES,
  COMMAND_SNIPPET_COMMAND_MAX_LINES,
  COMMAND_SNIPPET_DESCRIPTION_MAX_RUNES,
  COMMAND_SNIPPET_LOCALES_MAX,
  COMMAND_SNIPPET_MAX_LIBRARIES,
  COMMAND_SNIPPET_MAX_PER_LIBRARY,
  COMMAND_SNIPPET_MAX_PER_PLUGIN,
  COMMAND_SNIPPET_TAG_MAX_RUNES,
  COMMAND_SNIPPET_TAGS_MAX,
  COMMAND_SNIPPET_TITLE_MAX_RUNES,
  COMMAND_SNIPPET_VARIABLE_DEFAULT_MAX_RUNES,
  COMMAND_SNIPPET_VARIABLE_TITLE_MAX_RUNES,
  COMMAND_SNIPPET_VARIABLE_VALUE_MAX_RUNES,
  COMMAND_SNIPPET_VARIABLES_MAX,
  commandSnippetInsertionPlan,
  commandSnippetPreviewSummary,
  commandSnippetShell,
  commandSnippetsForTerminal,
  commandSnippetValueIssue,
  expandCommandSnippet,
  filterCommandSnippets,
  normalizeCommandSnippetText,
  parseCommandSnippetLibrary,
  resolveCommandSnippets,
} from "../src/domain/terminal/command-snippets.ts";

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");
const readFixture = async (name) =>
  JSON.parse(await readSource(`../../tools/verify/fixtures/contributions/${name}/plugin.json`));

function contribution(pluginId, entries, overrides = {}) {
  return {
    pluginId,
    pluginName: `${pluginId} name`,
    version: "1.0.0",
    source: "local",
    runtime: "declarative",
    activeInWorkspace: true,
    point: { entries, issues: [] },
    ...overrides,
  };
}

test("domain constants mirror the contribution contract", async () => {
  const contract = JSON.parse(await readSource("../../plugins/api/contribution-points.json"));
  const point = contract.points.commandSnippets;
  assert.equal(point.status, "enabled");
  assert.deepEqual(point.kinds, ["declarative", "process"]);
  assert.equal(point.maxEntries, COMMAND_SNIPPET_MAX_LIBRARIES);
  assert.equal(point.limits.commandsPerLibrary, COMMAND_SNIPPET_MAX_PER_LIBRARY);
  assert.equal(point.limits.commandsPerPlugin, COMMAND_SNIPPET_MAX_PER_PLUGIN);
  assert.equal(point.limits.titleMaxRunes, COMMAND_SNIPPET_TITLE_MAX_RUNES);
  assert.equal(point.limits.descriptionMaxRunes, COMMAND_SNIPPET_DESCRIPTION_MAX_RUNES);
  assert.equal(point.limits.localesMax, COMMAND_SNIPPET_LOCALES_MAX);
  assert.equal(point.limits.commandMaxBytes, COMMAND_SNIPPET_COMMAND_MAX_BYTES);
  assert.equal(point.limits.commandMaxLines, COMMAND_SNIPPET_COMMAND_MAX_LINES);
  assert.equal(point.limits.variablesMax, COMMAND_SNIPPET_VARIABLES_MAX);
  assert.equal(point.limits.variableTitleMaxRunes, COMMAND_SNIPPET_VARIABLE_TITLE_MAX_RUNES);
  assert.equal(point.limits.variableDefaultMaxRunes, COMMAND_SNIPPET_VARIABLE_DEFAULT_MAX_RUNES);
  assert.equal(point.limits.variableValueMaxRunes, COMMAND_SNIPPET_VARIABLE_VALUE_MAX_RUNES);
  assert.equal(point.limits.tagsMax, COMMAND_SNIPPET_TAGS_MAX);
  assert.equal(point.limits.tagMaxRunes, COMMAND_SNIPPET_TAG_MAX_RUNES);
  assert.deepEqual(point.lists.targets, ["local", "ssh", "docker"]);
  assert.deepEqual(point.lists.quoteModes, ["none", "posix"]);
  const vueCopy = JSON.parse(await readSource("../src/domain/plugins/contribution-points.json"));
  assert.deepEqual(vueCopy.points.commandSnippets, point);
});

test("valid fixture libraries parse and resolve for active plugins only", async () => {
  const manifest = await readFixture("valid/commandSnippets-inline-process");
  const [library] = manifest.contributes.commandSnippets;
  const commands = parseCommandSnippetLibrary(library, { pluginId: manifest.id, pluginName: manifest.name });
  assert.equal(commands.length, 3);
  assert.deepEqual(commands[0].targets, ["local", "ssh"]);
  assert.equal(commands[1].command, "docker compose pull\ndocker compose up -d");
  // Non-identifier {{...}} text is literal, not an undeclared variable.
  assert.equal(commands[2].command, "docker ps --format '{{.Names}}'");

  const resolved = resolveCommandSnippets([
    contribution("b.plugin", [library]),
    contribution("a.plugin", [library], { activeInWorkspace: false }),
  ]);
  assert.deepEqual([...new Set(resolved.map((snippet) => snippet.pluginId))], ["b.plugin"]);
  assert.equal(resolved[0].key, "b.plugin/docker/logs");
  assert.equal(commandSnippetsForTerminal(resolved, "docker", "linux").length, 0);
  assert.equal(commandSnippetsForTerminal(resolved, "ssh", "windows").length, 3);
  assert.equal(filterCommandSnippets(resolved, "LOGS", "en").length, 1);
});

test("out-of-contract commands are rejected like the host", async () => {
  assert.equal(normalizeCommandSnippetText("echo hi\r"), undefined);
  assert.equal(normalizeCommandSnippetText("echo \u001b[201~"), undefined);
  assert.equal(normalizeCommandSnippetText("echo \u202eevil"), undefined);
  assert.equal(normalizeCommandSnippetText(Array.from({ length: 33 }, () => "x")), undefined);
  assert.equal(normalizeCommandSnippetText("x".repeat(4097)), undefined);
  assert.equal(normalizeCommandSnippetText("echo hi  \n\n"), "echo hi");
  const base = { id: "lib", title: "Lib" };
  const undeclared = parseCommandSnippetLibrary(
    { ...base, commands: [{ id: "a", title: "A", command: "echo {{name}}" }] },
    { pluginId: "p", pluginName: "p" },
  );
  assert.deepEqual(undeclared, []);
  assert.equal(parseCommandSnippetLibrary({ ...base, targets: ["windows"], commands: [] }, { pluginId: "p", pluginName: "p" }), undefined);
  for (const name of ["commandSnippets-escape-char", "commandSnippets-carriage-return", "commandSnippets-undeclared-variable", "commandSnippets-bidi"]) {
    const manifest = await readFixture(`invalid/${name}`);
    const entries = manifest.contributes.commandSnippets;
    const resolved = resolveCommandSnippets([contribution("p", entries)]);
    assert.equal(resolved.length, 0, name);
  }
});

test("expansion quotes declared values and never accepts multi-line or control input", () => {
  const snippet = {
    command: "ls {{dir}} && echo {{raw}} {{.Names}} {{other}}",
    variables: [
      { name: "dir", title: "", default: "", quote: "posix" },
      { name: "raw", title: "", default: "x", quote: "none" },
    ],
  };
  assert.deepEqual(expandCommandSnippet(snippet, { dir: "my dir's", raw: "ok" }, { shell: "posix" }), {
    text: "ls 'my dir'\\''s' && echo ok {{.Names}} {{other}}",
    unquoted: [],
  });
  assert.equal(expandCommandSnippet(snippet, { dir: "a'b" }, { shell: "powershell" }).text.startsWith("ls 'a''b'"), true);
  assert.equal(expandCommandSnippet(snippet, { dir: "/tmp/safe" }, { shell: "posix" }).text.startsWith("ls /tmp/safe "), true);
  assert.equal(expandCommandSnippet(snippet, { dir: "" }, { shell: "posix" }).text.startsWith("ls '' "), true);
  assert.deepEqual(expandCommandSnippet(snippet, { dir: "a b" }, { shell: "cmd" }).unquoted, ["dir"]);
  assert.equal(expandCommandSnippet(snippet, { dir: "a\nrm -rf /" }, { shell: "posix" }).invalid, "dir");
  assert.equal(expandCommandSnippet(snippet, { raw: "a\rb" }, { shell: "posix" }).invalid, "raw");
  assert.equal(commandSnippetValueIssue("a\u0007"), "control");
  assert.equal(commandSnippetValueIssue("a\u2028b"), "control");
  assert.equal(commandSnippetValueIssue("x".repeat(1025)), "too-long");
  assert.equal(commandSnippetShell("local", "windows"), "powershell");
  assert.equal(commandSnippetShell("ssh", "windows"), "posix");
});

test("insertion plan strips newlines, never emits CR and blocks multi-line outside bracketed paste", () => {
  assert.deepEqual(commandSnippetInsertionPlan("echo hi\n\n", false), { mode: "single", text: "echo hi" });
  assert.deepEqual(commandSnippetInsertionPlan("echo hi\r\n", false), { mode: "single", text: "echo hi" });
  assert.deepEqual(commandSnippetInsertionPlan("a\nb\n", false), { mode: "blocked", text: "a\nb" });
  assert.deepEqual(commandSnippetInsertionPlan("a\r\nb", true), { mode: "multiline", text: "a\nb" });
  for (const text of ["a\rb", "x\r", "a\nb\r\n"]) {
    assert.equal(commandSnippetInsertionPlan(text, true).text.includes("\r"), false);
  }
});

test("install preview lists libraries, targets and command counts", async () => {
  const manifest = await readFixture("valid/commandSnippets-inline-process");
  assert.deepEqual(commandSnippetPreviewSummary(manifest.contributes), [
    { library: "docker", title: "Docker", targets: ["local", "ssh"], commands: 3 },
  ]);
  const declarative = await readFixture("valid/commandSnippets-file-declarative");
  const summary = commandSnippetPreviewSummary(declarative.contributes);
  assert.ok(summary.length >= 1);
  assert.equal(commandSnippetPreviewSummary({}).length, 0);
});

test("terminal wiring only pastes the planned text and never writes CR", async () => {
  const panel = await readSource("../src/components/terminal/TerminalCommandSnippetsPanel.vue");
  assert.match(panel, /commandSnippetInsertionPlan\(expansion\.value\.text, props\.bracketedPasteMode\(\)\)/);
  assert.match(panel, /:allow-trailing-newline="false"/);
  assert.doesNotMatch(panel, /v-html|innerHTML/);
  const panelCode = panel.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  assert.doesNotMatch(panelCode, /\\r/);
  const confirm = await readSource("../src/components/terminal/TerminalPasteConfirmDialog.vue");
  assert.match(confirm, /allowTrailingNewline/);
  for (const [path, kind] of [
    ["../src/components/terminal/LocalTerminal.vue", "local"],
    ["../src/components/terminal/SshTerminal.vue", "ssh"],
    ["../src/components/ssh/SshDockerTerminal.vue", "docker"],
  ]) {
    const source = await readSource(path);
    const body = source.slice(source.indexOf("function insertCommandSnippet"));
    const fn = body.slice(0, body.indexOf("\n}\n"));
    // SSH and docker pastes go through the predictive echo binding so they are never predicted.
    assert.match(fn, kind === "local" ? /xterm\?\.paste\(text\)/ : /predictiveEcho\.paste\(text\)/, path);
    assert.doesNotMatch(fn, /\\r|writeTo|queue\w*Input|sendInput/, path);
    assert.match(source, new RegExp(`terminal-kind="${kind}"`), path);
    assert.match(source, /:bracketed-paste-mode="bracketedPasteMode"/, path);
    assert.match(source, /modes\.bracketedPasteMode === true/, path);
  }
  const index = await readSource("../src/services/contributions/index.ts");
  assert.match(index, /^\s+registerCommandSnippetContributionConsumer,$/m);
});
