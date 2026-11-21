import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { KEYBINDING_COMMANDS } from "../src/domain/keybindings/catalog.ts";
import { importKeybindings } from "../src/domain/keybindings/import-export.ts";
import { parseKeySequence, sequenceId } from "../src/domain/keybindings/keys.ts";
import { monacoDefaultRules } from "../src/domain/keybindings/monaco-defaults.ts";
import { normalizeKeybindingOverride, overridesToRules } from "../src/domain/keybindings/persistence.ts";
import { presetRulesForPlatform } from "../src/domain/keybindings/preset.ts";
import { HOST_KEYBINDING_RULES } from "../src/domain/keybindings/presets/index.ts";
import { MINCODE_PRESET } from "../src/domain/keybindings/presets/mincode.ts";
import { resolveKeybindings } from "../src/domain/keybindings/resolver.ts";

import {
  analyzeCandidate,
  applyImportedOverrides,
  buildImportPreview,
  buildKeybindingRows,
  catalogRowCommand,
  EMPTY_RECORDER_STATE,
  exportUserKeybindings,
  filterKeybindingRows,
  importedOverrides,
  overridesAfterChange,
  overridesAfterRemove,
  overridesAfterReset,
  recordChord,
  RECORDER_CHORD_TIMEOUT_MS,
} from "../src/domain/keybindings/settings-model.ts";

function resolve(platform, overrides = [], presets = [MINCODE_PRESET]) {
  return resolveKeybindings({
    platform,
    presets,
    defaults: monacoDefaultRules(platform),
    host: HOST_KEYBINDING_RULES,
    user: presetRulesForPlatform(overridesToRules(overrides), platform),
  });
}

function rowsFor(platform, overrides = [], presets = [MINCODE_PRESET], extra = []) {
  return buildKeybindingRows({
    commands: [...KEYBINDING_COMMANDS.map(catalogRowCommand), ...extra],
    resolution: resolve(platform, overrides, presets),
    overrides,
    presetId: presets[presets.length - 1].id,
    platform,
  });
}

const row = (rows, id) => rows.find((item) => item.id === id);
const chord = (text, platform = "mac") => parseKeySequence(text, platform).chords[0];

test("rows list every catalog command with labels, sources and availability", () => {
  const rows = rowsFor("mac", [], [MINCODE_PRESET]);
  assert.equal(rows.length, KEYBINDING_COMMANDS.length);
  const palette = row(rows, "builtin:command-palette");
  assert.equal(palette.bindings[0].label, "⌘K");
  assert.equal(palette.source, "default");
  assert.equal(palette.modified, false);
  assert.equal(row(rows, "builtin:search-everywhere").available, true);
  assert.equal(row(rows, "builtin:quick-open"), undefined);
  assert.equal(row(rows, "builtin:go-to-symbol"), undefined);


  const win = rowsFor("win");
  assert.equal(row(win, "builtin:command-palette").bindings[0].label, "Ctrl+K");
});

test("user overrides mark rows as user / removed and discarded plugin commands are ignored", () => {
  const overrides = [
    { command: "builtin:ai", key: "cmd+j", platform: "mac" },
    { command: "builtin:settings", remove: true, platform: "mac" },
    { command: "plugin:x:y", key: "cmd+shift+9" },
  ];
  const rows = rowsFor("mac", overrides);
  assert.equal(row(rows, "builtin:ai").source, "user");
  assert.equal(row(rows, "builtin:ai").bindings[0].label, "⌘J");
  assert.equal(row(rows, "builtin:ai").modified, true);
  assert.equal(row(rows, "builtin:settings").source, "removed");
  assert.equal(row(rows, "builtin:settings").bindings.length, 0);
  assert.equal(row(rows, "plugin:x:y"), undefined);
});

