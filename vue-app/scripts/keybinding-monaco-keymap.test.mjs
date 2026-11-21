import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { parseKeySequence } from "../src/domain/keybindings/keys.ts";
import { monacoDefaultRules } from "../src/domain/keybindings/monaco-defaults.ts";
import {
  buildMonacoKeymapRules,
  MONACO_APP_COMMAND,
  MONACO_EDITOR_RUN_COMMAND,
  MONACO_EDITOR_SAVE_COMMAND,
} from "../src/domain/keybindings/monaco-keymap-rules.ts";
import { monacoKeybinding } from "../src/domain/keybindings/monaco-rules.ts";
import { HOST_KEYBINDING_RULES } from "../src/domain/keybindings/presets/index.ts";
import { lookupKeybinding, resolveKeybindings } from "../src/domain/keybindings/resolver.ts";
import { ALL_KEYMAP_PRESETS, keymapPresetChainOf } from "./keymap-presets-fixture.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const PLATFORMS = ["mac", "win", "linux"];

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

const key = (text, platform) => monacoKeybinding(parseKeySequence(text, platform), platform, api);

/** The rule set services/monaco-keymap.ts applies for a preset (same resolver input as the store). */
function build(presetId, platform, extra = {}) {
  const resolution = resolveKeybindings({
    platform,
    presets: keymapPresetChainOf(presetId),
    defaults: monacoDefaultRules(platform),
    host: HOST_KEYBINDING_RULES,
    user: extra.user,
  });
  return buildMonacoKeymapRules({ platform, resolution, api, editorModeOwns: extra.editorModeOwns });
}

