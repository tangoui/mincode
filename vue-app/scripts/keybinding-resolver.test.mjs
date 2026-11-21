import assert from "node:assert/strict";
import test from "node:test";
import { parseWhenExpression } from "../src/domain/commands/when.ts";
import { parseKeySequence } from "../src/domain/keybindings/keys.ts";
import { monacoDefaultRules } from "../src/domain/keybindings/monaco-defaults.ts";
import { MINCODE_PRESET, HOST_KEYBINDING_RULES } from "../src/domain/keybindings/presets/mincode.ts";
import { ALL_KEYMAP_PRESETS, keymapPresetChainOf } from "./keymap-plugin-presets.mjs";
import { presetRulesForPlatform } from "../src/domain/keybindings/preset.ts";
import {
  bindingsForCommand,
  findKeybindingConflicts,
  lookupKeybinding,
  primaryBinding,
  pruneReplacedDefaults,
  resolveKeybindings,
  whenClausesDisjoint,
} from "../src/domain/keybindings/resolver.ts";

const preset = (rules, id = "p") => ({ id, title: { zh: id, en: id }, rules });
const chords = (text, platform = "mac") => parseKeySequence(text, platform).chords;
const all = () => true;

test("later layers win and user rules beat presets", () => {
  const resolution = resolveKeybindings({
    platform: "mac",
    presets: [preset([{ key: "mod+k", command: "builtin:command-palette" }])],
    user: [{ key: "mod+k", command: "builtin:settings" }],
  });
  const hit = lookupKeybinding(resolution, chords("cmd+k"), all);
  assert.equal(hit.kind, "execute");
  assert.equal(hit.binding.command, "builtin:settings");
  assert.equal(hit.binding.layer, "user");
  assert.equal(primaryBinding(resolution, "builtin:command-palette").sequenceKey, "meta+k");
});

test("removals with and without key and when", () => {
  const base = [
    { key: "mod+k", command: "builtin:command-palette" },
    { key: "f1", command: "builtin:command-palette" },
    { key: "mod+s", command: "editor:save", when: "editor.focus" },
    { key: "mod+s", command: "editor:save", when: "input.focus" },
  ];
  let resolution = resolveKeybindings({ platform: "win", presets: [preset(base)], user: [{ key: "ctrl+k", command: "-builtin:command-palette" }] });
  assert.deepEqual(bindingsForCommand(resolution, "builtin:command-palette").map((b) => b.sequenceKey), ["f1"]);
  resolution = resolveKeybindings({ platform: "win", presets: [preset(base)], user: [{ command: "-builtin:command-palette" }] });
  assert.equal(bindingsForCommand(resolution, "builtin:command-palette").length, 0);
  resolution = resolveKeybindings({
    platform: "win",
    presets: [preset(base)],
    user: [{ key: "mod+s", command: "-editor:save", when: "input.focus" }],
  });
  assert.deepEqual(bindingsForCommand(resolution, "editor:save").map((b) => b.when), ["editor.focus"]);
});

test("preset chain: extends root first, child overrides", () => {
  const root = preset([{ key: "mod+k", command: "builtin:command-palette" }], "root");
  const child = { ...preset([{ key: "mod+k", command: "-builtin:command-palette" }, { key: "mod+p", command: "builtin:command-palette" }], "child"), extends: "root" };
  const resolution = resolveKeybindings({ platform: "linux", presets: [root, child] });
  assert.deepEqual(bindingsForCommand(resolution, "builtin:command-palette").map((b) => [b.sequenceKey, b.presetId]), [["ctrl+p", "child"]]);
});

test("invalid rules are reported, not applied", () => {
  const resolution = resolveKeybindings({
    platform: "mac",
    presets: [],
    user: [
      { key: "mod+k", command: "unknown:thing" },
      { key: "mod+", command: "builtin:settings" },
      { key: "mod+j", command: "builtin:settings", when: "a || b" },
      { command: "builtin:settings" },
    ],
  });
  assert.equal(resolution.bindings.length, 0);
  assert.deepEqual(resolution.issues.map((issue) => issue.index), [0, 1, 2, 3]);
});