test("search matches title, id, key label and key text; filters by modified / conflicts / scope / key", () => {
  const overrides = [{ command: "builtin:ai", key: "cmd+k", platform: "mac" }];
  const rows = rowsFor("mac", overrides);
  const titleOf = (item) => (item.titleKey ?? item.title ?? item.id).toString();
  const ids = (query) => filterKeybindingRows(rows, { filter: "all", text: "", ...query }, titleOf, "mac").map((item) => item.id);

  assert.ok(ids({ text: "builtin:settings" }).includes("builtin:settings"));
  assert.ok(ids({ text: "⌘K" }).includes("builtin:command-palette"));
  assert.ok(ids({ text: "cmd+k" }).includes("builtin:command-palette"));
  assert.deepEqual(ids({ filter: "modified" }), ["builtin:ai"]);
  const conflicts = ids({ filter: "conflicts" });
  assert.ok(conflicts.includes("builtin:ai") && conflicts.includes("builtin:command-palette"));
  assert.ok(ids({ filter: "scope:terminal" }).every((id) => id.startsWith("terminal:")));
  // Key search: exact sequence or chord prefix (⌘K matches ⌘K ⌘S style chords too).
  const byKey = ids({ keySequence: sequenceId(parseKeySequence("cmd+k", "mac")) });
  assert.ok(byKey.includes("builtin:command-palette") && byKey.includes("builtin:ai"));
  assert.ok(!byKey.includes("builtin:settings"));
});

test("recorder builds a two-chord sequence only within the timeout", () => {
  let state = recordChord(EMPTY_RECORDER_STATE, chord("cmd+k"), 1000);
  state = recordChord(state, chord("cmd+s"), 1000 + RECORDER_CHORD_TIMEOUT_MS);
  assert.equal(state.chords.length, 2);
  // A third key restarts; a late second key restarts too.
  assert.equal(recordChord(state, chord("cmd+j"), 3000).chords.length, 1);
  const late = recordChord(recordChord(EMPTY_RECORDER_STATE, chord("cmd+k"), 0), chord("cmd+s"), RECORDER_CHORD_TIMEOUT_MS + 1);
  assert.equal(late.chords.length, 1);
  assert.equal(late.chords[0].code, "KeyS");
});

test("candidate analysis lists overlapping conflicts, reserved keys and the global activation shortcut", () => {
  const resolution = resolve("mac");
  const palette = analyzeCandidate({ resolution, platform: "mac", command: "builtin:ai", key: "meta+k" });
  assert.ok(palette.conflicts.some((binding) => binding.command === "builtin:command-palette"));
  // A disjoint when does not conflict: terminal-only vs editor-only.
  const disjoint = analyzeCandidate({ resolution, platform: "mac", command: "terminal:clear", key: "meta+/" });
  assert.ok(!disjoint.conflicts.some((binding) => binding.command === "editor:editor.action.commentLine"));
  const same = analyzeCandidate({ resolution, platform: "mac", command: "builtin:ai", key: "meta+/" });
  assert.ok(same.conflicts.some((binding) => binding.command === "editor:editor.action.commentLine"));
  // The command's own bindings are not conflicts.
  assert.equal(analyzeCandidate({ resolution, platform: "mac", command: "builtin:command-palette", key: "meta+k" }).conflicts.length, 0);

  assert.equal(analyzeCandidate({ resolution, platform: "mac", command: "builtin:ai", key: "meta+q" }).reserved?.severity, "reserved");
  assert.equal(analyzeCandidate({ resolution: resolve("win"), platform: "win", command: "builtin:ai", key: "f5" }).reserved?.severity, "webview");
  const activation = { resolution, platform: "mac", command: "builtin:ai", key: "shift+meta+space" };
  assert.equal(analyzeCandidate(activation).globalShortcut, false);
  assert.equal(analyzeCandidate({ ...activation, globalShortcutEnabled: true }).globalShortcut, true);
  assert.equal(analyzeCandidate({ resolution, platform: "mac", command: "builtin:ai", key: "meta+j", when: "a ||" }).error, "when");
  assert.equal(analyzeCandidate({ resolution, platform: "mac", command: "builtin:ai", key: "nope+j" }).error, "key");
});

