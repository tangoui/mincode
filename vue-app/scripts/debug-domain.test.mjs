import assert from "node:assert/strict";
import test from "node:test";
import { existingKey, projectRootKey, toRelativeKey, toSourcePath } from "../src/domain/debug/paths.ts";
import {
  isDebuggerAddressPath,
  isStaleDebugSourceTabPath,
  isVirtualDebugSource,
  virtualSourceDocumentInfo,
  virtualSourceDocumentPath,
} from "../src/domain/debug/virtual-source.ts";
import { DEBUGGER_ADDRESS_LIMITS, addressBreakpointName, addressSourceSession, isValidDebuggerAddress } from "../src/domain/debug/address-breakpoints.ts";
import {
  configurationPayload,
  expandConfigurationVariables,
  newConfigurationFromEntryPoint,
  newConfigurationFromSnippet,
  normalizeProjectDebugStates,
} from "../src/domain/debug/configurations.ts";

const python = {
  pluginId: "mincode.python-debug",
  pluginName: "Python Debug",
  type: "python",
  title: "Python (debugpy)",
  languages: ["python"],
  configurationSnippets: [{ label: "Current file", request: "launch", body: { program: "${file}" } }],
  hasEntryPoints: true,
  hasCheck: true,
};

test("toRelativeKey is case-insensitive on darwin", () => {
  assert.equal(toRelativeKey("/Users/A/Proj", "/users/a/proj/src/app.py", true), "src/app.py");
  assert.equal(toRelativeKey("/Users/A/Proj", "/users/a/proj/src/app.py", false), null);
  assert.equal(toRelativeKey("/Users/A/Proj/", "/Users/A/Proj/src/app.py", false), "src/app.py");
  assert.equal(toRelativeKey("C:\\P", "c:\\p\\src\\app.py", true), "src/app.py");
});

test("toRelativeKey rejects paths outside the root", () => {
  assert.equal(toRelativeKey("/Users/A/Proj", "/Users/A/Project/app.py", false), null);
  assert.equal(toRelativeKey("/Users/A/Proj", "/Users/A/other.py", false), null);
  assert.equal(toRelativeKey("/Users/A/Proj", "/Users/A/Proj", false), null);
  assert.equal(toRelativeKey("/Users/A/Proj", "/Users/A/Proj/../x.py", false), null);
  assert.equal(toRelativeKey("/Users/A/Proj", "src/app.py", false), null);
  assert.equal(toRelativeKey("/", "/etc/hosts", false), "etc/hosts");
});

test("toSourcePath keeps the project root spelling", () => {
  assert.equal(toSourcePath("/Users/A/Proj", "src/app.py"), "/Users/A/Proj/src/app.py");
  assert.equal(toSourcePath("/Users/A/Proj/", "src/app.py"), "/Users/A/Proj/src/app.py");
  assert.equal(toSourcePath("C:\\P", "src/app.py"), "C:\\P\\src\\app.py");
  assert.equal(toSourcePath("C:\\", "app.py"), "C:\\app.py");
  assert.equal(toSourcePath("/", "etc/hosts"), "/etc/hosts");
});

test("projectRootKey trims trailing separators but keeps file-system roots", () => {
  assert.equal(projectRootKey("/Users/A/Proj/"), "/Users/A/Proj");
  assert.equal(projectRootKey("C:\\P\\"), "C:\\P");
  assert.equal(projectRootKey("/"), "/");
  assert.equal(projectRootKey("C:\\"), "C:\\");
  assert.equal(projectRootKey("C:/"), "C:/");
});

test("existingKey reuses a key that matches ignoring case only when case-insensitive", () => {
  assert.equal(existingKey(["src/App.py", "b.py"], "src/app.py", true), "src/App.py");
  assert.equal(existingKey(["src/App.py", "src/app.py"], "src/app.py", true), "src/app.py");
  assert.equal(existingKey(["src/App.py"], "src/app.py", false), "src/app.py");
  assert.equal(existingKey([], "x", true), "x");
});

test("expandConfigurationVariables replaces nested ${file}", () => {
  const fields = {
    program: "${file}",
    args: ["--root", "${projectRoot}", 3],
    env: { WHERE: "${projectRoot}/data:${file}" },
    justMyCode: false,
  };
  const expanded = expandConfigurationVariables(fields, { file: "/p/app.py", projectRoot: "/p" });
  assert.deepEqual(expanded, {
    program: "/p/app.py",
    args: ["--root", "/p", 3],
    env: { WHERE: "/p/data:/p/app.py" },
    justMyCode: false,
  });
  assert.equal(fields.program, "${file}", "input is not mutated");
});

test("expandConfigurationVariables throws without active file", () => {
  assert.throws(
    () => expandConfigurationVariables({ env: { A: ["${file}"] } }, { projectRoot: "/p" }),
    { message: "debug.config.noActiveFile" },
  );
  assert.deepEqual(expandConfigurationVariables({ cwd: "${projectRoot}" }, { projectRoot: "/p" }), { cwd: "/p" });
});

test("configurationPayload puts request inside the configuration", () => {
  const cfg = { id: "c1", name: "app", pluginId: python.pluginId, type: "python", request: "launch", fields: { program: "${file}" } };
  assert.deepEqual(configurationPayload(cfg, { file: "/p/a.py", projectRoot: "/p" }), { request: "launch", program: "/p/a.py" });
});

test("newConfigurationFromSnippet copies the snippet body", () => {
  const snippet = python.configurationSnippets[0];
  const cfg = newConfigurationFromSnippet(python, snippet);
  assert.equal(cfg.name, "Current file");
  assert.equal(cfg.pluginId, python.pluginId);
  assert.equal(cfg.type, "python");
  assert.equal(cfg.request, "launch");
  assert.deepEqual(cfg.fields, { program: "${file}" });
  assert.ok(cfg.id);
  cfg.fields.program = "x";
  assert.equal(snippet.body.program, "${file}");
  assert.equal(newConfigurationFromSnippet(python, snippet, "Mine").name, "Mine");
  assert.notEqual(newConfigurationFromSnippet(python, snippet).id, cfg.id);
});

test("newConfigurationFromEntryPoint reuses same-named config", () => {
  const entry = { line: 3, label: "Run app.py", configuration: { request: "launch", program: "src/app.py" } };
  const created = newConfigurationFromEntryPoint(python, entry, "app.py");
  assert.equal(created.name, "app");
  assert.equal(created.request, "launch");
  assert.deepEqual(created.fields, { program: "src/app.py" });

  const existing = { ...created, id: "kept", fields: { program: "src/app.py" } };
  const other = { ...created, id: "other-type", type: "node" };
  assert.equal(newConfigurationFromEntryPoint(python, entry, "app.py", [other, existing]), existing);
  assert.notEqual(newConfigurationFromEntryPoint(python, entry, "main.py", [other]).id, "other-type");
});

test("newConfigurationFromEntryPoint does not reuse a same-named config for a different file", () => {
  const src = newConfigurationFromEntryPoint(python, { line: 1, label: "Run", configuration: { request: "launch", program: "src/app.py" } }, "app.py");
  const testsEntry = { line: 1, label: "Run", configuration: { request: "launch", program: "tests/app.py" } };
  const tests = newConfigurationFromEntryPoint(python, testsEntry, "app.py", [src]);
  assert.notEqual(tests.id, src.id);
  assert.equal(tests.name, "app (2)");
  assert.deepEqual(tests.fields, { program: "tests/app.py" });
  // Same program with edited fields (or another request) is not reused either.
  const edited = { ...src, fields: { program: "tests/app.py", args: ["--x"] } };
  const attach = { ...src, id: "attach", name: "app (2)", request: "attach", fields: { program: "tests/app.py" } };
  assert.equal(newConfigurationFromEntryPoint(python, testsEntry, "app.py", [edited, attach]).name, "app (3)");
  // An identical config is reused whatever its suffix.
  assert.equal(newConfigurationFromEntryPoint(python, testsEntry, "app.py", [src, tests]), tests);
});

test("normalizeProjectDebugStates drops malformed entries", () => {
  const states = normalizeProjectDebugStates({
    "/p": {
      configs: [{ id: "c1", name: "a", pluginId: "x", type: "python", request: "launch", fields: {} }, { id: 3 }],
      selectedConfigId: "c1",
      breakpoints: { "a.py": [{ line: 2, enabled: true }, { line: "x" }], "b.py": "bad" },
      exceptionFilters: { python: ["raised", 1] },
      watches: ["x", 2],
      panelHeight: 300,
    },
    "/q": "bad",
  });
  assert.deepEqual(Object.keys(states), ["/p"]);
  assert.equal(states["/p"].configs.length, 1);
  assert.deepEqual(states["/p"].breakpoints, { "a.py": [{ line: 2, enabled: true }] });
  assert.deepEqual(states["/p"].exceptionFilters, { python: ["raised"] });
  assert.deepEqual(states["/p"].watches, ["x"]);
  assert.equal(states["/p"].panelHeight, 300);
  assert.deepEqual(normalizeProjectDebugStates(null), {});
});

// ---- session model (Task 5) ----

import { debuggeeNotices, isBottomFrame, newSessionView, reduceDebugEvent, splitOutput, stackTraceComplete } from "../src/domain/debug/session-model.ts";

function sessionView(overrides = {}) {
  return {
    ...newSessionView({
      id: "s1",
      projectRoot: "/p",
      configId: "c1",
      configName: "App",
      type: "python",
      noDebug: false,
      capabilities: {},
      startedAt: 1,
    }),
    ...overrides,
  };
}

test("reduceDebugEvent stopped sets paused", () => {
  const before = sessionView();
  const after = reduceDebugEvent(before, { sessionId: "s1", event: "stopped", body: { reason: "breakpoint", threadId: 7 } });
  assert.equal(after.state, "paused");
  assert.equal(after.stoppedReason, "breakpoint");
  assert.equal(after.stoppedThreadId, 7);
  assert.equal(before.state, "running", "the input is not mutated");
  const resumed = reduceDebugEvent(after, { sessionId: "s1", event: "continued", body: { threadId: 7 } });
  assert.equal(resumed.state, "running");
  assert.equal(resumed.stoppedThreadId, undefined);
});

test("breakpointsSet marks unverified lines", () => {
  const after = reduceDebugEvent(sessionView(), {
    sessionId: "s1",
    event: "mincode.breakpointsSet",
    body: { path: "/p/app.py", breakpoints: [{ line: 3, verified: true }, { line: 9, verified: false }] },
  });
  assert.deepEqual(after.verified["/p/app.py"], [
    { line: 3, verified: true },
    { line: 9, verified: false },
  ]);
  const filters = reduceDebugEvent(after, { sessionId: "s1", event: "mincode.breakpointsSet", body: { path: "", exceptionFilters: [] } });
  assert.deepEqual(Object.keys(filters.verified), ["/p/app.py"], "exception filter results are not source breakpoints");
});

