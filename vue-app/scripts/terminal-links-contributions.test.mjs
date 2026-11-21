import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import {
  TERMINAL_LINK_ACTIVE_PATTERNS_MAX,
  TERMINAL_LINK_CAPTURE_MAX_RUNES,
  TERMINAL_LINK_LINE_SCAN_MAX_CHARS,
  TERMINAL_LINK_MATCHES_PER_LINE,
  TERMINAL_LINK_MAX_ENTRIES,
  TERMINAL_LINK_OVERRUNS_BEFORE_DISABLE,
  TERMINAL_LINK_PATTERN_MAX_LENGTH,
  TERMINAL_LINK_PER_LINE_BUDGET_MS,
  TERMINAL_LINK_URL_MAX_LENGTH,
  TERMINAL_LINK_WRAPPED_ROWS_MAX,
  compileTerminalLinkRule,
  parseTerminalLinkEntry,
  resolveTerminalLinkFile,
  resolveTerminalLinkRules,
  resolveTerminalLinkUrl,
  scanTerminalLine,
  terminalLinkAbsolutePath,
  terminalLinkHoverText,
  terminalLinkPatternGroups,
  terminalLinkPreviewSummary,
  terminalLinkUrlTemplateValid,
} from "../src/domain/terminal/terminal-links.ts";

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");
const readFixture = async (name) =>
  JSON.parse(await readSource(`../../tools/verify/fixtures/contributions/${name}/plugin.json`));
const PLUGIN = { pluginId: "fixture.links", pluginName: "Fixture links" };

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

function compile(entry) {
  const rule = parseTerminalLinkEntry(entry, PLUGIN);
  assert.ok(rule, JSON.stringify(entry));
  const result = compileTerminalLinkRule(rule);
  assert.ok("rule" in result, JSON.stringify(result));
  return result.rule;
}

test("domain constants mirror the contribution contract", async () => {
  const contract = JSON.parse(await readSource("../../plugins/api/contribution-points.json"));
  const point = contract.points.terminalLinks;
  assert.equal(point.status, "enabled");
  assert.equal(point.maxEntries, TERMINAL_LINK_MAX_ENTRIES);
  assert.equal(point.limits.patternMaxLength, TERMINAL_LINK_PATTERN_MAX_LENGTH);
  assert.equal(point.limits.urlMaxLength, TERMINAL_LINK_URL_MAX_LENGTH);
  assert.equal(point.limits.lineScanMaxChars, TERMINAL_LINK_LINE_SCAN_MAX_CHARS);
  assert.equal(point.limits.wrappedRowsMax, TERMINAL_LINK_WRAPPED_ROWS_MAX);
  assert.equal(point.limits.matchesPerLine, TERMINAL_LINK_MATCHES_PER_LINE);
  assert.equal(point.limits.perLineBudgetMs, TERMINAL_LINK_PER_LINE_BUDGET_MS);
  assert.equal(point.limits.overrunsBeforeDisable, TERMINAL_LINK_OVERRUNS_BEFORE_DISABLE);
  assert.equal(point.limits.activePatternsMax, TERMINAL_LINK_ACTIVE_PATTERNS_MAX);
  assert.equal(point.limits.captureMaxRunes, TERMINAL_LINK_CAPTURE_MAX_RUNES);
  assert.deepEqual(point.lists.terminals, ["local", "ssh", "docker"]);
  assert.deepEqual(point.lists.targetTypes, ["url", "file"]);
  assert.deepEqual(point.lists.schemes, ["https", "http"]);
  const vueCopy = JSON.parse(await readSource("../src/domain/plugins/contribution-points.json"));
  assert.deepEqual(vueCopy.points.terminalLinks, point);
});

test("pattern subset rejects backreferences, lookaround, named groups and inline flags", () => {
  assert.equal(terminalLinkPatternGroups("(a)(?:b)[(]\\("), 1);
  for (const pattern of ["(a)\\1", "(?<=x)a", "(?=x)a", "(?!x)a", "(?<name>a)", "\\k<n>", "(?i)a", "\\p{L}", "", "a".repeat(257)]) {
    assert.equal(terminalLinkPatternGroups(pattern), undefined, pattern);
  }
  assert.equal(terminalLinkPatternGroups("(a)".repeat(10)), undefined);
});