test("monaco and terminal commands get implicit focus clauses", () => {
  const resolution = resolveKeybindings({
    platform: "mac",
    presets: [preset([{ key: "cmd+c", command: "terminal:copy", when: "terminal.selection" }])],
    defaults: monacoDefaultRules("mac"),
  });
  const rename = primaryBinding(resolution, "editor:editor.action.rename");
  assert.equal(rename.layer, "default");
  assert.deepEqual(rename.clauses.map((clause) => clause.key), ["editor.focus"]);
  const copy = primaryBinding(resolution, "terminal:copy");
  assert.deepEqual(copy.clauses.map((clause) => clause.key), ["terminal.focus", "terminal.selection"]);
});

test("chord lookup: exact executes, prefix pends, highest order wins", () => {
  const resolution = resolveKeybindings({
    platform: "mac",
    presets: [preset([
      { key: "mod+k mod+s", command: "builtin:keybindings" },
      { key: "cmd+k", command: "terminal:clear" },
    ])],
  });
  const terminal = (binding) => binding.clauses.every((clause) => clause.key !== "terminal.focus");
  // Outside the terminal, the chord is the only eligible candidate.
  assert.equal(lookupKeybinding(resolution, chords("cmd+k"), terminal).kind, "pending");
  assert.equal(lookupKeybinding(resolution, chords("cmd+k cmd+s"), terminal).kind, "execute");
  assert.equal(lookupKeybinding(resolution, chords("cmd+k cmd+x"), terminal).kind, "none");
  // In the terminal the later single-chord rule wins.
  const hit = lookupKeybinding(resolution, chords("cmd+k"), all);
  assert.equal(hit.kind, "execute");
  assert.equal(hit.binding.command, "terminal:clear");
});

test("when disjointness", () => {
  const w = (text) => parseWhenExpression(text);
  assert.equal(whenClausesDisjoint(w("editor.focus"), w("!editor.focus")), true);
  assert.equal(whenClausesDisjoint(w('editor.kind == "sql"'), w('editor.kind == "diff"')), true);
  assert.equal(whenClausesDisjoint(w('editor.kind == "sql"'), w('editor.kind != "sql"')), true);
  assert.equal(whenClausesDisjoint(w('!editor.kind == "sql"'), w('editor.kind == "sql"')), true);
  assert.equal(whenClausesDisjoint(w("editor.focus"), w("terminal.focus")), true);
  assert.equal(whenClausesDisjoint(w('editor.kind == "sql"'), w("!editor.kind")), true);
  // The project tree provider is false inside inputs, Monaco and xterm.
  assert.equal(whenClausesDisjoint(w("editor.focus"), w("tree.selection")), true);
  assert.equal(whenClausesDisjoint(w("terminal.focus"), w("tree.selection")), true);
  assert.equal(whenClausesDisjoint(w("input.focus"), w("terminal.focus")), true);
  assert.equal(whenClausesDisjoint(w("ai.panel.focus"), w("editor.focus")), false);
  assert.equal(whenClausesDisjoint(w('editor.kind != "sql"'), w('editor.kind != "diff"')), false);
  assert.equal(whenClausesDisjoint([], w("editor.focus")), false);
});

test("conflicts: same sequence and chord prefix with overlapping whens", () => {
  const resolution = resolveKeybindings({
    platform: "mac",
    presets: [preset([
      { key: "mod+k", command: "builtin:command-palette" },
      { key: "mod+k mod+s", command: "builtin:keybindings" },
      { key: "mod+s", command: "editor:save", when: 'editor.focus && editor.kind != "sql"' },
      { key: "mod+s", command: "database:run-query", when: 'editor.focus && editor.kind == "sql"' },
      { key: "mod+,", command: "builtin:settings" },
      { key: "mod+,", command: "builtin:ai" },
    ])],
  });
  const conflicts = findKeybindingConflicts(resolution.bindings).map((c) => [c.kind, c.first.command, c.second.command]);
  assert.deepEqual(conflicts, [
    ["prefix", "builtin:command-palette", "builtin:keybindings"],
    ["same", "builtin:settings", "builtin:ai"],
  ]);
});