test("breakpointsSet for a debugger address is kept apart from project paths", () => {
  const view = withBreakpoints("/p/app.py", [{ line: 3, verified: true }]);
  const after = reduceDebugEvent(view, {
    sessionId: "s1",
    event: "mincode.breakpointsSet",
    body: { path: "x-lib://pkg/Type.class", address: true, breakpoints: [{ id: 7, line: 4, verified: false, message: "not loaded" }] },
  });
  assert.equal(after.verified, view.verified, "project path results are untouched");
  assert.deepEqual(after.addressVerified, { "x-lib://pkg/Type.class": [{ line: 4, verified: false, id: 7, message: "not loaded" }] });
  assert.deepEqual(after.breakpointIds, {}, "address ids are not project path ids");
  assert.deepEqual(after.addressBreakpointIds, { 7: "x-lib://pkg/Type.class" });
  const failed = reduceDebugEvent(view, {
    sessionId: "s1",
    event: "mincode.breakpointsSet",
    body: { path: "x-lib://pkg/Type.class", address: true, breakpoints: null, error: "boom" },
  });
  assert.deepEqual(failed.addressVerified, { "x-lib://pkg/Type.class": [] });
  assert.equal(failed.verified, view.verified);
});

test("a later breakpoint event for an address updates the address results", () => {
  const address = "x-lib://pkg/Type.class?=a/b";
  const view = reduceDebugEvent(withBreakpoints("/p/app.py", [{ id: 1, line: 3, verified: true }]), {
    sessionId: "s1",
    event: "mincode.breakpointsSet",
    body: { path: address, address: true, breakpoints: [{ id: 7, line: 40, verified: false, message: "not loaded" }] },
  });
  const byId = breakpointEvent(view, { reason: "changed", breakpoint: { id: 7, verified: true, line: 41 } });
  assert.deepEqual(byId.addressVerified[address], [{ line: 40, verified: true, id: 7, actualLine: 41 }]);
  assert.equal(byId.verified, view.verified);
  // An unknown id whose source is an address goes to that address, never to `verified`.
  const bySource = breakpointEvent(view, { reason: "changed", breakpoint: { id: 9, verified: true, line: 40, source: { path: address } } });
  assert.deepEqual(Object.keys(bySource.verified), ["/p/app.py"]);
  assert.deepEqual(bySource.addressVerified[address], [{ line: 40, verified: true, id: 9 }]);
  const other = breakpointEvent(view, { reason: "new", breakpoint: { id: 11, verified: true, line: 5, source: { path: "x-lib://other/Thing.class" } } });
  assert.deepEqual(Object.keys(other.verified), ["/p/app.py"]);
  assert.deepEqual(other.addressVerified["x-lib://other/Thing.class"], [{ line: 5, verified: true, id: 11 }]);
  assert.deepEqual(other.addressBreakpointIds, { 7: address, 11: "x-lib://other/Thing.class" });
  const removed = breakpointEvent(view, { reason: "removed", breakpoint: { id: 7 } });
  assert.deepEqual(removed.addressVerified[address], []);
  // A file:// source is not a debugger address: it stays on the path side.
  const file = breakpointEvent(view, { reason: "new", breakpoint: { verified: true, line: 2, source: { path: "file:///p/x.py" } } });
  assert.deepEqual(file.addressVerified, view.addressVerified);
});

function withBreakpoints(path, breakpoints, view = sessionView()) {
  return reduceDebugEvent(view, { sessionId: "s1", event: "mincode.breakpointsSet", body: { path, breakpoints } });
}

function breakpointEvent(view, body, options) {
  return reduceDebugEvent(view, { sessionId: "s1", event: "breakpoint", body }, options);
}

test("breakpointsSet keeps ids", () => {
  const after = withBreakpoints("/p/A.java", [
    { id: 1, line: 3, verified: false, message: "class not loaded" },
    { line: 9, verified: true },
  ]);
  assert.deepEqual(after.verified["/p/A.java"], [
    { line: 3, verified: false, id: 1, message: "class not loaded" },
    { line: 9, verified: true },
  ]);
  assert.deepEqual(after.breakpointIds, { 1: "/p/A.java" });
  const replaced = withBreakpoints("/p/A.java", [{ id: 2, line: 3, verified: false }], after);
  assert.deepEqual(replaced.breakpointIds, { 2: "/p/A.java" }, "ids of the replaced results are dropped");
});

test("breakpoint event updates verified state by id", () => {
  const before = withBreakpoints("/p/A.java", [
    { id: 1, line: 3, verified: false, message: "class not loaded" },
    { id: 2, line: 9, verified: false },
  ]);
  const after = breakpointEvent(before, { reason: "changed", breakpoint: { id: 1, verified: true } });
  assert.deepEqual(after.verified["/p/A.java"], [
    { line: 3, verified: true, id: 1 },
    { line: 9, verified: false, id: 2 },
  ]);
  assert.equal(before.verified["/p/A.java"][0].verified, false, "the input is not mutated");
  const again = breakpointEvent(after, { reason: "changed", breakpoint: { id: 2, verified: false, message: "no code" } });
  assert.deepEqual(again.verified["/p/A.java"][1], { line: 9, verified: false, id: 2, message: "no code" });
  assert.equal(breakpointEvent(after, { reason: "changed", breakpoint: { id: 7, verified: true } }), after, "an unknown id without a source is ignored");
  assert.equal(breakpointEvent(after, { reason: "changed" }), after, "a body without a breakpoint is ignored");
});

test("breakpoint event with moved line keeps requested line and records actualLine", () => {
  const before = withBreakpoints("/p/A.java", [
    { id: 1, line: 4, verified: false },
    { id: 2, line: 6, verified: false },
  ]);
  const after = breakpointEvent(before, { reason: "changed", breakpoint: { id: 1, verified: true, line: 6 } });
  assert.deepEqual(after.verified["/p/A.java"], [
    { line: 4, verified: true, id: 1, actualLine: 6 },
    { line: 6, verified: false, id: 2 },
  ]);
  assert.deepEqual(after.breakpointIds, { 1: "/p/A.java", 2: "/p/A.java" });
  const back = breakpointEvent(after, { reason: "changed", breakpoint: { id: 1, verified: true, line: 4 } });
  assert.deepEqual(back.verified["/p/A.java"][0], { line: 4, verified: true, id: 1 }, "actualLine is dropped once it matches");
  // Without an id, the adapter's line also finds a result moved there (one requested on that line comes first).
  const unmatched = withBreakpoints("/p/B.java", [{ line: 4, verified: true, actualLine: 7 }]);
  const byActual = breakpointEvent(unmatched, { reason: "changed", breakpoint: { verified: false, line: 7, source: { path: "/p/B.java" } } });
  assert.deepEqual(byActual.verified["/p/B.java"], [{ line: 4, verified: false, actualLine: 7 }]);
});

test("setBreakpoints results pin to the requested line and record a moved actualLine", async () => {
  const { breakpointResults } = await import("../src/domain/debug/session-model.ts");
  assert.deepEqual(breakpointResults([4, 9], [{ line: 5, verified: true }, { verified: false }]), [
    { line: 4, verified: true, actualLine: 5 },
    { line: 9, verified: false },
  ]);
  const view = withBreakpoints("/p/A.java", [{ line: 4, verified: true, actualLine: 5 }]);
  assert.deepEqual(view.verified["/p/A.java"], [{ line: 4, verified: true, actualLine: 5 }], "breakpointsSet keeps actualLine");
});

test("removed reason drops the result", () => {
  const before = withBreakpoints("/p/A.java", [
    { id: 1, line: 3, verified: true },
    { id: 2, line: 9, verified: true },
  ]);
  const after = breakpointEvent(before, { reason: "removed", breakpoint: { id: 1, verified: false } });
  assert.deepEqual(after.verified["/p/A.java"], [{ line: 9, verified: true, id: 2 }]);
  assert.deepEqual(after.breakpointIds, { 2: "/p/A.java" });
});

test("unknown id with source path merges by line", () => {
  const before = withBreakpoints("/P/A.java", [{ line: 3, verified: false }]);
  const merged = breakpointEvent(
    before,
    { reason: "new", breakpoint: { id: 5, verified: true, line: 3, source: { path: "/p/a.java" } } },
    { caseInsensitive: true },
  );
  assert.deepEqual(Object.keys(merged.verified), ["/P/A.java"], "the existing key is reused ignoring case");
  assert.deepEqual(merged.verified["/P/A.java"], [{ line: 3, verified: true, id: 5 }]);
  assert.deepEqual(merged.breakpointIds, { 5: "/P/A.java" });
  const sensitive = breakpointEvent(before, { reason: "new", breakpoint: { verified: true, line: 3, source: { path: "/p/a.java" } } });
  assert.deepEqual(sensitive.verified["/P/A.java"], [{ line: 3, verified: false }], "case matters by default");
  assert.deepEqual(sensitive.verified["/p/a.java"], [{ line: 3, verified: true }]);
  const added = breakpointEvent(before, { reason: "new", breakpoint: { verified: false, line: 8, source: { path: "/P/A.java" } } });
  assert.deepEqual(added.verified["/P/A.java"], [
    { line: 3, verified: false },
    { line: 8, verified: false },
  ]);
  const removed = breakpointEvent(before, { reason: "removed", breakpoint: { verified: false, line: 3, source: { path: "/P/A.java" } } });
  assert.deepEqual(removed.verified["/P/A.java"], []);
  assert.equal(breakpointEvent(before, { reason: "new", breakpoint: { verified: true, source: { path: "/P/A.java" } } }), before, "no line, no merge");
});

test("session ends on mincode.sessionEnded and clears paused state", () => {
  const paused = sessionView({
    state: "paused",
    stoppedThreadId: 1,
    stoppedReason: "step",
    frames: [{ id: 1, name: "main", line: 1, column: 1 }],
    selectedFrameId: 1,
  });
  const ended = reduceDebugEvent(paused, { sessionId: "s1", event: "mincode.sessionEnded", body: { reason: "exited" } });
  assert.equal(ended.state, "ended");
  assert.equal(ended.endReason, "exited");
  assert.equal(ended.stoppedThreadId, undefined);
  assert.equal(ended.stoppedReason, undefined);
  assert.deepEqual(ended.frames, []);
  assert.equal(ended.selectedFrameId, undefined);
  const late = reduceDebugEvent(ended, { sessionId: "s1", event: "stopped", body: { reason: "pause", threadId: 1 } });
  assert.equal(late.state, "ended", "an ended session stays ended");
});

test("output is capped at 2000 lines", () => {
  let view = sessionView();
  for (let i = 0; i < 2001; i += 1) {
    view = reduceDebugEvent(view, { sessionId: "s1", event: "output", body: { category: "stdout", output: `line ${i}\n` } });
  }
  assert.equal(view.output.length, 2000);
  assert.deepEqual(view.output[0], { seq: 2, category: "stdout", text: "line 1\n" });
  assert.deepEqual(view.output.at(-1), { seq: 2001, category: "stdout", text: "line 2000\n" });
  const terminal = reduceDebugEvent(view, { sessionId: "s1", event: "mincode.runInTerminal", body: { terminalSessionId: "t1", title: "x" } });
  assert.equal(terminal.consoleTerminalId, "t1");
});