test("URL templates keep a fixed scheme and host", () => {
  assert.equal(terminalLinkUrlTemplateValid("https://jira.example.com/browse/{1}", 1), true);
  assert.equal(terminalLinkUrlTemplateValid("https://example.com/search?q={1}", 1), true);
  assert.equal(terminalLinkUrlTemplateValid("https://{1}.example.com/", 1), false);
  assert.equal(terminalLinkUrlTemplateValid("https://example.com{1}", 1), false);
  assert.equal(terminalLinkUrlTemplateValid("https://example.com?q={1}", 1), false);
  assert.equal(terminalLinkUrlTemplateValid("javascript:alert({1})", 1), false);
  assert.equal(terminalLinkUrlTemplateValid("https://user:pw@example.com/{1}", 1), false);
  assert.equal(terminalLinkUrlTemplateValid("https://example.com/{2}", 1), false);
});

test("captures are percent-encoded and cannot change the host", () => {
  assert.equal(
    resolveTerminalLinkUrl("https://jira.example.com/browse/{1}", ["x", "../@evil.com/?a=b#c"]),
    "https://jira.example.com/browse/..%2F%40evil.com%2F%3Fa%3Db%23c",
  );
  assert.equal(resolveTerminalLinkUrl("https://example.com/{1}", ["", undefined]), "https://example.com/");
  assert.equal(resolveTerminalLinkUrl("https://example.com/{1}", ["", "a".repeat(513)]), undefined);
});

test("fixture rules scan, resolve and respect terminals", async () => {
  const manifest = await readFixture("valid/terminalLinks-declarative");
  const rules = resolveTerminalLinkRules([contribution(manifest.id, manifest.contributes.terminalLinks)]);
  assert.equal(rules.length, 2);
  assert.deepEqual(rules[1].terminals, ["local"]);
  const compiled = rules.map((rule) => compileTerminalLinkRule(rule).rule);
  const { links, overruns } = scanTerminalLine("see ABC-123 and src/app.mcdemo:12:3 at https://x.test/ABC-9", compiled, { now: () => 0 });
  assert.deepEqual(overruns, []);
  assert.deepEqual(links.map((link) => link.text), ["ABC-123", "src/app.mcdemo:12:3"]);
  assert.equal(resolveTerminalLinkUrl(links[0].rule.target.url, links[0].groups), "https://jira.example.com/browse/ABC-123");
  assert.deepEqual(resolveTerminalLinkFile(links[1].rule.target, links[1].groups), { path: "src/app.mcdemo", line: 12, column: 3 });
  // A file target defaults to local terminals and cannot target ssh/docker.
  assert.deepEqual(parseTerminalLinkEntry({ id: "f", pattern: "(\\S+):(\\d+)", target: { type: "file", path: "{1}" } }, PLUGIN).terminals, ["local"]);
  assert.equal(parseTerminalLinkEntry({ id: "f", pattern: "(\\S+)", target: { type: "file", path: "{1}" }, terminals: ["ssh"] }, PLUGIN), undefined);
  // Inactive plugins contribute nothing.
  assert.equal(resolveTerminalLinkRules([contribution("p", manifest.contributes.terminalLinks, { activeInWorkspace: false })]).length, 0);
});

test("scan is bounded by characters and matches", () => {
  const rule = compile({ id: "num", pattern: "\\d+", target: { type: "url", url: "https://example.com/{0}" } });
  const many = Array.from({ length: 100 }, (_, index) => String(index)).join(" ");
  const bounded = scanTerminalLine(many, [rule], { now: () => 0 });
  assert.equal(bounded.links.length, TERMINAL_LINK_MATCHES_PER_LINE);
  assert.deepEqual(bounded.overruns, []);
  const long = `${"x".repeat(TERMINAL_LINK_LINE_SCAN_MAX_CHARS)} 42`;
  const truncated = scanTerminalLine(long, [rule], { now: () => 0 });
  assert.equal(truncated.links.length, 0);
  assert.deepEqual(truncated.overruns, []);
});

test("scan reports a time budget overrun before starting a rule", () => {
  const rule = compile({ id: "num", pattern: "\\d+", target: { type: "url", url: "https://example.com/{0}" } });
  let clock = 0;
  const slow = scanTerminalLine("42", [rule], { now: () => (clock += TERMINAL_LINK_PER_LINE_BUDGET_MS + 1) });
  assert.deepEqual(slow.links, []);
  assert.deepEqual(slow.overruns, ["fixture.links/num"]);
});

