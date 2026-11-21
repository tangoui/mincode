import assert from "node:assert/strict";
import test from "node:test";
import {
  exportKeybindings,
  importKeybindings,
  KEYBINDING_IMPORT_MAX_ENTRIES,
  mapWhenExpression,
  stripJsonc,
} from "../src/domain/keybindings/import-export.ts";

test("stripJsonc removes comments and trailing commas but keeps strings", () => {
  const text = `[
    // line comment
    { "key": "cmd+k", "command": "a//b", /* block */ "when": "x" },
  ]`;
  assert.deepEqual(JSON.parse(stripJsonc(text)), [{ key: "cmd+k", command: "a//b", when: "x" }]);
  assert.equal(stripJsonc('"a\\"/*b"'), '"a\\"/*b"');
});

test("VS Code ids and when keys map to MinCode", () => {
  const { rules, issues } = importKeybindings(
    `[
      { "key": "cmd+shift+p", "command": "workbench.action.showCommands" },
      { "key": "cmd+d", "command": "editor.action.addSelectionToNextFindMatch", "when": "editorTextFocus" },
      { "key": "cmd+k", "command": "-workbench.action.showCommands" },
      { "command": "-workbench.action.quickOpen" },
      { "key": "alt+down", "command": "mincode.diff.next", "when": "isInDiffEditor" },
      { "key": "cmd+j", "command": "builtin:ai", "when": "!isInDiffEditor && editor.kind == 'sql'" },
    ]`,
    "mac",
  );
  assert.deepEqual(issues, []);
  assert.deepEqual(rules, [
    { key: "cmd+shift+p", command: "builtin:command-palette" },
    { key: "cmd+d", command: "editor:editor.action.addSelectionToNextFindMatch", when: "editor.focus" },
    { key: "cmd+k", command: "-builtin:command-palette" },
    { command: "-builtin:quick-open" },
    { key: "alt+down", command: "editor:mincode.diff.next", when: 'editor.kind == "diff"' },
    { key: "cmd+j", command: "builtin:ai", when: 'editor.kind != "diff" && editor.kind == "sql"' },
  ]);
});

test("unsupported entries are reported, not guessed", () => {
  const { rules, issues } = importKeybindings(
    JSON.stringify([
      "nope",
      { key: "cmd+k" },
      { key: "cmd+k", command: "gitlens.showQuickRepoStatus" },
      { key: "cmd+", command: "builtin:settings" },
      { key: "cmd+j", command: "builtin:ai", when: "a || b" },
      { key: "cmd+j", command: "builtin:ai", when: "resourceExtname =~ /ts/" },
      { key: "cmd+j", command: "builtin:ai", when: "sideBarVisible" },
      { command: "builtin:ai" },
      { key: "cmd+j", command: "builtin:ai", when: 3 },
    ]),
    "mac",
  );
  assert.equal(rules.length, 0);
  assert.deepEqual(
    issues.map((issue) => [issue.index, issue.reason]),
    [[0, "shape"], [1, "shape"], [2, "command"], [3, "key"], [4, "when"], [5, "when"], [6, "when"], [7, "key"], [8, "shape"]],
  );
  assert.throws(() => importKeybindings("{}", "mac"), /array/);
  assert.throws(() => importKeybindings(JSON.stringify(Array(KEYBINDING_IMPORT_MAX_ENTRIES + 1).fill({})), "mac"), /more than/);
  assert.throws(() => importKeybindings("[", "mac"));
});

test("mapWhenExpression rejects VS Code-only operators", () => {
  assert.deepEqual(mapWhenExpression(" "), { ok: true, when: "" });
  assert.deepEqual(mapWhenExpression("terminalFocus && terminalTextSelected"), { ok: true, when: "terminal.focus && terminal.selection" });
  for (const when of ["(a)", "a in b", "a > 1", "a || b"]) assert.equal(mapWhenExpression(when).ok, false, when);
});

test("export round-trips through import", () => {
  const rules = [
    { key: "mod+p", command: "builtin:command-palette", when: "!editor.focus" },
    { key: "mod+k", command: "-builtin:command-palette" },
    { command: "-builtin:settings" },
  ];
  const text = exportKeybindings(rules);
  assert.ok(text.endsWith("\n"));
  assert.deepEqual(JSON.parse(text)[0], { key: "mod+p", command: "builtin:command-palette", when: "!editor.focus" });
  assert.deepEqual(importKeybindings(text, "win").rules, rules);
});