test("every runInTerminal terminal is kept; the latest is shown", () => {
  let view = sessionView();
  const run = (id) => reduceDebugEvent(view, { sessionId: "s1", event: "mincode.runInTerminal", body: { terminalSessionId: id, title: "x" } });
  view = run("t1");
  view = run("t2");
  assert.equal(view.consoleTerminalId, "t2");
  assert.deepEqual(view.consoleTerminalIds, ["t1", "t2"]);
  assert.equal(run("t2"), view, "a repeated id changes nothing");
});

test("an external-terminal run shows a notice instead of a terminal", () => {
  const view = sessionView();
  const external = reduceDebugEvent(view, {
    sessionId: "s1",
    event: "mincode.runInTerminal",
    body: { external: true, terminal: "Terminal", title: "Python Debug Console" },
  });
  assert.deepEqual(external.consoleNotice, { kind: "external", terminal: "Terminal" });
  assert.equal(external.consoleTerminalId, undefined);
  assert.equal(external.consoleTerminalIds, undefined);
});

test("a fallback to the integrated terminal attaches it and keeps the reason", () => {
  const view = sessionView();
  const fallback = reduceDebugEvent(view, {
    sessionId: "s1",
    event: "mincode.runInTerminal",
    body: {
      terminalSessionId: "t1",
      title: "x",
      externalFallback: { code: "unavailable", message: "external terminal: no graphical session" },
    },
  });
  assert.equal(fallback.consoleTerminalId, "t1");
  assert.deepEqual(fallback.consoleNotice, {
    kind: "externalFallback",
    code: "unavailable",
    message: "external terminal: no graphical session",
  });
  // A later plain integrated run is not a fallback.
  const plain = reduceDebugEvent(fallback, { sessionId: "s1", event: "mincode.runInTerminal", body: { terminalSessionId: "t2", title: "x" } });
  assert.equal(plain.consoleNotice, undefined);
  // Malformed fallback details are ignored, the terminal still attaches.
  const odd = reduceDebugEvent(view, { sessionId: "s1", event: "mincode.runInTerminal", body: { terminalSessionId: "t3", externalFallback: "x" } });
  assert.equal(odd.consoleTerminalId, "t3");
  assert.equal(odd.consoleNotice, undefined);
});

test("debuggeeNotices lists program and python", () => {
  const notices = debuggeeNotices(
    {
      prepareId: "p",
      launch: {},
      type: "python",
      kind: "",
      projectRoot: "/p",
      request: "launch",
      arguments: { program: "/p/app.py", python: ["/usr/bin/python3"], justMyCode: true, cwd: "x".repeat(300) },
    },
    "Full arguments",
  );
  assert.equal(notices[0], "request: launch");
  assert.ok(notices.includes("program: /p/app.py"));
  assert.ok(notices.includes('python: ["/usr/bin/python3"]'));
  assert.ok(!notices.some((line) => line.startsWith("justMyCode")));
  const cwd = notices.find((line) => line.startsWith("cwd: "));
  assert.ok(cwd && cwd.length <= "cwd: ".length + 200);
  assert.ok(notices.at(-1).startsWith("Full arguments: {"));
  assert.ok(notices.at(-1).includes("justMyCode"));
});

test("a long output line is split into 8 KiB entries without losing text", () => {
  const long = "x".repeat(20000);
  const view = reduceDebugEvent(sessionView(), { sessionId: "s1", event: "output", body: { category: "stdout", output: long } });
  assert.deepEqual(view.output.map((entry) => entry.text.length), [8192, 8192, 20000 - 16384]);
  assert.equal(view.output.map((entry) => entry.text).join(""), long);
  const short = reduceDebugEvent(view, { sessionId: "s1", event: "output", body: { category: "stdout", output: "ok\n" } });
  assert.equal(short.output.at(-1).text, "ok\n");
  assert.deepEqual(short.output.map((entry) => entry.seq), [1, 2, 3, 4]);
});

test("a 64 KiB output batch of many lines is kept whole and in order", () => {
  const lines = Array.from({ length: 5900 }, (_, i) => `line ${String(i).padStart(5, "0")}\n`);
  const batch = lines.join("");
  assert.ok(batch.length >= 48 * 1024 && batch.length <= 64 * 1024);
  const view = reduceDebugEvent(sessionView(), { sessionId: "s1", event: "output", body: { category: "stdout", output: batch } });
  assert.equal(view.output.map((entry) => entry.text).join(""), batch);
  for (const entry of view.output) {
    assert.ok(entry.text.length <= 8 * 1024);
    assert.ok(entry.text.endsWith("\n"), "entries end at a line break");
  }
});

test("output over the entry cap drops the oldest entries", () => {
  let view = sessionView();
  const batch = Array.from({ length: 900 }, (_, i) => `${String(i).padStart(9, "0")}\n`).join(""); // 9000 chars → 2 entries
  for (let i = 0; i < 1500; i += 1) {
    view = reduceDebugEvent(view, { sessionId: "s1", event: "output", body: { category: "stdout", output: `${i}:${batch}` } });
  }
  assert.equal(view.output.length, 2000);
  assert.equal(view.output.at(-1).seq, 3000);
  assert.ok(view.output.at(-1).text.endsWith("000000899\n"));
  assert.ok(view.output[0].text.startsWith("500:"), "the oldest 500 events are gone");
});

test("splitOutput keeps surrogate pairs and trailing newlines", () => {
  const text = `${"a".repeat(7)}\u{1F600}${"b".repeat(3)}`; // the emoji sits on chars 8..9
  const pieces = splitOutput(text, 8);
  assert.deepEqual(pieces, ["a".repeat(7), `\u{1F600}${"b".repeat(3)}`]);
  for (const piece of pieces) assert.ok(!/[\uD800-\uDBFF]$/.test(piece) && !/^[\uDC00-\uDFFF]/.test(piece), JSON.stringify(piece));
  assert.deepEqual(splitOutput("ab\ncd\n", 4), ["ab\n", "cd\n"]);
  assert.deepEqual(splitOutput("ok\n", 8), ["ok\n"]);
});

// ---- run configuration form (Task 8) ----

import { readFileSync } from "node:fs";
import {
  applyConfigFields,
  configFormFields,
  draftsFor,
  parseConfigForm,
  remoteServerArgs,
  switchRequestFields,
  validateConfigFields,
} from "../src/domain/debug/config-form.ts";
import { duplicateConfiguration } from "../src/domain/debug/configurations.ts";

const pythonDebugger = JSON.parse(readFileSync(new URL("../../official-plugins/python-debug/plugin.json", import.meta.url), "utf8")).contributes.debuggers[0];

test("parseConfigForm maps python-debug schema", () => {
  const launch = parseConfigForm(pythonDebugger.configurationSchema.launch);
  const byKey = Object.fromEntries(launch.map((field) => [field.key, field]));
  assert.deepEqual(launch.map((field) => field.key), ["program", "module", "args", "env", "cwd", "console", "python", "justMyCode", "showReturnValue", "subProcess"]);
  assert.equal(byKey.program.kind, "string");
  assert.equal(byKey.program.label, "program");
  assert.equal(byKey.program.required, false);
  assert.match(byKey.program.description, /Script to run/);
  assert.equal(byKey.args.kind, "stringList");
  assert.equal(byKey.env.kind, "stringMap");
  assert.equal(byKey.console.kind, "enum");
  assert.deepEqual(byKey.console.options, ["integratedTerminal", "internalConsole", "externalTerminal"]);
  assert.equal(byKey.console.default, "integratedTerminal");
  assert.equal(byKey.justMyCode.kind, "boolean");
  assert.equal(byKey.justMyCode.default, true);

  const attach = Object.fromEntries(parseConfigForm(pythonDebugger.configurationSchema.attach).map((field) => [field.key, field]));
  assert.equal(attach.port.kind, "integer");
  assert.equal(attach.port.default, 5678);
  assert.equal(attach.pathMappings.kind, "json");
});

test("unknown shapes fall back to json", () => {
  const fields = parseConfigForm({
    type: "object",
    required: ["count", "deep"],
    properties: {
      count: { type: "number", title: "Count" },
      nullable: { type: ["string", "null"] },
      mixed: { type: ["string", "number"] },
      numbers: { type: "array", items: { type: "number" } },
      deep: { type: "object", properties: { a: { type: "string" } } },
      untyped: {},
      oddEnum: { type: "string", enum: ["a", 1] },
    },
  });
  const byKey = Object.fromEntries(fields.map((field) => [field.key, field]));
  assert.equal(byKey.count.kind, "number");
  assert.equal(byKey.count.label, "Count");
  assert.equal(byKey.count.required, true);
  assert.equal(byKey.nullable.kind, "string");
  assert.equal(byKey.mixed.kind, "json");
  assert.equal(byKey.numbers.kind, "json");
  assert.equal(byKey.deep.kind, "json");
  assert.equal(byKey.deep.required, true);
  assert.equal(byKey.untyped.kind, "json");
  assert.equal(byKey.oddEnum.kind, "json");
  assert.deepEqual(parseConfigForm(undefined), []);
  assert.deepEqual(parseConfigForm({ type: "object" }), []);
  // Fields the schema does not describe are offered as JSON.
  const withExtra = configFormFields(pythonDebugger.configurationSchema.attach, { port: 1, legacy: { a: 1 } });
  assert.deepEqual(withExtra.at(-1), { key: "legacy", label: "legacy", required: false, kind: "json" });
});

test("validateConfigFields reports bad JSON", () => {
  const fields = parseConfigForm({
    type: "object",
    required: ["program"],
    properties: {
      program: { type: "string" },
      port: { type: "integer" },
      ratio: { type: "number" },
      env: { type: "object", additionalProperties: { type: "string" } },
      pathMappings: { type: "array", items: { type: "object" } },
    },
  });
  assert.deepEqual(validateConfigFields(fields, { program: " ", port: "1.5", ratio: "x", env: "A=1\nnoequals", pathMappings: "[{" }), {
    program: "debug.configDialog.required",
    port: "debug.configDialog.invalidInteger",
    ratio: "debug.configDialog.invalidNumber",
    env: "debug.configDialog.invalidMapLine",
    pathMappings: "debug.configDialog.invalidJson",
  });
  assert.deepEqual(validateConfigFields(fields, { program: "a.py", port: "5678", ratio: "0.5", env: "A=1\n\nB=x=y", pathMappings: "" }), {});
});