test("row actions produce valid overrides: change, add, remove, reset", () => {
  const platform = "mac";
  const rows = rowsFor(platform);
  const palette = row(rows, "builtin:command-palette").bindings[0].binding;

  // Changing a preset binding removes it for this platform and adds the new key.
  let overrides = overridesAfterChange([], { command: "builtin:command-palette", target: palette, key: "meta+j", platform });
  assert.ok(overrides.every((override) => normalizeKeybindingOverride(override)));
  let next = row(rowsFor(platform, overrides), "builtin:command-palette");
  assert.deepEqual(next.bindings.map((binding) => binding.label), ["⌘J"]);
  assert.equal(next.source, "user");

  // Changing the user binding again replaces the override in place.
  overrides = overridesAfterChange(overrides, { command: "builtin:command-palette", target: next.bindings[0].binding, key: "meta+l", platform });
  assert.equal(overrides.length, 2);
  next = row(rowsFor(platform, overrides), "builtin:command-palette");
  assert.deepEqual(next.bindings.map((binding) => binding.label), ["⌘L"]);

  // Adding an alternate keeps the existing binding.
  overrides = overridesAfterChange(overrides, { command: "builtin:command-palette", key: "meta+k meta+p", platform });
  next = row(rowsFor(platform, overrides), "builtin:command-palette");
  assert.deepEqual(next.bindings.map((binding) => binding.label), ["⌘K ⌘P", "⌘L"]);

  // Removing every binding drops user additions and removes preset ones.
  const removed = overridesAfterRemove(overrides, "builtin:command-palette", platform);
  next = row(rowsFor(platform, removed), "builtin:command-palette");
  assert.equal(next.bindings.length, 0);
  assert.equal(next.source, "removed");

  // Removing one user binding deletes its override.
  const single = overridesAfterRemove(overrides, "builtin:command-palette", platform, next.bindings[0]?.binding ?? row(rowsFor(platform, overrides), "builtin:command-palette").bindings[0].binding);
  assert.equal(single.length, overrides.length - 1);

  assert.deepEqual(overridesAfterReset([...removed, { command: "builtin:ai", key: "meta+j" }], "builtin:command-palette"), [{ command: "builtin:ai", key: "meta+j" }]);
  // Overrides are platform-scoped: Windows still has the MinCode default.
  assert.equal(row(rowsFor("win", removed), "builtin:command-palette").bindings[0].label, "Ctrl+K");
});

test("import preview maps entries, reports unsupported ones, and merges or replaces", () => {
  const result = importKeybindings(
    `[
      { "key": "cmd+shift+p", "command": "workbench.action.showCommands" },
      { "key": "cmd+k", "command": "-workbench.action.showCommands" },
      { "key": "cmd+j", "command": "some.unknown.command" },
      { "key": "cmd+l", "command": "builtin:ai", "when": "a || b" },
    ]`,
    "mac",
  );
  const preview = buildImportPreview(result, "mac");
  assert.deepEqual(
    preview.mapped.map((entry) => [entry.command, entry.removal, entry.label]),
    [["builtin:command-palette", false, "⇧⌘P"], ["builtin:command-palette", true, "⌘K"]],
  );
  assert.deepEqual(preview.unsupported.map((issue) => issue.reason), ["command", "when"]);
  const imported = importedOverrides(preview, "mac");
  assert.ok(imported.every((override) => normalizeKeybindingOverride(override)));
  assert.equal(imported[1].remove, true);

  const existing = [{ command: "builtin:ai", key: "meta+j", platform: "mac" }];
  assert.equal(applyImportedOverrides(existing, imported, "merge").length, 3);
  assert.equal(applyImportedOverrides([...existing, imported[0]], imported, "merge").length, 3);
  assert.deepEqual(applyImportedOverrides(existing, imported, "replace"), imported);

  const rows = rowsFor("mac", applyImportedOverrides([], imported, "replace"));
  assert.deepEqual(row(rows, "builtin:command-palette").bindings.map((binding) => binding.label), ["⇧⌘P"]);
});