test("reserved and webview keys are flagged on resolved bindings", () => {
  const resolution = resolveKeybindings({
    platform: "win",
    presets: [],
    user: [{ key: "f5", command: "builtin:settings" }, { key: "alt+f4", command: "builtin:ai" }],
  });
  assert.deepEqual(resolution.bindings.map((b) => b.reserved?.severity), ["webview", "reserved"]);
});

test("MinCode preset with host rules keeps panel keys scoped", () => {
  const resolution = resolveKeybindings({ platform: "mac", presets: [MINCODE_PRESET], host: HOST_KEYBINDING_RULES });
  const context = (values) => (binding) => binding.clauses.every((clause) => {
    const value = values[clause.key];
    const truthy = clause.op === "" ? Boolean(value) : (clause.op === "==") === (value === clause.value);
    return clause.negate ? !truthy : truthy;
  });
  const copy = lookupKeybinding(resolution, chords("cmd+c"), context({ "tree.selection": true }));
  assert.equal(copy.binding.command, "project:copy-node");
  assert.equal(lookupKeybinding(resolution, chords("cmd+c"), context({ "tree.selection": true, "text.selection": true })).kind, "none");
  const palette = lookupKeybinding(resolution, chords("cmd+k"), context({}));
  assert.equal(palette.binding.command, "builtin:command-palette");
});

test("narrower overrides that win are not conflicts; shadowed ones are", () => {
  const conflicts = (rules, host = []) =>
    findKeybindingConflicts(resolveKeybindings({ platform: "mac", presets: [preset(rules)], host }).bindings).map((c) => [c.first.command, c.second.command]);
  // Panel-scoped host rule after a preset's global binding: intended override.
  assert.deepEqual(conflicts([{ key: "mod+shift+f", command: "builtin:find-in-files" }], [{ key: "mod+shift+f", command: "ai:search-conversations", when: "ai.panel.focus" }]), []);
  // The narrow binding is earlier, so the global one always wins: real conflict.
  assert.deepEqual(conflicts([
    { key: "mod+shift+f", command: "ai:search-conversations", when: "ai.panel.focus" },
    { key: "mod+shift+f", command: "builtin:find-in-files" },
  ]), [["ai:search-conversations", "builtin:find-in-files"]]);
  // Terminal commands scoped to terminal focus run before the window dispatcher.
  assert.deepEqual(conflicts([
    { key: "mod+k mod+s", command: "builtin:keybindings" },
    { key: "mod+k", command: "terminal:clear", when: "terminal.focus" },
  ]), []);
  // Two terminal commands on the same key still conflict.
  assert.deepEqual(conflicts([
    { key: "mod+k", command: "terminal:clear", when: "terminal.focus" },
    { key: "mod+k", command: "terminal:find", when: "terminal.focus" },
  ]), [["terminal:clear", "terminal:find"]]);
  // A user binding onto the palette key is reported.
  const user = findKeybindingConflicts(resolveKeybindings({
    platform: "mac",
    presets: [MINCODE_PRESET],
    host: HOST_KEYBINDING_RULES,
    user: [{ key: "mod+k", command: "builtin:ai" }],
  }).bindings).map((c) => [c.first.command, c.second.command]);
  assert.deepEqual(user, [["builtin:command-palette", "builtin:ai"]]);
});

test("pruneReplacedDefaults drops defaults a preset rebinds or claims for the whole editor", () => {
  const defaults = [
    { key: "f2", command: "editor:editor.action.rename" },
    { key: "mod+d", command: "editor:editor.action.addSelectionToNextFindMatch" },
    { key: "alt+up", command: "editor:editor.action.moveLinesUpAction" },
  ];
  const kept = pruneReplacedDefaults(defaults, [
    { key: "shift+f6", command: "editor:editor.action.rename" },
    { key: "mod+d", command: "editor:editor.action.duplicateSelection", when: "editor.focus" },
    { key: "alt+up", command: "editor:mincode.diff.previous", when: 'editor.kind == "diff"' },
  ], "mac");
  assert.deepEqual(kept.map((rule) => rule.command), ["editor:editor.action.moveLinesUpAction"]);
});