test("drafts round-trip through applyConfigFields", () => {
  const fields = configFormFields(pythonDebugger.configurationSchema.launch, {});
  const original = { program: "${file}", args: ["--a", "b c"], env: { A: "1", B: "x=y" }, justMyCode: false, extra: 1 };
  const drafts = draftsFor(fields, original);
  assert.equal(drafts.args, "--a\nb c");
  assert.equal(drafts.env, "A=1\nB=x=y");
  assert.equal(drafts.module, "");
  assert.equal(drafts.justMyCode, false);
  assert.equal(drafts.console, "");
  // Undescribed fields are kept; emptied fields are removed.
  assert.deepEqual(applyConfigFields(fields, { ...drafts, args: "", env: "A=2\r\n" }, original), { program: "${file}", env: { A: "2" }, justMyCode: false, extra: 1 });
  // A boolean left at its default is not written unless it was set.
  assert.deepEqual(applyConfigFields(fields, draftsFor(fields, { program: "a.py" }), { program: "a.py" }), { program: "a.py" });
  assert.deepEqual(draftsFor(fields, {}).justMyCode, true);

  const attach = configFormFields(pythonDebugger.configurationSchema.attach, {});
  const attachDrafts = draftsFor(attach, { host: "localhost", port: 5678, pathMappings: [{ localRoot: ".", remoteRoot: "/app" }] });
  assert.equal(attachDrafts.port, "5678");
  assert.equal(JSON.parse(attachDrafts.pathMappings)[0].remoteRoot, "/app");
  assert.deepEqual(applyConfigFields(attach, { ...attachDrafts, port: "6000", pathMappings: " " }, {}), { host: "localhost", port: 6000 });
});

test("remoteServerArgs substitutes the port", () => {
  const template = pythonDebugger.remote.argsTemplate;
  assert.equal(remoteServerArgs(template, "6000"), "python -m debugpy --listen 0.0.0.0:6000 --wait-for-client <script.py>");
  assert.equal(remoteServerArgs("a ${port} b ${port}", 7), "a 7 b 7");
  assert.equal(remoteServerArgs("a ${port}", ""), "a ${port}");
});

test("duplicateConfiguration copies with a new id", () => {
  const cfg = { id: "c1", name: "App", pluginId: python.pluginId, type: "python", request: "launch", fields: { args: ["x"] } };
  const copy = duplicateConfiguration(cfg, "App (copy)");
  assert.notEqual(copy.id, "c1");
  assert.equal(copy.name, "App (copy)");
  assert.deepEqual(copy.fields, cfg.fields);
  copy.fields.args.push("y");
  assert.deepEqual(cfg.fields.args, ["x"]);
});

test("values a list or map control cannot show losslessly are edited as JSON", () => {
  const launch = pythonDebugger.configurationSchema.launch;
  const kindOf = (values, key) => configFormFields(launch, values).find((field) => field.key === key).kind;
  assert.equal(kindOf({ args: ["a", "b c"] }, "args"), "stringList");
  assert.equal(kindOf({ args: ["", "a"] }, "args"), "json");
  assert.equal(kindOf({ args: ["a\nb"] }, "args"), "json");
  assert.equal(kindOf({ args: [1] }, "args"), "json");
  assert.equal(kindOf({ args: "a" }, "args"), "json");
  assert.equal(kindOf({ env: { A: "1", B: "x=y" } }, "env"), "stringMap");
  assert.equal(kindOf({ env: { "A=B": "x" } }, "env"), "json");
  assert.equal(kindOf({ env: { A: "x\ny" } }, "env"), "json");
  assert.equal(kindOf({ env: { " A": "1" } }, "env"), "json");
  assert.equal(kindOf({ env: { "": "1" } }, "env"), "json");
  assert.equal(kindOf({ env: { A: 1 } }, "env"), "json");
  assert.equal(kindOf({ justMyCode: "yes" }, "justMyCode"), "json");
  assert.equal(kindOf({ program: 3 }, "program"), "json");
  const attach = pythonDebugger.configurationSchema.attach;
  assert.equal(configFormFields(attach, { port: "5678" }).find((field) => field.key === "port").kind, "json");
  // A JSON field keeps the value exactly.
  const fields = configFormFields(launch, { args: ["", "a"] });
  const drafts = draftsFor(fields, { args: ["", "a"] });
  assert.deepEqual(applyConfigFields(fields, drafts, { args: ["", "a"] }), { args: ["", "a"] });
});

test("stringMap validation trims keys and rejects blank and duplicate keys", () => {
  const fields = parseConfigForm({ type: "object", properties: { env: { type: "object", additionalProperties: { type: "string" } } } });
  assert.deepEqual(validateConfigFields(fields, { env: " =x" }), { env: "debug.configDialog.invalidMapLine" });
  assert.deepEqual(validateConfigFields(fields, { env: "A=1\n A =2" }), { env: "debug.configDialog.duplicateMapKey" });
  assert.deepEqual(validateConfigFields(fields, { env: " A =1\nB= x " }), {});
  assert.deepEqual(applyConfigFields(fields, { env: " A =1\nB= x " }, {}), { env: { A: "1", B: " x " } });
});

test("numbers are plain decimals and port is 1-65535", () => {
  const fields = parseConfigForm({
    type: "object",
    properties: { port: { type: "integer" }, count: { type: "integer" }, ratio: { type: "number" }, other: { type: "number" } },
  });
  const check = (values) => validateConfigFields(fields, values);
  assert.deepEqual(check({ count: "0x10", ratio: "1e3", other: "Infinity" }), {
    count: "debug.configDialog.invalidInteger",
    ratio: "debug.configDialog.invalidNumber",
    other: "debug.configDialog.invalidNumber",
  });
  assert.deepEqual(check({ count: "-3", ratio: "+.5", other: "-1.25" }), {});
  assert.deepEqual(check({ port: "0" }), { port: "debug.configDialog.invalidPort" });
  assert.deepEqual(check({ port: "65536" }), { port: "debug.configDialog.invalidPort" });
  assert.deepEqual(check({ port: "5678" }), {});
  const numberPort = parseConfigForm({ type: "object", properties: { port: { type: "number" } } });
  assert.deepEqual(validateConfigFields(numberPort, { port: "1.5" }), { port: "debug.configDialog.invalidPort" });
});

test("switchRequestFields keeps the other request's fields until switching back", () => {
  const { launch, attach } = pythonDebugger.configurationSchema;
  const start = { program: "app.py", args: ["-v"], python: "/usr/bin/python3", justMyCode: false };
  const toAttach = switchRequestFields(start, launch, attach, {});
  assert.deepEqual(toAttach.fields, { python: "/usr/bin/python3", justMyCode: false });
  const edited = { ...toAttach.fields, port: 7000, python: "/opt/py" };
  const back = switchRequestFields(edited, attach, launch, toAttach.stash);
  // Launch-only fields come back; shared edits are kept; attach-only ones are put aside.
  assert.deepEqual(back.fields, { program: "app.py", args: ["-v"], python: "/opt/py", justMyCode: false });
  const again = switchRequestFields(back.fields, launch, attach, back.stash);
  assert.deepEqual(again.fields, { python: "/opt/py", justMyCode: false, port: 7000 });
});

// ---- inline values, hover expressions, evaluate history (Task 9) ----

const { computeInlineValues, hoverExpressionAt } = await import("../src/domain/debug/inline-values.ts");
const { rememberExpression } = await import("../src/domain/debug/evaluate-history.ts");

test("computeInlineValues shows values on lines that use them", () => {
  const lines = ["import os", "x = 1", "y = x + 2", "print(y)", "z = 3"];
  const values = computeInlineValues(lines, 4, [
    { name: "x", value: "1" },
    { name: "y", value: "3" },
    { name: "z", value: "3" },
  ]);
  assert.deepEqual(values, [
    { line: 2, text: "x = 1" },
    { line: 3, text: "y = 3, x = 1" },
    { line: 4, text: "y = 3" },
  ]);
  // Only [pausedLine - lookBack, pausedLine].
  assert.deepEqual(computeInlineValues(lines, 4, [{ name: "x", value: "1" }], { lookBack: 1 }), [{ line: 3, text: "x = 1" }]);
  // Whole words only, and not attributes of something else.
  assert.deepEqual(computeInlineValues(["xs = obj.x + x1 + 0x1f"], 1, [{ name: "x", value: "1" }]), []);
});

test("computeInlineValues ignores names inside strings and comments", () => {
  const lines = ['s = "x is here" + x', "t = 'x'  # x again", "u = 2 // x", "w = `x` + x"];
  const values = computeInlineValues(lines, 4, [{ name: "x", value: "7" }]);
  assert.deepEqual(values, [
    { line: 1, text: "x = 7" },
    { line: 4, text: "x = 7" },
  ]);
  assert.deepEqual(computeInlineValues(['a = "\\"x" + x'], 1, [{ name: "x", value: "7" }]), [{ line: 1, text: "x = 7" }]);
});

test("computeInlineValues truncates long values", () => {
  const values = computeInlineValues(["print(data)"], 1, [{ name: "data", value: "a".repeat(100) }]);
  assert.equal(values[0].text, `data = ${"a".repeat(59)}…`);
  assert.equal(computeInlineValues(["v"], 1, [{ name: "v", value: "abcdef" }], { maxValueChars: 4 })[0].text, "v = abc…");
  assert.equal(computeInlineValues(["v"], 1, [{ name: "v", value: "line1\nline2" }])[0].text, "v = line1 line2");
});

test("computeInlineValues limits to three per line", () => {
  const variables = ["a", "b", "c", "d"].map((name, index) => ({ name, value: String(index) }));
  assert.deepEqual(computeInlineValues(["d = a + b + c + a"], 1, variables), [{ line: 1, text: "d = 3, a = 0, b = 1" }]);
  assert.deepEqual(computeInlineValues(["d = a + b + c"], 1, variables, { maxPerLine: 1 }), [{ line: 1, text: "d = 3" }]);
});

test("hoverExpressionAt takes the word and its member chain", () => {
  const line = "total = order.items.count + self.x";
  assert.deepEqual(hoverExpressionAt(line, line.indexOf("items") + 2), {
    expression: "order.items",
    startColumn: line.indexOf("order") + 1,
    endColumn: line.indexOf("items") + 6,
  });
  assert.equal(hoverExpressionAt(line, 1)?.expression, "total");
  assert.equal(hoverExpressionAt(line, line.indexOf("x") + 1)?.expression, "self.x");
  assert.equal(hoverExpressionAt(line, line.indexOf("=") + 1), null);
  assert.equal(hoverExpressionAt('s = "name" # name', 7), null);
  assert.equal(hoverExpressionAt('s = "name" # name', 16), null);
  assert.equal(hoverExpressionAt("f().name", 6), null);
  assert.equal(hoverExpressionAt("n = 0x1f", 7), null);
});

test("rememberExpression keeps the latest 20 distinct expressions first", () => {
  let history = [];
  for (let index = 0; index < 25; index += 1) history = rememberExpression(history, `e${index}`);
  assert.equal(history.length, 20);
  assert.equal(history[0], "e24");
  history = rememberExpression(history, "e20");
  assert.equal(history[0], "e20");
  assert.equal(history.filter((item) => item === "e20").length, 1);
  assert.deepEqual(rememberExpression(["a"], "  "), ["a"]);
});