/** The rule Monaco would pick for a key: the last addition on it whose when does not exclude `context`. */
function winner(result, keybinding, context = {}) {
  const additions = result.rules.filter((rule) => !rule.command.startsWith("-") && rule.keybinding === keybinding);
  const matches = additions.filter((rule) => {
    const when = rule.when ?? "";
    if (context.kind && /mincodeEditorKind == '/.test(when) && !when.includes(`mincodeEditorKind == '${context.kind}'`)) return false;
    if (context.kind && when.includes(`mincodeEditorKind != '${context.kind}'`)) return false;
    if (!context.diff && /(^|[^!])isInDiffEditor/.test(when)) return false;
    if (context.diff && when.includes("!isInDiffEditor")) return false;
    return true;
  });
  return matches[matches.length - 1];
}

test("removal rules precede additions on every preset and platform", () => {
  for (const { id } of ALL_KEYMAP_PRESETS) {
    for (const platform of PLATFORMS) {
      const { rules, skipped } = build(id, platform);
      const firstAddition = rules.findIndex((rule) => !rule.command.startsWith("-"));
      assert.ok(firstAddition >= 0, `${id}/${platform}`);
      assert.ok(rules.slice(firstAddition).every((rule) => !rule.command.startsWith("-")), `${id}/${platform}`);
      assert.deepEqual(skipped, [], `${id}/${platform} skipped`);
      for (const rule of rules) assert.ok(Number.isInteger(rule.keybinding) && rule.keybinding > 0, `${id}/${platform} ${rule.command}`);
    }
  }
});

test("MinCode: ⌘K and other app chords are mirrored into Monaco", () => {
  for (const platform of PLATFORMS) {
    const result = build("mincode", platform);
    const palette = winner(result, key("mod+k", platform), { kind: "project" });
    assert.equal(palette.command, MONACO_APP_COMMAND, platform);
    assert.deepEqual(palette.commandArgs, { id: "builtin:command-palette" }, platform);
    assert.equal(palette.when, "editorFocus", platform);
    assert.equal(winner(result, key("mod+i", platform)).commandArgs.id, "builtin:ai", platform);
    // Lock is `!editor.focus`, so inside Monaco ⇧⌘L stays Select All Occurrences.
    const selectAll = winner(result, key("mod+shift+l", platform));
    assert.ok(!selectAll || !selectAll.command.startsWith("mincode.app"), platform);
    assert.ok(!result.mirroredCommands.includes("builtin:lock-app"), platform);
    assert.ok(result.mirroredCommands.includes("builtin:command-palette"));
  }
});

test("text-editing and terminal-only chords are never mirrored", () => {
  for (const { id } of ALL_KEYMAP_PRESETS) {
    for (const platform of PLATFORMS) {
      const result = build(id, platform);
      for (const text of ["mod+c", "mod+v", "mod+x", "mod+a", "mod+z", "mod+shift+z", "escape"]) {
        const rule = winner(result, key(text, platform));
        assert.ok(!rule || !rule.command.startsWith("mincode.app"), `${id}/${platform} ${text} → ${rule?.commandArgs?.id}`);
      }
      assert.ok(!result.mirroredCommands.some((command) => command.startsWith("terminal:")), `${id}/${platform}`);
    }
  }
});

test("SQL editors: mod+enter runs the query in every preset; mod+s only in MinCode", () => {
  for (const platform of PLATFORMS) {
    for (const { id } of ALL_KEYMAP_PRESETS) {
      const result = build(id, platform);
      const run = winner(result, key("mod+enter", platform), { kind: "sql" });
      assert.equal(run.command, MONACO_EDITOR_RUN_COMMAND, `${id}/${platform}`);
      assert.equal(run.commandArgs.id, "database:run-query");
      const save = winner(result, key("mod+s", platform), { kind: "sql" });
      if (keymapPresetChainOf(id).some((preset) => preset.id === "mincode")) assert.equal(save.command, MONACO_EDITOR_RUN_COMMAND, `${id}/${platform}`);
      else assert.equal(save, undefined, `${id}/${platform}: mod+s is not bound in SQL editors`);
      // Every other editor saves on mod+s (JetBrains: save all → focused save).
      assert.equal(winner(result, key("mod+s", platform), { kind: "project" }).command, MONACO_EDITOR_SAVE_COMMAND, `${id}/${platform}`);
    }
  }
});

test("diff navigation keys follow the preset and require a diff editor", () => {
  const expected = {
    mincode: ["alt+down", "alt+up"],
  };
  for (const [id, [next, previous]] of Object.entries(expected)) {
    for (const platform of PLATFORMS) {
      const result = build(id, platform);
      const nextRule = winner(result, key(next, platform), { diff: true });
      const previousRule = winner(result, key(previous, platform), { diff: true });
      assert.equal(nextRule.command, "mincode.diff.next", `${id}/${platform}`);
      assert.equal(previousRule.command, "mincode.diff.previous", `${id}/${platform}`);
      assert.equal(nextRule.when, "editorTextFocus && isInDiffEditor");
    }
  }
  // Outside diff editors MinCode ⌥↑ / ⌥↓ keep moving lines.
  const mincode = build("mincode", "mac");
  assert.equal(winner(mincode, key("alt+up", "mac")), undefined);
  assert.ok(!mincode.rules.some((rule) => rule.command === "-editor.action.moveLinesUpAction"));
});

test("user overrides win and runtime-only clauses travel in commandArgs", () => {
  const result = build("mincode", "mac", {
    user: [
      { key: "cmd+j", command: "builtin:ai", when: "editor.focus && !dialog.focus" },
      { key: "cmd+shift+d", command: "editor:editor.action.duplicateSelection" },
      { key: "cmd+g", command: "builtin:settings", when: "tree.selection" },
    ],
  });
  const ai = winner(result, key("cmd+j", "mac"));
  assert.equal(ai.command, MONACO_APP_COMMAND);
  assert.deepEqual(ai.commandArgs, { id: "builtin:ai", when: "!dialog.focus" });
  assert.equal(winner(result, key("cmd+shift+d", "mac")).command, "editor.action.duplicateSelection");
  // tree.selection is never true while Monaco has focus.
  assert.ok(!result.rules.some((rule) => rule.commandArgs?.id === "builtin:settings" && rule.keybinding === key("cmd+g", "mac")));
});

test("editor-mode owned sequences are left to the provider", () => {
  const result = build("mincode", "mac", {
    editorModeOwns: (sequence) => sequence.kind === "chords" && sequence.chords[0].code === "KeyK",
  });
  assert.ok(!result.rules.some((rule) => rule.keybinding === key("cmd+k", "mac")));
  assert.ok(result.skipped.some((skip) => skip.binding.command === "builtin:command-palette" && skip.reason === "editor-mode"));
});

test("every editor command in the rule set exists in a Monaco module MinCode imports", () => {
  const runtimePath = join(root, "src/services/monaco-runtime.js");
  const runtime = readFileSync(runtimePath, "utf8");
  const registers = [...runtime.matchAll(/import\s+"([^"]+\/register\.js)"/g)].map((match) => resolve(dirname(runtimePath), match[1]));
  const monacoRoot = join(root, "node_modules/monaco-editor/esm/vs/editor");
  const featureRoots = [join(monacoRoot, "contrib"), join(monacoRoot, "standalone")];
  const reachable = new Set();
  const queue = [...registers];
  while (queue.length) {
    const file = queue.pop();
    if (!existsSync(file)) continue;
    for (const match of readFileSync(file, "utf8").matchAll(/(?:import|export)\s+(?:[^'"]*?from\s+)?['"](\.[^'"]+)['"]/g)) {
      const target = resolve(dirname(file), match[1]);
      if (reachable.has(target) || !featureRoots.some((prefix) => target.startsWith(prefix))) continue;
      reachable.add(target);
      queue.push(target);
    }
  }
  const files = [...reachable, join(monacoRoot, "browser/coreCommands.js"), join(monacoRoot, "browser/editorExtensions.js")];
  const sources = files.filter((file) => existsSync(file)).map((file) => readFileSync(file, "utf8")).join("\n");
  const ids = new Set();
  for (const { id } of ALL_KEYMAP_PRESETS) {
    for (const platform of PLATFORMS) {
      for (const rule of build(id, platform).rules) {
        const command = rule.command.replace(/^-/, "");
        if (!command.startsWith("mincode.")) ids.add(command);
        if (rule.commandArgs?.fallback) ids.add(rule.commandArgs.fallback);
      }
    }
  }
  assert.ok(ids.size > 0);
  for (const id of ids) {
    const quoted = new RegExp(`['"]${id.replace(/\./g, "\\.")}['"]`);
    assert.ok(quoted.test(sources), `${id} is not defined in a Monaco module imported by monaco-runtime.js`);
  }
});

const read = (path) => readFileSync(join(root, path), "utf8");

test("every Monaco host installs the keymap on mount and releases its registration", () => {
  for (const path of [
    "src/components/ui/MonacoTextEditor.vue",
    "src/components/ui/MonacoDiffEditor.vue",
    "src/components/ui/ReadonlyCodeViewer.vue",
  ]) {
    const source = read(path);
    assert.match(source, /onMounted\([\s\S]*?ensureMonacoKeymap\(\)/, path);
    assert.match(source, /from "@\/services\/monaco-keymap"/, path);
    assert.match(source, /onBeforeUnmount\([\s\S]*?(keymapRegistration\?\.dispose\(\)|disposeDiffNavigation\?\.\(\))/, path);
  }
  // No other component creates Monaco editors outside these hosts.
  const text = read("src/components/ui/MonacoTextEditor.vue");
  assert.doesNotMatch(text, /keybindings:\s*\[monaco\.KeyMod\.CtrlCmd \| monaco\.KeyCode\.KeyS\]/);
  assert.match(text, /id: "app\.save-file"/);
  assert.match(text, /registerMonacoEditor\(editor, \{[\s\S]*?kind: props\.kind \?\? "text"/);
  const query = read("src/components/database/query/DatabaseQueryWorkspace.vue");
  assert.match(query, /<MonacoTextEditor[\s\S]*?kind="sql"[\s\S]*?@run="emit\('run', 'smart'\)"/);
  assert.doesNotMatch(query, /@save="emit\('run'/);
});

test("the keymap service applies one global rule set and disposes the previous one", () => {
  const service = read("src/services/monaco-keymap.ts");
  assert.match(service, /appliedRules\?\.dispose\(\);\s*appliedRules = monaco\.editor\.addKeybindingRules\(/);
  assert.match(service, /effectScope\(true\)/);
  for (const command of ["MONACO_APP_COMMAND", "MONACO_EDITOR_SAVE_COMMAND", "MONACO_EDITOR_RUN_COMMAND", '"mincode.diff.next"', '"mincode.diff.previous"']) {
    assert.match(service, new RegExp(`registerCommand\\(${command.replace(/\./g, "\\.")}`), command);
  }
  assert.doesNotMatch(service, /addEventListener\("keydown"/);
});

test("MinCode: debugger F-keys are mirrored with a runtime debug.active check and give the key back to Monaco", () => {
  for (const platform of PLATFORMS) {
    const result = build("mincode", platform);
    const cases = [
      ["f8", "project:debug.stepOver", "editor.action.marker.nextInFiles"],
      ["shift+f8", "project:debug.stepOut", "editor.action.marker.prevInFiles"],
      ["alt+f8", "project:debug.evaluate", "editor.action.marker.next"],
      ["f7", "project:debug.stepInto", undefined],
      ["shift+f7", "project:debug.smartStepInto", undefined],
      ["alt+f9", "project:debug.runToCursor", undefined],
    ];
    for (const [text, id, fallback] of cases) {
      const rule = winner(result, key(text, platform), { kind: "project" });
      assert.equal(rule.command, MONACO_APP_COMMAND, `${platform} ${text}`);
      assert.deepEqual(rule.commandArgs, { id, when: "debug.active", ...(fallback ? { fallback } : {}) }, `${platform} ${text}`);
    }
    const toggle = winner(result, key("mod+f8", platform), { kind: "project" });
    assert.deepEqual(toggle.commandArgs, { id: "project:debug.toggleBreakpoint" }, platform);
  }
});