test("scan reports a time budget overrun at the eighth match check", () => {
  const rule = compile({ id: "num", pattern: "\\d+", target: { type: "url", url: "https://example.com/{0}" } });
  const many = Array.from({ length: 100 }, (_, index) => String(index)).join(" ");
  let clockCalls = 0;
  const slow = scanTerminalLine(many, [rule], {
    now: () => (++clockCalls < 3 ? 0 : TERMINAL_LINK_PER_LINE_BUDGET_MS + 1),
  });
  assert.deepEqual(slow.links.map((link) => link.text), ["0", "1", "2", "3", "4", "5", "6", "7"]);
  assert.deepEqual(slow.overruns, ["fixture.links/num"]);
  assert.equal(clockCalls, 3);
});

test("scan reports a time budget overrun after a rule finishes", () => {
  const rule = compile({ id: "num", pattern: "\\d+", target: { type: "url", url: "https://example.com/{0}" } });
  let clockCalls = 0;
  const slow = scanTerminalLine("42", [rule], {
    now: () => (++clockCalls < 3 ? 0 : TERMINAL_LINK_PER_LINE_BUDGET_MS + 1),
  });
  assert.deepEqual(slow.links.map((link) => link.text), ["42"]);
  assert.deepEqual(slow.overruns, ["fixture.links/num"]);
  assert.equal(clockCalls, 3);
});

test("frontend-only rejections: catastrophic and empty-match fixtures", async () => {
  const catastrophic = await readFixture("invalid/terminalLinks-catastrophic");
  const [unsafe] = resolveTerminalLinkRules([contribution(catastrophic.id, catastrophic.contributes.terminalLinks)]);
  assert.ok(unsafe);
  assert.ok("issue" in compileTerminalLinkRule(unsafe));
  const empty = await readFixture("invalid/terminalLinks-empty-match");
  const rules = resolveTerminalLinkRules([contribution(empty.id, empty.contributes.terminalLinks)]);
  for (const rule of rules) assert.ok("issue" in compileTerminalLinkRule(rule), rule.pattern);
  for (const name of ["terminalLinks-backreference", "terminalLinks-lookbehind", "terminalLinks-host-placeholder", "terminalLinks-javascript-scheme", "terminalLinks-file-on-ssh", "terminalLinks-too-long"]) {
    const manifest = await readFixture(`invalid/${name}`);
    assert.equal(resolveTerminalLinkRules([contribution(manifest.id, manifest.contributes.terminalLinks)]).length, 0, name);
  }
});

test("file paths, hover text and install preview", async () => {
  assert.equal(terminalLinkAbsolutePath("src/a.ts", "/work/app/"), "/work/app/src/a.ts");
  assert.equal(terminalLinkAbsolutePath("./a.ts", "C:\\work"), "C:\\work\\a.ts");
  assert.equal(terminalLinkAbsolutePath("/etc/hosts", undefined), "/etc/hosts");
  assert.equal(terminalLinkAbsolutePath("~/a", "/work"), undefined);
  assert.equal(terminalLinkAbsolutePath("a", undefined), undefined);
  assert.equal(resolveTerminalLinkFile({ type: "file", path: "{1}", line: "{2}" }, ["", "a", "0"]), undefined);
  assert.equal(terminalLinkHoverText("https://x/<b>\u202e", "P\u0007"), "https://x/<b> · P");
  const manifest = await readFixture("valid/terminalLinks-declarative");
  assert.deepEqual(terminalLinkPreviewSummary(manifest.contributes).map((entry) => entry.target), ["https://jira.example.com", "file"]);
});

test("terminal wiring never renders HTML and disposes the provider", async () => {
  const service = await readSource("../src/services/terminal-plugin-links.ts");
  assert.doesNotMatch(service, /innerHTML|insertAdjacentHTML|v-html/);
  assert.match(service, /normalizeWebUrl\(resolved\)/);
  assert.match(service, /openSystemBrowser\(url\)/);
  assert.match(service, /options\.kind !== "local"/);
  for (const [path, kind] of [
    ["../src/components/terminal/LocalTerminal.vue", "local"],
    ["../src/components/terminal/SshTerminal.vue", "ssh"],
    ["../src/components/ssh/SshDockerTerminal.vue", "docker"],
  ]) {
    const source = await readSource(path);
    assert.match(source, new RegExp(`attachPluginTerminalLinks\\(xterm, \\{\\s+kind: "${kind}"`), path);
    const unmount = source.slice(source.indexOf("onBeforeUnmount("));
    assert.ok(unmount.indexOf("pluginLinks?.dispose()") >= 0, path);
    assert.ok(unmount.indexOf("pluginLinks?.dispose()") < unmount.indexOf("xterm?.dispose()"), path);
  }
  const index = await readSource("../src/services/contributions/index.ts");
  assert.match(index, /^\s+registerTerminalLinkContributionConsumer,$/m);
});