test("Java launch notices keep javaExec/vmArgs and path counts before long full arguments", () => {
  const notices = debuggeeNotices({ request: "launch", arguments: {
    classPaths: Array.from({ length: 400 }, (_, i) => `/long/path/${i}`),
    modulePaths: ["/module"], javaExec: "/custom/bin/java", vmArgs: "-javaagent:agent.jar",
  }});
  assert.ok(notices.includes("javaExec: /custom/bin/java"));
  assert.ok(notices.includes("vmArgs: -javaagent:agent.jar"));
  assert.ok(notices.includes("classPaths: 400 entries"));
  assert.ok(notices.includes("modulePaths: 1 entries"));
  assert.ok(!notices.at(-1).includes("/custom/bin/java"));
  assert.ok(notices.indexOf("javaExec: /custom/bin/java") < notices.length - 1);
});


test("persisted logpoints preserve literal text but discard blank or non-string messages", () => {
  const states = normalizeProjectDebugStates({ "/p": { breakpoints: { "a.py": [
    { line: 2, logMessage: "  value={value} <tag>  ", enabled: false },
    { line: 3, logMessage: " \t ", enabled: true },
    { line: 4, logMessage: 42, enabled: true },
  ] } } });
  assert.deepEqual(states["/p"].breakpoints["a.py"], [
    { line: 2, logMessage: "  value={value} <tag>  ", enabled: false },
    { line: 3, enabled: true },
    { line: 4, enabled: true },
  ]);
});


test("virtual debug sources are those without a local absolute path", () => {
  assert.equal(isVirtualDebugSource({ path: "/a/B.java" }), false);
  assert.equal(isVirtualDebugSource({ path: "C:\\a\\B.java" }), false);
  assert.equal(isVirtualDebugSource({ path: "C:/a/B.java", sourceReference: 0 }), false);
  assert.equal(isVirtualDebugSource({ path: "jdt://contents/java.base/java.lang.reflect/Method.class?x" }), true);
  assert.equal(isVirtualDebugSource({ sourceReference: 7 }), true);
  assert.equal(isVirtualDebugSource({ path: "/a/B.java", sourceReference: 7 }), false);
  assert.equal(isVirtualDebugSource({ name: "x" }), false);
  assert.equal(isVirtualDebugSource(undefined), false);
});

test("virtual source document paths are stable and end with the source name", () => {
  const source = { path: "jdt://contents/java.base/java.lang.reflect/Method.class?=demo/x", name: "Method.java", sourceReference: 7 };
  const path = virtualSourceDocumentPath("s1", source, "frame");
  assert.ok(path.startsWith("debug-source://s1/"), path);
  assert.ok(path.endsWith("/Method.java"), path);
  assert.equal(virtualSourceDocumentPath("s1", source, "frame"), path);
  assert.notEqual(virtualSourceDocumentPath("s2", source, "frame"), path);
  assert.ok(virtualSourceDocumentPath("s1", { sourceReference: 3 }, "eval").endsWith("/eval"));
  assert.ok(virtualSourceDocumentPath("s1", { path: "jdt://x/y/Foo.class" }, "f").endsWith("/Foo.class"));
  // Only one path segment after the session and key: no slash survives from the key.
  assert.equal(path.slice("debug-source://s1/".length).split("/").length, 2);
});


test("stale debugger tabs saved by older versions are recognised", () => {
  // Older versions joined java-debug's jdt:// address onto the project root and saved it.
  assert.equal(isStaleDebugSourceTabPath("/Users/a/java-demo/jdt://contents/java.base/java.lang/Class.class?=x/%3Cjava.lang(Class.class"), true);
  assert.equal(isStaleDebugSourceTabPath("C:/work/demo/jdt://contents/x/Y.class"), true);
  assert.equal(isStaleDebugSourceTabPath("jdt://contents/x/Y.class"), true);
  assert.equal(isStaleDebugSourceTabPath("debug-source://s1/ref%3A7/Method.java"), true);
  assert.equal(isStaleDebugSourceTabPath("/Users/a/java-demo/src/App.java"), false);
  assert.equal(isStaleDebugSourceTabPath("/Users/a/notes/jdt-tips.md"), false);
  assert.equal(isStaleDebugSourceTabPath("/Users/a/x/jdt:/y.java"), false);
});

import { variableNode, variableNodePath } from "../src/domain/debug/variable-tree.ts";

test("variable node paths are stable across refreshes and unique among siblings", () => {
  assert.equal(variableNodePath("scope:Local", "list", 0), "scope:Local/list");
  assert.equal(variableNodePath("scope:Local/list", "0", 0), "scope:Local/list/0");
  assert.equal(variableNodePath("scope:Local", "x", 1), "scope:Local/x#2");
  assert.equal(variableNodePath("scope:Local", "a/b", 0), "scope:Local/a%2Fb");
  const first = variableNode({ name: "list", value: "ArrayList size=3", variablesReference: 7 }, "scope:Local", 0);
  const again = variableNode({ name: "list", value: "ArrayList size=4", variablesReference: 9 }, "scope:Local", 0);
  assert.equal(first.key, again.key);
});

// ---- exception types (pause only on these exception types) ----

import { EXCEPTION_TYPE_LIMITS, normalizeExceptionTypes, parseExceptionTypesText } from "../src/domain/debug/exception-types.ts";
import contributionContract from "../src/domain/plugins/contribution-points.json" with { type: "json" };

test("normalizeProjectDebugStates keeps valid exception type lists and drops the rest", () => {
  const states = normalizeProjectDebugStates({
    "/p": {
      exceptionTypes: {
        java: ["java.lang.NullPointerException", 3, " com.example.AppError ", "java.lang.NullPointerException", "not a name", "", null],
        empty: [],
        other: "java.lang.Error",
        object: { 0: "a.B" },
      },
    },
    "/old": { configs: [], exceptionFilters: { java: ["caught"] } },
  });
  assert.deepEqual(states["/p"].exceptionTypes, { java: ["java.lang.NullPointerException", "com.example.AppError"], empty: [] });
  // Data saved before the list existed reads as no lists.
  assert.deepEqual(states["/old"].exceptionTypes, {});
  assert.deepEqual(normalizeProjectDebugStates({ "/x": { exceptionTypes: ["a.B"] } })["/x"].exceptionTypes, {});
});

test("normalizeProjectDebugStates keeps at most the host's number of stored exception types", () => {
  const many = Array.from({ length: 70 }, (_, i) => `x.E${i}`);
  const states = normalizeProjectDebugStates({ "/p": { exceptionTypes: { java: many } } });
  assert.deepEqual(states["/p"].exceptionTypes.java, many.slice(0, 64));
});

test("exception type limits come from the debuggers contract", () => {
  assert.equal(EXCEPTION_TYPE_LIMITS.max, contributionContract.points.debuggers.limits.exceptionTypesMax);
  assert.equal(EXCEPTION_TYPE_LIMITS.maxRunes, contributionContract.points.debuggers.limits.exceptionTypeMaxRunes);
});

test("normalizeExceptionTypes trims and de-duplicates in first-seen order", () => {
  assert.deepEqual(normalizeExceptionTypes([" java.lang.NullPointerException ", "NPE", "java.lang.NullPointerException", "$x._y"]), {
    ok: true,
    types: ["java.lang.NullPointerException", "NPE", "$x._y"],
  });
  assert.deepEqual(normalizeExceptionTypes([]), { ok: true, types: [] });
});

test("normalizeExceptionTypes rejects what the host rejects, with the first offending item", () => {
  for (const value of ["  ", "", "a b", "1abc", "java..Error", ".a", "a.", "java.*", "a-b", "é.Error", "a\u0000b"]) {
    const result = normalizeExceptionTypes(["ok.Fine", value]);
    assert.deepEqual(result, { ok: false, reason: "format", index: 1, value: value.trim() }, JSON.stringify(value));
  }
  const long = "a".repeat(257);
  assert.deepEqual(normalizeExceptionTypes([long]), { ok: false, reason: "tooLong", index: 0, value: long });
  // The length is checked first, like the host: a long invalid item is "tooLong".
  assert.equal(normalizeExceptionTypes(["*".repeat(300)]).reason, "tooLong");
  assert.equal(normalizeExceptionTypes(["a".repeat(256)]).ok, true);
});

test("normalizeExceptionTypes allows 64 distinct items, duplicates not counted", () => {
  const items = Array.from({ length: 64 }, (_, i) => `x.E${i}`);
  assert.equal(normalizeExceptionTypes([...items, "x.E0"]).ok, true);
  assert.deepEqual(normalizeExceptionTypes([...items, "x.E64"]), { ok: false, reason: "tooMany", index: 64, value: "x.E64" });
});

test("parseExceptionTypesText splits lines and commas and drops blank entries", () => {
  assert.deepEqual(parseExceptionTypesText("  java.lang.NullPointerException \r\n\n  \ncom.a.B, com.a.C,\n"), [
    "java.lang.NullPointerException",
    "com.a.B",
    "com.a.C",
  ]);
  assert.deepEqual(parseExceptionTypesText("   \n "), []);
  // Inner spaces are kept so the item is reported, not silently split.
  assert.deepEqual(parseExceptionTypesText("java.lang. Error"), ["java.lang. Error"]);
});

// ---- method (function) breakpoints ----

import {
  FUNCTION_BREAKPOINT_LIMITS,
  breakpointListItemResults,
  enabledFunctionBreakpointNames,
  functionBreakpointResults,
  normalizeFunctionBreakpointName,
} from "../src/domain/debug/function-breakpoints.ts";

test("normalizeProjectDebugStates reads method breakpoints per debugger type; old data has none", () => {
  const states = normalizeProjectDebugStates({
    "/p": {
      functionBreakpoints: {
        java: [
          { name: " app.Main#run ", enabled: true },
          { name: "app.Main#stop", enabled: false },
          { name: "app.Main#run", enabled: false },
          { name: "app.Other#go" },
          { name: "" },
          { name: "   " },
          { name: "two\nlines" },
          { name: "bidi‮override" },
          { name: "x".repeat(1025) },
          { name: 3 },
          "app.Main#bare",
          null,
        ],
        empty: [],
        notList: { name: "a#b" },
      },
    },
    "/old": { configs: [], exceptionTypes: { java: [] } },
  });
  assert.deepEqual(states["/p"].functionBreakpoints, {
    java: [
      { name: "app.Main#run", enabled: true },
      { name: "app.Main#stop", enabled: false },
      { name: "app.Other#go", enabled: true },
    ],
    empty: [],
  });
  assert.deepEqual(states["/old"].functionBreakpoints, {});
  assert.deepEqual(normalizeProjectDebugStates({ "/x": { functionBreakpoints: [{ name: "a#b" }] } })["/x"].functionBreakpoints, {});
});

test("normalizeProjectDebugStates keeps at most the host's number of method breakpoints", () => {
  const many = Array.from({ length: 260 }, (_, i) => ({ name: `app.C#m${i}`, enabled: true }));
  const states = normalizeProjectDebugStates({ "/p": { functionBreakpoints: { java: many } } });
  assert.equal(states["/p"].functionBreakpoints.java.length, 256);
  assert.deepEqual(states["/p"].functionBreakpoints.java.at(-1), { name: "app.C#m255", enabled: true });
  assert.equal(FUNCTION_BREAKPOINT_LIMITS.max, contributionContract.points.debuggers.limits.functionBreakpointsMax);
  assert.equal(FUNCTION_BREAKPOINT_LIMITS.maxRunes, 1024);
});