test("built-in and official plugin presets have no conflicts on any platform", () => {
  for (const platform of ["mac", "win", "linux"]) {
    for (const leaf of ALL_KEYMAP_PRESETS) {
      const chain = keymapPresetChainOf(leaf.id);
      const overriding = [...chain.flatMap((p) => presetRulesForPlatform(p.rules, platform)), ...presetRulesForPlatform(HOST_KEYBINDING_RULES, platform)];
      const resolution = resolveKeybindings({
        platform,
        presets: chain,
        defaults: pruneReplacedDefaults(monacoDefaultRules(platform), overriding, platform),
        host: HOST_KEYBINDING_RULES,
      });
      const found = findKeybindingConflicts(resolution.bindings).map((c) => `${c.first.command} <> ${c.second.command}`);
      assert.deepEqual(found, [], `${leaf.id} on ${platform}`);
    }
  }
});

// ---------------------------------------------------------------------------
// Debugger keys
// ---------------------------------------------------------------------------

/** The resolution the keybindings store builds for a preset (pruned Monaco defaults + host rules). */
function storeResolution(presetId, platform) {
  const chain = keymapPresetChainOf(presetId);
  const overriding = [...chain.flatMap((p) => presetRulesForPlatform(p.rules, platform)), ...presetRulesForPlatform(HOST_KEYBINDING_RULES, platform)];
  return resolveKeybindings({
    platform,
    presets: chain,
    defaults: pruneReplacedDefaults(monacoDefaultRules(platform), overriding, platform),
    host: HOST_KEYBINDING_RULES,
  });
}

/** Eligibility under a when-context (the dispatcher's clause check). */
const inContext = (values) => (binding) => binding.clauses.every((clause) => {
  const value = values[clause.key];
  const truthy = clause.op === "" ? Boolean(value) : (clause.op === "==") === ((value ?? "") === clause.value);
  return clause.negate ? !truthy : truthy;
});

const lookupIn = (resolution, key, platform, values) => {
  const hit = lookupKeybinding(resolution, chords(key, platform), inContext(values));
  return hit.kind === "execute" ? hit.binding.command : null;
};

test("F8 resolves to project:debug.stepOver when debug.active", () => {
  for (const platform of ["mac", "win", "linux"]) {
    const resolution = storeResolution("mincode", platform);
    const session = { "debug.active": true };
    // The host rule wins over Monaco's F8 default even with the editor focused.
    assert.equal(lookupIn(resolution, "f8", platform, { ...session, "editor.focus": true }), "project:debug.stepOver", platform);
    assert.equal(lookupIn(resolution, "f8", platform, session), "project:debug.stepOver", platform);
    assert.equal(lookupIn(resolution, "f7", platform, session), "project:debug.stepInto", platform);
    assert.equal(lookupIn(resolution, "shift+f7", platform, session), "project:debug.smartStepInto", platform);
    assert.equal(lookupIn(resolution, "shift+f8", platform, { ...session, "editor.focus": true }), "project:debug.stepOut", platform);
    assert.equal(lookupIn(resolution, "alt+f9", platform, session), "project:debug.runToCursor", platform);
    assert.equal(lookupIn(resolution, "alt+f8", platform, session), "project:debug.evaluate", platform);
    assert.equal(lookupIn(resolution, platform === "mac" ? "cmd+alt+r" : "f9", platform, session), "project:debug.resume", platform);
  }
});

test("F8 falls back to marker.nextInFiles without a session", () => {
  for (const platform of ["mac", "win", "linux"]) {
    const resolution = storeResolution("mincode", platform);
    assert.equal(lookupIn(resolution, "f8", platform, { "editor.focus": true }), "editor:editor.action.marker.nextInFiles", platform);
    assert.equal(lookupIn(resolution, "shift+f8", platform, { "editor.focus": true }), "editor:editor.action.marker.prevInFiles", platform);
    for (const key of ["f8", "f7", "shift+f7", "shift+f8", "alt+f9", "alt+f8", platform === "mac" ? "cmd+alt+r" : "f9"]) {
      assert.equal(lookupIn(resolution, key, platform, {}), null, `${platform} ${key}`);
    }
  }
});