test("export writes this platform's overrides as keybindings.json that imports back", () => {
  const overrides = [
    { command: "builtin:ai", key: "meta+j", platform: "mac" },
    { command: "builtin:ai", key: "ctrl+j", platform: "win" },
    { command: "builtin:command-palette", key: "meta+k", remove: true, platform: "mac" },
  ];
  const text = exportUserKeybindings(overrides, "mac");
  assert.deepEqual(JSON.parse(text), [
    { key: "meta+j", command: "builtin:ai" },
    { key: "meta+k", command: "-builtin:command-palette" },
  ]);
  const roundTrip = buildImportPreview(importKeybindings(text, "mac"), "mac");
  assert.equal(roundTrip.unsupported.length, 0);
  assert.equal(roundTrip.mapped.length, 2);
});

test("settings view lazy-loads the keybinding panel and no longer has the static shortcut table", async () => {
  const view = await readFile(new URL("../src/views/SettingsWorkspaceView.vue", import.meta.url), "utf8");
  assert.match(
    view,
    /const KeybindingSettingsPanel = defineAsyncComponent\(\(\) => import\("@\/components\/settings\/KeybindingSettingsPanel\.vue"\)\)/,
  );
  assert.doesNotMatch(view, /import KeybindingSettingsPanel/);
  assert.match(view, /<KeybindingSettingsPanel\s+v-else-if="active === 'shortcuts'"/);
  assert.doesNotMatch(view, /shortcutGroups|ShortcutItem|settings-shortcut-group|COMMAND_PALETTE_SHORTCUTS|boundKey\(/);

  const panel = await readFile(new URL("../src/components/settings/KeybindingSettingsPanel.vue", import.meta.url), "utf8");
  assert.match(panel, /AppSearchField/);
  assert.match(panel, /AppFilterButton/);
  assert.match(panel, /saveTextFileToDisk\(/);
  assert.match(panel, /ElMessageBox\.confirm/);
  assert.match(panel, /defineAsyncComponent\(\(\) => import\("@\/components\/settings\/KeybindingRecorderDialog\.vue"\)\)/);
  const recorder = await readFile(new URL("../src/components/settings/KeybindingRecorderDialog.vue", import.meta.url), "utf8");
  assert.match(recorder, /store\.setRecording\(true\)/);
  assert.match(recorder, /store\.setRecording\(false\)/);
  assert.match(recorder, /@keydown\.capture="onCaptureKeydown"/);
});

test("every settings.keybindings key used by the panel exists in zh and en", async () => {
  const sources = await Promise.all(
    ["components/settings/KeybindingSettingsPanel.vue", "components/settings/KeybindingRecorderDialog.vue"].map((path) =>
      readFile(new URL(`../src/${path}`, import.meta.url), "utf8"),
    ),
  );
  const used = new Set(sources.flatMap((source) => [...source.matchAll(/"(settings\.keybindings\.[A-Za-z.-]+)"|'(settings\.keybindings\.[A-Za-z.-]+)'/g)].map((match) => match[1] ?? match[2])));
  const dynamic = [
    ...["global", "editor", "diff", "terminal", "ai", "project", "database", "plugin"].map((scope) => `settings.keybindings.scope.${scope}`),
    ...["default", "preset", "user", "removed", "none"].map((source) => `settings.keybindings.source.${source}`),
    ...["shape", "command", "key", "when"].map((reason) => `settings.keybindings.importReason.${reason}`),
  ];
  const reasons = await readFile(new URL("../src/domain/keybindings/reserved.ts", import.meta.url), "utf8");
  for (const match of reasons.matchAll(/":\s*"([a-z-]+)"/g)) dynamic.push(`settings.keybindings.reason.${match[1]}`);
  for (const locale of ["zh", "en"]) {
    const messages = await readFile(new URL(`../src/i18n/locales/${locale}.ts`, import.meta.url), "utf8");
    for (const key of [...used, ...dynamic]) assert.ok(messages.includes(`"${key}":`), `${locale}: ${key}`);
  }
});