test("normalizeFunctionBreakpointName trims and refuses what the host refuses", () => {
  assert.deepEqual(normalizeFunctionBreakpointName("  app.Main#run \t"), { ok: true, name: "app.Main#run" });
  assert.deepEqual(normalizeFunctionBreakpointName("   "), { ok: false, reason: "empty" });
  assert.deepEqual(normalizeFunctionBreakpointName("é".repeat(1024)), { ok: true, name: "é".repeat(1024) });
  assert.deepEqual(normalizeFunctionBreakpointName("😀".repeat(1025)), { ok: false, reason: "tooLong" });
  for (const value of ["a\nb", "a\rb", "a\tb", "a\u0000b", "a\u007fb", "a\u0085b", "a b", "a‪b", "a⁦b", "a​b", "a﻿b", "a\ud800b"]) {
    assert.deepEqual(normalizeFunctionBreakpointName(value), { ok: false, reason: "invalid" }, JSON.stringify(value));
  }
});

test("enabledFunctionBreakpointNames keeps the enabled names in order", () => {
  assert.deepEqual(
    enabledFunctionBreakpointNames([
      { name: "a#b", enabled: true },
      { name: "c#d", enabled: false },
      { name: "e#f", enabled: true },
    ]),
    ["a#b", "e#f"],
  );
  assert.deepEqual(enabledFunctionBreakpointNames(undefined), []);
});

test("breakpointListItemResults answers exactly one result per sent breakpoint", () => {
  // A short reply: the missing ones are unverified.
  assert.deepEqual(breakpointListItemResults([{ verified: true, id: 4 }], 3), [{ verified: true, id: 4 }, { verified: false }, { verified: false }]);
  // Extra results belong to nothing that was sent.
  assert.deepEqual(breakpointListItemResults([{ verified: true }, { verified: true }], 1), [{ verified: true }]);
  assert.deepEqual(breakpointListItemResults(null, 2, "bad"), [
    { verified: false, message: "bad" },
    { verified: false, message: "bad" },
  ]);
  assert.deepEqual(breakpointListItemResults([{ verified: false, message: "not loaded", id: 0 }, "junk"], 2), [
    { verified: false, id: 0, message: "not loaded" },
    { verified: false },
  ]);
});

test("functionBreakpointResults pairs results with the sent names", () => {
  assert.deepEqual(functionBreakpointResults(["a#b", "c#d"], [{ verified: true, id: 1 }]), [
    { name: "a#b", verified: true, id: 1 },
    { name: "c#d", verified: false },
  ]);
});

test("the start-time method breakpoint results land on the names the session started with", () => {
  const view = sessionView({ functionBreakpoints: [{ name: "app.Main#run", verified: false }, { name: "app.Main#nope", verified: false }] });
  const after = reduceDebugEvent(view, {
    sessionId: "s1",
    event: "mincode.breakpointsSet",
    body: { path: "", functionBreakpoints: [{ id: 1, verified: false, line: 0 }] },
  });
  assert.deepEqual(after.functionBreakpoints, [
    { name: "app.Main#run", verified: false, id: 1 },
    { name: "app.Main#nope", verified: false },
  ]);
  assert.deepEqual(after.verified, {}, "no source breakpoint results");
  const failed = reduceDebugEvent(view, { sessionId: "s1", event: "mincode.breakpointsSet", body: { path: "", functionBreakpoints: null, error: "boom" } });
  assert.deepEqual(failed.functionBreakpoints, [
    { name: "app.Main#run", verified: false, message: "boom" },
    { name: "app.Main#nope", verified: false, message: "boom" },
  ]);
  // Data breakpoint results do not touch the method breakpoints.
  const data = reduceDebugEvent(after, { sessionId: "s1", event: "mincode.breakpointsSet", body: { path: "", dataBreakpoints: [] } });
  assert.deepEqual(data.functionBreakpoints, after.functionBreakpoints);
});

test("a breakpoint event verifies a method breakpoint by id once its class is loaded", () => {
  const view = withBreakpoints("/p/A.java", [{ id: 7, line: 3, verified: true }], sessionView({
    functionBreakpoints: [{ name: "app.Main#run", verified: false, id: 1 }, { name: "x", verified: false, id: 0 }],
  }));
  const verified = breakpointEvent(view, { reason: "changed", breakpoint: { id: 1, verified: true, line: 0 } });
  assert.deepEqual(verified.functionBreakpoints, [{ name: "app.Main#run", verified: true, id: 1 }, { name: "x", verified: false, id: 0 }]);
  assert.deepEqual(verified.verified, view.verified, "line breakpoints are unchanged");
  const message = breakpointEvent(verified, { reason: "changed", breakpoint: { id: 1, verified: false, message: "gone" } });
  assert.deepEqual(message.functionBreakpoints[0], { name: "app.Main#run", verified: false, id: 1, message: "gone" });
  const removed = breakpointEvent(verified, { reason: "removed", breakpoint: { id: 1 } });
  assert.deepEqual(removed.functionBreakpoints[0], { name: "app.Main#run", verified: false, id: 1 });
  // A line breakpoint's id still finds the line breakpoint.
  const line = breakpointEvent(view, { reason: "changed", breakpoint: { id: 7, verified: false, line: 3 } });
  assert.equal(line.verified["/p/A.java"][0].verified, false);
  assert.deepEqual(line.functionBreakpoints, view.functionBreakpoints);
});

// ---- field (data) breakpoints ----

import {
  DATA_BREAKPOINT_LIMITS,
  dataBreakpointResults,
  isValidDataId,
  offeredAccessTypes,
  parseDataBreakpointInfo,
  sameDataBreakpointLists,
  sessionDataBreakpointList,
  toggledDataBreakpoints,
} from "../src/domain/debug/data-breakpoints.ts";

test("normalizeProjectDebugStates reads field breakpoints per debugger type; old data has none", () => {
  const states = normalizeProjectDebugStates({
    "/p": {
      dataBreakpoints: {
        java: [
          { dataId: "Counter#count", description: "Counter.count : int", accessType: "write" },
          { dataId: "Counter#count", description: "again", accessType: "write" },
          { dataId: "Counter#count", description: "Counter.count : int", accessType: "read" },
          { dataId: "Counter#total", accessType: "readWrite" },
          { dataId: "Counter#old" },
          { dataId: " spaced ", description: "two\nlines‮", accessType: "write" },
          { dataId: "Counter#x", accessType: "modify" },
          { dataId: "" },
          { dataId: "   " },
          { dataId: "a\nb" },
          { dataId: "x".repeat(1025) },
          { dataId: 3 },
          "Counter#bare",
          null,
        ],
        empty: [],
        notList: { dataId: "a#b" },
      },
    },
    "/old": { configs: [], functionBreakpoints: { java: [] } },
  });
  assert.deepEqual(states["/p"].dataBreakpoints, {
    java: [
      { dataId: "Counter#count", description: "Counter.count : int", accessType: "write" },
      { dataId: "Counter#count", description: "Counter.count : int", accessType: "read" },
      { dataId: "Counter#total", description: "Counter#total", accessType: "readWrite" },
      { dataId: "Counter#old", description: "Counter#old", accessType: "write" },
      // The id is sent verbatim (not trimmed); the description is one line.
      { dataId: " spaced ", description: "two lines", accessType: "write" },
    ],
    empty: [],
  });
  assert.deepEqual(states["/old"].dataBreakpoints, {});
  assert.deepEqual(normalizeProjectDebugStates({ "/x": { dataBreakpoints: [{ dataId: "a#b" }] } })["/x"].dataBreakpoints, {});
});

test("normalizeProjectDebugStates keeps at most the host's number of field breakpoints", () => {
  const many = Array.from({ length: 260 }, (_, i) => ({ dataId: `C#f${i}`, description: `C.f${i}`, accessType: "write" }));
  const states = normalizeProjectDebugStates({ "/p": { dataBreakpoints: { java: many } } });
  assert.equal(states["/p"].dataBreakpoints.java.length, 256);
  assert.equal(states["/p"].dataBreakpoints.java.at(-1).dataId, "C#f255");
  assert.equal(DATA_BREAKPOINT_LIMITS.max, contributionContract.points.debuggers.limits.dataBreakpointsMax);
  assert.equal(DATA_BREAKPOINT_LIMITS.maxRunes, 1024);
});

test("isValidDataId refuses what the host refuses", () => {
  assert.equal(isValidDataId("Counter#count"), true);
  assert.equal(isValidDataId("é".repeat(1024)), true);
  for (const value of ["", "  ", "😀".repeat(1025), "a\nb", "a\tb", "a\u0000b", "a‮b", "a\ud800b", null, 4]) {
    assert.equal(isValidDataId(value), false, JSON.stringify(value));
  }
});

test("parseDataBreakpointInfo reads java-debug's answer; no body or a bad id means no breakpoint", () => {
  assert.deepEqual(
    parseDataBreakpointInfo({ dataId: "Counter#count", description: "Counter.count : int", accessTypes: ["read", "write", "readWrite"], canPersist: true }),
    { dataId: "Counter#count", description: "Counter.count : int", accessTypes: ["read", "write", "readWrite"], canPersist: true },
  );
  // java-debug answers a variable that is no field without a body.
  assert.deepEqual(parseDataBreakpointInfo(null), { dataId: null, description: "" });
  // Without an id the adapter's text is not shown anywhere.
  assert.deepEqual(parseDataBreakpointInfo({ dataId: null, description: "not a field\u202e" }), { dataId: null, description: "" });
  assert.deepEqual(parseDataBreakpointInfo({ dataId: "a\nb" }).dataId, null);
  assert.deepEqual(parseDataBreakpointInfo({ dataId: "x", accessTypes: ["write", "modify", 3], canPersist: "yes" }), {
    dataId: "x",
    description: "x",
    accessTypes: ["write"],
  });
});

test("offeredAccessTypes offers write by default and read only when the adapter lists it", () => {
  assert.deepEqual(offeredAccessTypes({ dataId: "a", description: "" }), ["write"]);
  assert.deepEqual(offeredAccessTypes({ dataId: "a", description: "", accessTypes: ["read", "write", "readWrite"] }), ["write", "read"]);
  assert.deepEqual(offeredAccessTypes({ dataId: "a", description: "", accessTypes: ["write"] }), ["write"]);
  assert.deepEqual(offeredAccessTypes({ dataId: "a", description: "", accessTypes: ["read"] }), ["read"]);
  assert.deepEqual(offeredAccessTypes({ dataId: null, description: "", accessTypes: ["read", "write"] }), []);
});