test("mod+f8 toggles breakpoint without a session", () => {
  for (const platform of ["mac", "win", "linux"]) {
    const resolution = storeResolution("mincode", platform);
    assert.equal(lookupIn(resolution, "mod+f8", platform, {}), "project:debug.toggleBreakpoint", platform);
    assert.equal(lookupIn(resolution, "mod+f8", platform, { "editor.focus": true }), "project:debug.toggleBreakpoint", platform);
    // Run / Debug / Stop stay out of the MinCode default preset.
    for (const command of ["project:debug.run", "project:debug.debug", "project:debug.stop"]) {
      assert.deepEqual(bindingsForCommand(resolution, command), [], `${platform} ${command}`);
    }
  }
});

test("intellij keymap binds run/debug/stop", () => {
  const expected = {
    mac: { "ctrl+r": "project:debug.run", "ctrl+d": "project:debug.debug", "cmd+f2": "project:debug.stop" },
    win: { "shift+f10": "project:debug.run", "shift+f9": "project:debug.debug", "ctrl+f2": "project:debug.stop" },
    linux: { "shift+f10": "project:debug.run", "shift+f9": "project:debug.debug", "ctrl+f2": "project:debug.stop" },
  };
  for (const [platform, keys] of Object.entries(expected)) {
    const resolution = storeResolution("keymaps.intellij", platform);
    for (const [key, command] of Object.entries(keys)) assert.equal(lookupIn(resolution, key, platform, {}), command, `${platform} ${key}`);
    const session = { "debug.active": true };
    assert.equal(lookupIn(resolution, "f8", platform, session), "project:debug.stepOver", platform);
    assert.equal(lookupIn(resolution, "f7", platform, session), "project:debug.stepInto", platform);
    assert.equal(lookupIn(resolution, "shift+f7", platform, session), "project:debug.smartStepInto", platform);
    assert.equal(lookupIn(resolution, "shift+f8", platform, session), "project:debug.stepOut", platform);
    assert.equal(lookupIn(resolution, "alt+f9", platform, session), "project:debug.runToCursor", platform);
    assert.equal(lookupIn(resolution, "alt+f8", platform, session), "project:debug.evaluate", platform);
    assert.equal(lookupIn(resolution, platform === "mac" ? "cmd+alt+r" : "f9", platform, session), "project:debug.resume", platform);
    assert.equal(lookupIn(resolution, "mod+f8", platform, {}), "project:debug.toggleBreakpoint", platform);
    // F7 in a diff editor stays "next difference" during a session.
    assert.equal(lookupIn(resolution, "f7", platform, { ...session, "editor.focus": true, "editor.kind": "diff" }), "editor:mincode.diff.next", platform);
    assert.equal(lookupIn(resolution, "shift+f7", platform, { ...session, "editor.focus": true, "editor.kind": "diff" }), "editor:mincode.diff.previous", platform);
  }
});

test("a state-scoped binding over a Monaco default is layering, a focus-only one is a conflict", () => {
  const conflicts = (rule) =>
    findKeybindingConflicts(resolveKeybindings({
      platform: "mac",
      presets: [preset([rule])],
      defaults: [{ key: "f8", command: "editor:editor.action.marker.nextInFiles" }],
    }).bindings).map((c) => [c.first.command, c.second.command]);
  assert.deepEqual(conflicts({ key: "f8", command: "project:debug.stepOver", when: "debug.active" }), []);
  assert.deepEqual(conflicts({ key: "f8", command: "project:debug.stepOver", when: "!input.focus" }), [["editor:editor.action.marker.nextInFiles", "project:debug.stepOver"]]);
  assert.deepEqual(conflicts({ key: "f8", command: "project:debug.stepOver" }), [["editor:editor.action.marker.nextInFiles", "project:debug.stepOver"]]);
  // A chord default has no Monaco fallback: a state-scoped prefix key would eat it, so it stays a conflict.
  const prefix = findKeybindingConflicts(resolveKeybindings({
    platform: "mac",
    presets: [preset([{ key: "mod+k", command: "project:debug.stepOver", when: "debug.active" }])],
    defaults: [{ key: "mod+k mod+c", command: "editor:editor.action.commentLine" }],
  }).bindings).map((c) => [c.kind, c.first.command, c.second.command]);
  assert.deepEqual(prefix, [["prefix", "editor:editor.action.commentLine", "project:debug.stepOver"]]);
});
