import assert from "node:assert/strict";
import test from "node:test";
import { parseKeySequence } from "../src/domain/keybindings/keys.ts";
import { monacoDefaultRules } from "../src/domain/keybindings/monaco-defaults.ts";
import { buildMonacoKeybindingRules, monacoKeybinding, translateWhenClauseForMonaco, translateWhenForMonaco } from "../src/domain/keybindings/monaco-rules.ts";
import { MINCODE_PRESET } from "../src/domain/keybindings/presets/mincode.ts";
import { resolveKeybindings } from "../src/domain/keybindings/resolver.ts";
import { parseWhenExpression } from "../src/domain/commands/when.ts";


// Stub of Monaco's KeyMod / KeyCode enums (values match monaco-editor).
const KeyCode = {
  Backspace: 1, Tab: 2, Enter: 3, Escape: 9, Space: 10,
  LeftArrow: 15, UpArrow: 16, RightArrow: 17, DownArrow: 18, Delete: 20,
  ...Object.fromEntries(Array.from({ length: 10 }, (_, i) => [`Digit${i}`, 21 + i])),
  ...Object.fromEntries(Array.from({ length: 26 }, (_, i) => [`Key${String.fromCharCode(65 + i)}`, 31 + i])),
  ...Object.fromEntries(Array.from({ length: 12 }, (_, i) => [`F${i + 1}`, 59 + i])),
  Comma: 87, Slash: 90, BracketLeft: 92, BracketRight: 94, Period: 89, Minus: 88, Equal: 86, Semicolon: 85, Quote: 95, Backquote: 91, Backslash: 93,
  Home: 14, End: 13, PageUp: 11, PageDown: 12,
};
const api = {
  KeyMod: { CtrlCmd: 2048, Shift: 1024, Alt: 512, WinCtrl: 256 },
  KeyCode,
  chord: (a, b) => (a | (b << 16)) >>> 0,
};
const seq = (text, platform) => parseKeySequence(text, platform);

test("chords convert per platform (CtrlCmd is ⌘ on mac, Ctrl elsewhere)", () => {
  assert.equal(monacoKeybinding(seq("cmd+k", "mac"), "mac", api), 2048 | KeyCode.KeyK);
  assert.equal(monacoKeybinding(seq("ctrl+k", "mac"), "mac", api), 256 | KeyCode.KeyK);
  assert.equal(monacoKeybinding(seq("ctrl+k", "win"), "win", api), 2048 | KeyCode.KeyK);
  assert.equal(monacoKeybinding(seq("mod+shift+alt+up", "linux"), "linux", api), 2048 | 1024 | 512 | KeyCode.UpArrow);
  assert.equal(monacoKeybinding(seq("mod+k mod+s", "mac"), "mac", api), api.chord(2048 | KeyCode.KeyK, 2048 | KeyCode.KeyS));
  assert.equal(monacoKeybinding(seq("shift shift", "mac"), "mac", api), null);
});

test("when translation", () => {
  const w = (text) => translateWhenForMonaco(parseWhenExpression(text));
  assert.deepEqual(translateWhenForMonaco([]), { ok: true, when: "editorTextFocus" });
  assert.deepEqual(w('editor.focus && editor.kind == "diff"'), { ok: true, when: "editorTextFocus && isInDiffEditor" });
  assert.deepEqual(w('editor.kind != "diff"'), { ok: true, when: "editorTextFocus && !isInDiffEditor" });
  assert.deepEqual(w('editor.kind == "sql"'), { ok: true, when: "editorTextFocus && mincodeEditorKind == 'sql'" });
  assert.equal(w("tree.selection").ok, false);
  assert.equal(w("!editor.focus").ok, false);
});

test("MinCode preset only adds the diff navigation rules", () => {
  for (const platform of ["mac", "win", "linux"]) {
    const resolution = resolveKeybindings({ platform, presets: [MINCODE_PRESET], defaults: monacoDefaultRules(platform) });
    const result = buildMonacoKeybindingRules({ platform, resolution, api });
    assert.deepEqual(result.changedCommands.sort(), ["mincode.diff.next", "mincode.diff.previous"], platform);
    assert.deepEqual(result.skipped, [], platform);
    assert.deepEqual(
      result.rules.map((rule) => [rule.command, rule.when]),
      [
        ["mincode.diff.next", "editorTextFocus && isInDiffEditor"],
        ["mincode.diff.previous", "editorTextFocus && isInDiffEditor"],
      ],
      platform,
    );
  }
});

test("untranslatable whens and editor-mode owned keys are skipped and reported", () => {
  const preset = {
    id: "t",
    title: { zh: "t", en: "t" },
    rules: [
      { key: "mod+shift+r", command: "editor:editor.action.rename", when: "tree.selection" },
      { key: "mod+alt+j", command: "editor:editor.action.formatDocument" },
    ],
  };
  const resolution = resolveKeybindings({ platform: "linux", presets: [preset], defaults: monacoDefaultRules("linux") });
  const result = buildMonacoKeybindingRules({
    platform: "linux",
    resolution,
    api,
    editorModeOwns: (sequence) => sequence.kind === "chords" && sequence.chords[0].code === "KeyJ",
  });
  const reasons = result.skipped.map((skip) => [skip.binding.command, skip.reason]).sort((a, b) => b[0].localeCompare(a[0]));
  assert.deepEqual(reasons, [
    ["editor:editor.action.rename", "when"],
    ["editor:editor.action.formatDocument", "editor-mode"],
  ]);
  // The rename default (F2) survives and is re-added with Monaco's own when.
  assert.ok(result.rules.some((rule) => rule.command === "editor.action.rename" && rule.keybinding === KeyCode.F2));
});

test("single clause translation used by mirrored app bindings", () => {
  const one = (text) => translateWhenClauseForMonaco(parseWhenExpression(text)[0]);
  assert.equal(one("editor.focus"), "editorTextFocus");
  assert.equal(one('editor.kind == "diff"'), "isInDiffEditor");
  assert.equal(one('editor.kind != "sql"'), "mincodeEditorKind != 'sql'");
  assert.equal(one("dialog.focus"), null);
  assert.equal(one("!editor.focus"), null);
});