test("field breakpoints are told apart by dataId and access type", () => {
  const write = { dataId: "C#f", description: "C.f", accessType: "write" };
  const read = { dataId: "C#f", description: "C.f", accessType: "read" };
  const added = toggledDataBreakpoints([write], read);
  assert.deepEqual(added, [write, read]);
  assert.deepEqual(toggledDataBreakpoints(added, { ...write, description: "other" }), [read]);
  assert.deepEqual(sessionDataBreakpointList([write], [read, { ...write, description: "dup" }]), [write, read]);
  assert.deepEqual(sessionDataBreakpointList(undefined, undefined), []);
  assert.equal(sameDataBreakpointLists([write, read], [{ dataId: "C#f", accessType: "write" }, { dataId: "C#f", accessType: "read" }]), true);
  assert.equal(sameDataBreakpointLists([write, read], [read, write]), false);
  assert.deepEqual(dataBreakpointResults([write, read], [{ verified: true, id: 2 }]), [
    { ...write, verified: true, id: 2 },
    { ...read, verified: false },
  ]);
});

test("the start-time field breakpoint results land on the breakpoints the session started with", () => {
  const write = { dataId: "Counter#count", description: "Counter.count : int", accessType: "write" };
  const read = { dataId: "Nope#x", description: "Nope#x", accessType: "read" };
  const view = sessionView({
    functionBreakpoints: [{ name: "app.Main#run", verified: false }],
    dataBreakpoints: [{ ...write, verified: false }, { ...read, verified: false }],
  });
  // As java-debug 0.53.1 answers before any class is loaded.
  const after = reduceDebugEvent(view, {
    sessionId: "s1",
    event: "mincode.breakpointsSet",
    body: { path: "", dataBreakpoints: [{ id: 1, verified: false, line: 0 }, { id: 2, verified: false, line: 0 }] },
  });
  assert.deepEqual(after.dataBreakpoints, [
    { ...write, verified: false, id: 1 },
    { ...read, verified: false, id: 2 },
  ]);
  assert.deepEqual(after.functionBreakpoints, view.functionBreakpoints, "method breakpoints are unchanged");
  const failed = reduceDebugEvent(view, { sessionId: "s1", event: "mincode.breakpointsSet", body: { path: "", dataBreakpoints: null, error: "boom" } });
  assert.deepEqual(failed.dataBreakpoints, [
    { ...write, verified: false, message: "boom" },
    { ...read, verified: false, message: "boom" },
  ]);
  // Method breakpoint results do not touch the field breakpoints.
  const functions = reduceDebugEvent(after, { sessionId: "s1", event: "mincode.breakpointsSet", body: { path: "", functionBreakpoints: [{ id: 9, verified: true }] } });
  assert.deepEqual(functions.dataBreakpoints, after.dataBreakpoints);
  // A session that was sent none ignores the event.
  assert.equal(reduceDebugEvent(sessionView(), { sessionId: "s1", event: "mincode.breakpointsSet", body: { path: "", dataBreakpoints: [] } }).dataBreakpoints, undefined);
});

test("a breakpoint event verifies a field breakpoint by id once its class is loaded", () => {
  const write = { dataId: "Counter#count", description: "Counter.count : int", accessType: "write" };
  const view = withBreakpoints("/p/A.java", [{ id: 7, line: 3, verified: true }], sessionView({
    functionBreakpoints: [{ name: "app.Main#run", verified: false, id: 1 }],
    dataBreakpoints: [{ ...write, verified: false, id: 2 }],
  }));
  // java-debug 0.53.1 reports it with reason "new" when the class loads.
  const verified = breakpointEvent(view, { reason: "new", breakpoint: { id: 2, verified: true, line: 0 } });
  assert.deepEqual(verified.dataBreakpoints, [{ ...write, verified: true, id: 2 }]);
  assert.deepEqual(verified.functionBreakpoints, view.functionBreakpoints);
  assert.deepEqual(verified.verified, view.verified, "line breakpoints are unchanged");
  const removed = breakpointEvent(verified, { reason: "removed", breakpoint: { id: 2 } });
  assert.deepEqual(removed.dataBreakpoints, [{ ...write, verified: false, id: 2 }]);
  // Method and line breakpoint ids still find their own.
  const fn = breakpointEvent(view, { reason: "changed", breakpoint: { id: 1, verified: true } });
  assert.deepEqual(fn.dataBreakpoints, view.dataBreakpoints);
  assert.equal(fn.functionBreakpoints[0].verified, true);
  const line = breakpointEvent(view, { reason: "changed", breakpoint: { id: 7, verified: false, line: 3 } });
  assert.deepEqual(line.dataBreakpoints, view.dataBreakpoints);
});

test("a data breakpoint stop pauses like any other stop", () => {
  const paused = reduceDebugEvent(sessionView(), { sessionId: "s1", event: "stopped", body: { reason: "data breakpoint", threadId: 1 } });
  assert.equal(paused.state, "paused");
  assert.equal(paused.stoppedReason, "data breakpoint");
});

test("a stack is whole when fewer frames than requested came, or totalFrames says so", () => {
  assert.equal(stackTraceComplete(1, 50, undefined), true);
  assert.equal(stackTraceComplete(50, 50, undefined), false);
  assert.equal(stackTraceComplete(50, 50, 50), true);
  assert.equal(stackTraceComplete(50, 50, 120), false);
  assert.equal(stackTraceComplete(50, 50, "50"), false);
});

test("only the last frame of a whole stack is the bottom frame; a continue forgets it", () => {
  const frames = [{ id: 1, name: "inner", line: 1, column: 1 }, { id: 2, name: "main", line: 1, column: 1 }];
  const view = sessionView({ state: "paused", frames, framesComplete: true });
  assert.equal(isBottomFrame(view, 2), true);
  assert.equal(isBottomFrame(view, 1), false);
  assert.equal(isBottomFrame({ ...view, framesComplete: undefined }, 2), false);
  const running = reduceDebugEvent(view, { sessionId: "s1", event: "continued", body: { threadId: 1, allThreadsContinued: true } });
  assert.equal(running.framesComplete, undefined);
});

test("debugger addresses are never treated as local files to open", () => {
  assert.equal(isDebuggerAddressPath("jdt://contents/java.base/sun.security.provider/PolicyFile.class"), true);
  assert.equal(isDebuggerAddressPath("/Users/a/demo/jdt://contents/x/Y.class"), true);
  assert.equal(isDebuggerAddressPath("debug-source://s1/ref%3A7/%3Csun.security.provider"), true);
  assert.equal(isDebuggerAddressPath("C:/work/demo/src/App.java"), false);
  assert.equal(isDebuggerAddressPath("/Users/a/notes/jdt-tips.md"), false);
  assert.equal(isDebuggerAddressPath("/Users/a/x/jdt:/y.java"), false);
});

// ---- address breakpoints ----

test("isValidDebuggerAddress mirrors the host's address rule", () => {
  for (const ok of [
    "jdt://contents/java.base/java.util/ArrayList.class?=java.base/%5C/x",
    "ab://x",
    "foo+bar.baz://x",
    "x-lib://pkg/Type.class",
    `ab://${"x".repeat(DEBUGGER_ADDRESS_LIMITS.maxRunes - 5)}`,
    `ab://${"\u{1F600}".repeat(DEBUGGER_ADDRESS_LIMITS.maxRunes - 5)}`,
  ]) {
    assert.equal(isValidDebuggerAddress(ok), true, ok.slice(0, 40));
  }
  for (const bad of [
    "file:///p/A.java",
    "FILE:///p/A.java",
    "/abs/A.java",
    "src/A.java",
    "../x.java",
    "C://Users/x/secret.py",
    "c://x",
    "a://x",
    "1b://x",
    "ab:/x",
    "",
    `ab://${"x".repeat(DEBUGGER_ADDRESS_LIMITS.maxRunes - 4)}`,
    "ab://x\ny",
    "ab://x\ty",
    "ab://x\u202Ey",
    "ab://x\u200By",
    "ab://x\uFEFF",
    "ab://x\u0085",
    "ab://x\uD800",
    42,
    null,
  ]) {
    assert.equal(isValidDebuggerAddress(bad), false, String(bad).slice(0, 40));
  }
});

test("addressBreakpointName is the address's last segment without its extension", () => {
  assert.equal(addressBreakpointName("jdt://contents/java.base/java.util/ArrayList.class?=java.base/%3Cjava.util(ArrayList.class"), "ArrayList");
  assert.equal(addressBreakpointName("x-lib://pkg/Outer%24Inner.class#frag"), "Outer$Inner");
  assert.equal(addressBreakpointName("x-lib://pkg/Bad%E0.class"), "Bad%E0");
  assert.equal(addressBreakpointName("x-lib://"), "x-lib://");
  assert.equal(addressBreakpointName("x-lib://Makefile"), "Makefile");
});

test("virtualSourceDocumentInfo reads the session and the address back", () => {
  const address = "jdt://contents/java.base/java.util/ArrayList.class?=java.base/x";
  const path = virtualSourceDocumentPath("s 1", { name: "ArrayList.java", path: address, sourceReference: 4 }, "f");
  assert.deepEqual(virtualSourceDocumentInfo(path), { sessionId: "s 1", address });
  const byReference = virtualSourceDocumentPath("s1", { name: "<eval>", sourceReference: 9 }, "f");
  assert.deepEqual(virtualSourceDocumentInfo(byReference), { sessionId: "s1" }, "a source without an address has none");
  const relative = virtualSourceDocumentPath("s1", { name: "x.py", path: "lib/x.py" }, "f");
  assert.deepEqual(virtualSourceDocumentInfo(relative), { sessionId: "s1" }, "an address the host refuses is none");
  const fileUri = virtualSourceDocumentPath("s1", { name: "x.py", path: "file:///p/x.py" }, "f");
  assert.deepEqual(virtualSourceDocumentInfo(fileUri), { sessionId: "s1" });
  assert.equal(virtualSourceDocumentInfo("/p/src/A.java"), null);
  assert.equal(virtualSourceDocumentInfo("debug-source://%E0/x/y"), null);
});

test("normalizeProjectDebugStates keeps valid address breakpoints per debugger type, anything else gets none", () => {
  const address = "jdt://contents/java.base/java.util/ArrayList.class?=x";
  assert.deepEqual(normalizeProjectDebugStates({ "/p": { breakpoints: {} } })["/p"].addressBreakpoints, {});
  const states = normalizeProjectDebugStates({
    "/p": {
      addressBreakpoints: {
        java: {
          [address]: [{ line: 9, enabled: false, condition: "e == null" }, { line: 4 }, { line: 4, condition: "dup" }, { line: 0 }, "x"],
          "file:///p/A.java": [{ line: 1 }],
          "src/A.java": [{ line: 1 }],
          "ab://x‮": [{ line: 1 }],
          "ab://empty": [{ line: -1 }],
          "ab://list": "nope",
        },
        node: { "ab://only-bad": [{ line: 0 }] },
        "": { "ab://no-type": [{ line: 1 }] },
        // The flat shape (an address directly under addressBreakpoints) is not a type.
        [address]: [{ line: 3 }],
      },
    },
  });
  assert.deepEqual(states["/p"].addressBreakpoints, {
    java: {
      [address]: [
        { line: 4, enabled: true },
        { line: 9, enabled: false, condition: "e == null" },
      ],
    },
  });
  assert.deepEqual(normalizeProjectDebugStates(states), states, "normalizing again changes nothing");
  assert.deepEqual(normalizeProjectDebugStates({ "/p": { addressBreakpoints: [] } })["/p"].addressBreakpoints, {});
  assert.deepEqual(normalizeProjectDebugStates({ "/p": { addressBreakpoints: { [address]: [{ line: 1 }] } } })["/p"].addressBreakpoints, {});
  const many = Object.fromEntries(Array.from({ length: DEBUGGER_ADDRESS_LIMITS.maxAddresses + 3 }, (_, i) => [`ab://c${i}`, [{ line: 1 }]]));
  assert.equal(Object.keys(normalizeProjectDebugStates({ "/p": { addressBreakpoints: { java: many } } })["/p"].addressBreakpoints.java).length, DEBUGGER_ADDRESS_LIMITS.maxAddresses);
});

test("debuggeeNotices shows env keys but never their values", () => {
  const notices = debuggeeNotices({ request: "launch", arguments: {
    mainClass: "demo.App", env: { DB_PASSWORD: "hunter2", TOKEN: "s3cr3t" },
  }}, "Full arguments");
  const full = notices.at(-1);
  assert.ok(!notices.some((line) => line.includes("hunter2") || line.includes("s3cr3t")), notices.join("\n"));
  assert.ok(full.includes('"DB_PASSWORD":"•••"') && full.includes('"TOKEN":"•••"'), full);
  assert.ok(full.includes('"mainClass":"demo.App"'));
  // A non-object env is not a variable map: it is shown masked as a whole.
  const odd = debuggeeNotices({ request: "launch", arguments: { env: "A=1" } }, "Full arguments").at(-1);
  assert.ok(!odd.includes("A=1"), odd);
});

test("addressSourceSession prefers the asking session, else the newest live one of the type, never an ended one", () => {
  const view = (id, startedAt, state = "paused") => ({ id, startedAt, state });
  // The dialog's own live session.
  assert.equal(addressSourceSession([view("a", 1), view("b", 2)], "a")?.id, "a");
  // Its own ended: the newest live one instead.
  assert.equal(addressSourceSession([view("a", 1, "ended"), view("b", 2, "running"), view("c", 3, "ended")], "a")?.id, "b");
  // Not of the type (absent from the list): the newest live one.
  assert.equal(addressSourceSession([view("b", 2), view("c", 3)], "x")?.id, "c");
  // Only ended ones: none.
  assert.equal(addressSourceSession([view("a", 1, "ended"), view("b", 2, "ended")], "a"), undefined);
  assert.equal(addressSourceSession([], "a"), undefined);
});

test("a breakpoint event with id 0 (shared by unresolvable method breakpoints) changes no list item", () => {
  const before = sessionView({
    functionBreakpoints: [
      { name: "a.B#missing", verified: false, id: 0, message: "not found" },
      { name: "a.B#other", verified: false, id: 0, message: "not found" },
    ],
    dataBreakpoints: [{ dataId: "d", accessType: "write", verified: false, id: 0 }],
  });
  const after = reduceDebugEvent(before, { sessionId: "s1", event: "breakpoint", body: { reason: "changed", breakpoint: { id: 0, verified: true } } });
  assert.equal(after, before);
  // Another id still updates its item.
  const listed = sessionView({ functionBreakpoints: [{ name: "a.B#run", verified: false, id: 4 }] });
  const verified = reduceDebugEvent(listed, { sessionId: "s1", event: "breakpoint", body: { reason: "changed", breakpoint: { id: 4, verified: true } } });
  assert.deepEqual(verified.functionBreakpoints, [{ name: "a.B#run", verified: true, id: 4 }]);
});

test("start-time list results replace the old answers: a short reply leaves no old id or message", () => {
  const before = sessionView({
    functionBreakpoints: [
      { name: "a.B#run", verified: false, id: 7, message: "old reason" },
      { name: "a.B#stop", verified: true, id: 8 },
    ],
    dataBreakpoints: [{ dataId: "d", accessType: "write", verified: false, id: 9, message: "old" }],
  });
  const fn = reduceDebugEvent(before, { sessionId: "s1", event: "mincode.breakpointsSet", body: { functionBreakpoints: [{ verified: true }] } });
  assert.deepEqual(fn.functionBreakpoints, [
    { name: "a.B#run", verified: true },
    { name: "a.B#stop", verified: false },
  ]);
  const data = reduceDebugEvent(before, { sessionId: "s1", event: "mincode.breakpointsSet", body: { dataBreakpoints: [] } });
  assert.deepEqual(data.dataBreakpoints, [{ dataId: "d", accessType: "write", verified: false }]);
});

test("every stopped event counts a stop, so the same thread stopping twice is two stops", () => {
  const first = reduceDebugEvent(sessionView(), { sessionId: "s1", event: "stopped", body: { reason: "exception", threadId: 1 } });
  const second = reduceDebugEvent(first, { sessionId: "s1", event: "stopped", body: { reason: "exception", threadId: 1 } });
  assert.equal(first.stopSeq, 1);
  assert.equal(second.stopSeq, 2);
  const ran = reduceDebugEvent(second, { sessionId: "s1", event: "continued", body: { threadId: 1 } });
  assert.equal(ran.stopSeq, 2);
});

test("debuggeeNotices masks secret-looking values in vmArgs and args, shown and in the full arguments", () => {
  const notices = debuggeeNotices({ request: "launch", arguments: {
    mainClass: "demo.App",
    vmArgs: '-Xmx1g -Dspring.datasource.password=hunter2 "-Ddb.Token=a b" -Dapp.name=demo -DAPI_KEY=k1',
    args: ["--client-secret=s3cr3t", "--port=8080", "apiKey='k 2'", "--private-key=pk", "--Credential=cr", "--passwd=pw"],
  }}, "Full arguments");
  const all = notices.join("\n");
  for (const secret of ["hunter2", "a b", "k1", "s3cr3t", "k 2", "pk", "=cr", "=pw"]) assert.ok(!all.includes(secret), `${secret} in\n${all}`);
  const vmArgs = notices.find((line) => line.startsWith("vmArgs: "));
  assert.equal(vmArgs, 'vmArgs: -Xmx1g -Dspring.datasource.password=••• "-Ddb.Token=•••" -Dapp.name=demo -DAPI_KEY=•••');
  const args = notices.find((line) => line.startsWith("args: "));
  assert.equal(args, 'args: ["--client-secret=•••","--port=8080","apiKey=•••","--private-key=•••","--Credential=•••","--passwd=•••"]');
  assert.ok(notices.at(-1).includes("-Dapp.name=demo") && notices.at(-1).includes("--port=8080"));
  // Only names with those words; nothing else changes.
  const plain = debuggeeNotices({ request: "launch", arguments: { vmArgs: "-Dtokenizer.size=3 -Dkeystore=x", args: "a=b --mode=fast" } });
  assert.ok(plain.includes("args: a=b --mode=fast"));
  assert.ok(plain.some((line) => line === "vmArgs: -Dtokenizer.size=••• -Dkeystore=x"), plain.join("\n"));
});

test("debuggeeNotices masks secrets inside URL values: secret query parameters and userinfo passwords", () => {
  const launch = {
    mainClass: "demo.App",
    vmArgs: '-Dspring.datasource.url=jdbc:mysql://db:3306/app?user=a&password=hunter2&useSSL=true "-Dhook=https://h/p?token=y1 z"',
    args: [
      "--db-url=jdbc:postgresql://admin:pa55@db.local:5432/app",
      "--api=https://h/p?q=1&api_key=k3#top",
      "--mirror=ftp://anon:s3cr3t@ftp.example.org/pub",
      "--tls=https://h/x?client_secret=cs9;page=2",
    ],
  };
  const notices = debuggeeNotices({ request: "launch", arguments: launch }, "Full arguments");
  const all = notices.join("\n");
  for (const secret of ["hunter2", "y1", "pa55", "k3", "s3cr3t", "cs9"]) assert.ok(!all.includes(secret), `${secret} in\n${all}`);
  assert.equal(
    notices.find((line) => line.startsWith("vmArgs: ")),
    'vmArgs: -Dspring.datasource.url=jdbc:mysql://db:3306/app?user=a&password=•••&useSSL=true "-Dhook=https://h/p?token=••• z"',
  );
  assert.equal(
    notices.find((line) => line.startsWith("args: ")),
    'args: ["--db-url=jdbc:postgresql://admin:•••@db.local:5432/app","--api=https://h/p?q=1&api_key=•••#top","--mirror=ftp://anon:•••@ftp.example.org/pub","--tls=https://h/x?client_secret=•••;page=2"]',
  );
  // What is launched is unchanged.
  assert.equal(launch.args[0], "--db-url=jdbc:postgresql://admin:pa55@db.local:5432/app");
  // Ordinary URLs and lookalikes stay verbatim.
  const ordinary = [
    "--url=https://example.com:8443/search?q=token&page=2#password",
    "--repo=git@github.com:org/repo.git",
    "--proxy=http://user@proxy.local:3128/",
    "--home=http://localhost:8080/app?mode=fast&lang=en",
    "--path=/tmp/a?password",
    "user:pass@host",
  ];
  const plain = debuggeeNotices({ request: "launch", arguments: { args: ordinary } }, "Full arguments");
  assert.equal(plain.at(-1), `Full arguments: ${JSON.stringify({ args: ordinary })}`);
});

test("debuggeeNotices masks SQL Server JDBC ;password= and whole userinfo passwords with @ or /", () => {
  const launch = {
    mainClass: "demo.App",
    args: [
      "--db-url=jdbc:sqlserver://db:1433;databaseName=app;user=sa;password=Hun7er;encrypt=true",
      "--spring.datasource.url=jdbc:jtds:sqlserver://db/app;Token=t0k",
      "--pg=postgresql://admin:p@ss@db.local:5432/app",
      "--http=https://admin:ab/cd@host.local/x",
      "--mssql=jdbc:sqlserver://db:1433;user=sa;password=P@ss;encrypt=true",
    ],
  };
  const notices = debuggeeNotices({ request: "launch", arguments: launch }, "Full arguments");
  const all = notices.join("\n");
  for (const secret of ["Hun7er", "t0k", "p@ss", "ss@db", "ab/cd", "cd@host", "P@ss", "@ss"]) assert.ok(!all.includes(secret), `${secret} in\n${all}`);
  for (const shown of [
    "jdbc:sqlserver://db:1433;databaseName=app;user=sa;password=•••;encrypt=true",
    "jdbc:jtds:sqlserver://db/app;Token=•••",
    "postgresql://admin:•••@db.local:5432/app",
    "https://admin:•••@host.local/x",
    "jdbc:sqlserver://db:1433;user=sa;password=•••;encrypt=true",
  ]) {
    assert.ok(all.includes(shown), `${shown} not in\n${all}`);
  }
  // `;name=value` outside a jdbc: value is left alone.
  const plain = debuggeeNotices({ request: "launch", arguments: { args: ["--style=a;password-hint=shown"] } }, "Full arguments");
  assert.equal(plain.at(-1), `Full arguments: ${JSON.stringify({ args: ["--style=a;password-hint=shown"] })}`);
});
