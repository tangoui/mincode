import assert from "node:assert/strict";
import test from "node:test";
import { buildKeybindingContext } from "../src/domain/keybindings/context.ts";
import { formatKeySequence } from "../src/domain/keybindings/keys.ts";
import { KeybindingMatcher } from "../src/domain/keybindings/matcher.ts";
import { monacoDefaultRules } from "../src/domain/keybindings/monaco-defaults.ts";
import {
  MAX_KEYBINDING_OVERRIDES,
  defaultPersistedKeybindings,
  migratePaletteShortcut,
  normalizeKeybindingOverride,
  normalizePersistedKeybindings,
  overridesToRules,
  paletteShortcutFromOverrides,
  presetPluginId,
  withPaletteShortcut,
} from "../src/domain/keybindings/persistence.ts";
import { presetRulesForPlatform } from "../src/domain/keybindings/preset.ts";
import { HOST_KEYBINDING_RULES, MINCODE_PRESET } from "../src/domain/keybindings/presets/mincode.ts";
import { primaryBinding, resolveKeybindings } from "../src/domain/keybindings/resolver.ts";

function resolve(overrides, platform = "mac") {
  return resolveKeybindings({
    platform,
    presets: [MINCODE_PRESET],
    defaults: monacoDefaultRules(platform),
    host: HOST_KEYBINDING_RULES,
    user: presetRulesForPlatform(overridesToRules(overrides), platform),
  });
}

function press(resolution, event, { platform = "mac", focus = "other", context = {} } = {}) {
  const matcher = new KeybindingMatcher({
    now: () => 1000,
    platform,
    resolution: () => resolution,
    doubleModifier: () => false,
    monacoMode: () => "input",
  });
  const full = { ...buildKeybindingContext({ target: null, platform, focus }), ...context };
  const decision = matcher.handleKeydown(
    { key: event.code.replace(/^Key/, "").toLowerCase(), ctrlKey: false, shiftKey: false, altKey: false, metaKey: false, ...event },
    { focus, context: full, canExecute: () => true },
  );
  return decision.kind === "execute" ? decision.binding.command : decision.kind;
}

test("normalizes overrides: invalid command, key, when and platform are rejected", () => {
  assert.deepEqual(normalizeKeybindingOverride({ command: "builtin:settings", key: " mod+, " }), { command: "builtin:settings", key: "mod+," });
  assert.equal(normalizeKeybindingOverride({ command: "nope:x", key: "mod+," }), null);
  assert.equal(normalizeKeybindingOverride({ command: "builtin:settings", key: "mod+nokey" }), null);
  assert.equal(normalizeKeybindingOverride({ command: "builtin:settings" }), null);
  assert.deepEqual(normalizeKeybindingOverride({ command: "builtin:settings", remove: true }), { command: "builtin:settings", remove: true });
  assert.equal(normalizeKeybindingOverride({ command: "builtin:settings", key: "mod+,", when: "a ||" }), null);
  assert.equal(normalizeKeybindingOverride({ command: "builtin:settings", key: "mod+,", platform: "beos" }), null);
  assert.deepEqual(
    normalizeKeybindingOverride({ command: "builtin:settings", key: "cmd+,", platform: ["mac", "mac"] }),
    { command: "builtin:settings", key: "cmd+,", platform: ["mac"] },
  );
});

test("normalizes the stored document and caps overrides", () => {
  assert.deepEqual(normalizePersistedKeybindings(null), { state: defaultPersistedKeybindings(), dropped: 0 });
  assert.deepEqual(normalizePersistedKeybindings({ version: 2, preset: "vscode" }).state.preset, "mincode");
  const many = Array.from({ length: MAX_KEYBINDING_OVERRIDES + 3 }, () => ({ command: "builtin:settings", key: "mod+," }));
  const { state, dropped } = normalizePersistedKeybindings({
    version: 1,
    preset: " keymaps.eclipse ",
    overrides: [{ command: "bogus" }, ...many],
    options: { doubleModifier: true, sendKeybindingsToShell: "yes" },
    migrations: { paletteShortcut: true },
  });
  assert.equal(state.preset, "keymaps.eclipse");
  assert.equal(state.overrides.length, MAX_KEYBINDING_OVERRIDES);
  assert.equal(dropped, 4);
  assert.deepEqual(state.options, { doubleModifier: true, sendKeybindingsToShell: false });
  assert.deepEqual(state.migrations, { paletteShortcut: true });
});

test("preset ids are stored as chosen, without legacy rewrites", () => {
  for (const id of ["mincode", "keymaps.vim", "vscode", "keymap-vim.vim", "acme.custom"]) {
    assert.equal(normalizePersistedKeybindings({ version: 1, preset: ` ${id} ` }).state.preset, id);
  }
  assert.equal(presetPluginId("keymaps.intellij"), "keymaps");
  assert.equal(presetPluginId("mincode"), undefined);
});

test("palette shortcut migration is idempotent and keeps other overrides", () => {
  const base = defaultPersistedKeybindings();
  base.overrides = [{ command: "builtin:settings", key: "mod+;" }];
  const first = migratePaletteShortcut(base, "mod+p");
  assert.equal(first.changed, true);
  assert.equal(first.state.migrations.paletteShortcut, true);
  assert.deepEqual(first.state.overrides, [
    { command: "builtin:settings", key: "mod+;" },
    { command: "builtin:command-palette", key: "mod+k", remove: true },
    { command: "builtin:command-palette", key: "mod+p", when: "!editor.focus" },
  ]);
  const second = migratePaletteShortcut(first.state, "mod+shift+p");
  assert.equal(second.changed, false);
  assert.deepEqual(second.state, first.state);
  const untouched = migratePaletteShortcut(defaultPersistedKeybindings(), "mod+k");
  assert.deepEqual(untouched.state.overrides, []);
  assert.equal(untouched.state.migrations.paletteShortcut, true);
  assert.equal(migratePaletteShortcut(defaultPersistedKeybindings(), "garbage").state.overrides.length, 0);
});

test("setShortcut shim replaces only the palette overrides and round-trips", () => {
  const others = [{ command: "builtin:settings", key: "mod+;" }];
  const p = withPaletteShortcut(others, "mod+p");
  assert.equal(paletteShortcutFromOverrides(p), "mod+p");
  const sp = withPaletteShortcut(p, "mod+shift+p");
  assert.equal(paletteShortcutFromOverrides(sp), "mod+shift+p");
  assert.equal(sp.filter((item) => item.command === "builtin:command-palette").length, 2);
  const k = withPaletteShortcut(sp, "mod+k");
  assert.deepEqual(k, others);
  assert.equal(paletteShortcutFromOverrides(k), "mod+k");
});

test("legacy palette choices keep their behaviour through the resolver and matcher", () => {
  const cmdK = { code: "KeyK", metaKey: true };
  const cmdP = { code: "KeyP", metaKey: true };
  const cmdShiftP = { code: "KeyP", metaKey: true, shiftKey: true };

  const defaults = resolve([]);
  assert.equal(press(defaults, cmdK), "builtin:command-palette");
  assert.equal(press(defaults, cmdK, { focus: "input" }), "builtin:command-palette");
  assert.equal(formatKeySequence(primaryBinding(defaults, "builtin:command-palette").sequence, "mac"), "⌘K");
  assert.equal(press(resolve([], "linux"), { code: "KeyK", ctrlKey: true }, { platform: "linux" }), "builtin:command-palette");

  const modP = resolve(paletteShortcutOverridesFor("mod+p"));
  assert.notEqual(press(modP, cmdK), "builtin:command-palette");
  assert.equal(press(modP, cmdP), "builtin:command-palette");
  // Old shouldDeferCommandPaletteShortcut: ⌘P stays with the editor inside Monaco.
  assert.notEqual(press(modP, cmdP, { focus: "monaco", context: { "editor.focus": true } }), "builtin:command-palette");

  const modShiftP = resolve(paletteShortcutOverridesFor("mod+shift+p"));
  assert.equal(press(modShiftP, cmdShiftP), "builtin:command-palette");
  assert.notEqual(press(modShiftP, cmdP), "builtin:command-palette");
  assert.notEqual(press(modShiftP, cmdK), "builtin:command-palette");
});

test("host rules keep AI panel keys", () => {
  const resolution = resolve([]);
  const ai = { "ai.panel.focus": true };
  assert.equal(press(resolution, { code: "Escape", key: "Escape" }, { focus: "input", context: { ...ai, "ai.running": true } }), "ai:stop");
  assert.equal(press(resolution, { code: "Escape", key: "Escape" }, { focus: "input", context: ai }), "ignore");
  assert.equal(press(resolution, { code: "KeyO", metaKey: true, shiftKey: true }, { focus: "input", context: ai }), "ai:new-conversation");
  assert.equal(press(resolution, { code: "KeyF", metaKey: true, shiftKey: true }, { context: ai }), "ignore");
  assert.equal(
    press(resolution, { code: "KeyF", metaKey: true, shiftKey: true }, { context: { ...ai, "ai.conversations.global": true } }),
    "ai:search-conversations",
  );
  assert.equal(press(resolution, { code: "ArrowUp", altKey: true }, { focus: "input", context: ai }), "ai:previous-conversation");
  assert.equal(
    press(resolution, { code: "ArrowDown", altKey: true }, { focus: "input", context: { ...ai, "ai.multiline.field": true } }),
    "ignore",
  );
  const selectedMessage = { "text.selection": true, "ai.message.selection": true };
  assert.equal(press(resolution, { code: "KeyC", metaKey: true }, { context: selectedMessage }), "ai:copy-message");
  assert.equal(press(resolution, { code: "KeyC", metaKey: true }, { focus: "input", context: selectedMessage }), "ignore");
});

function paletteShortcutOverridesFor(shortcut) {
  return withPaletteShortcut([], shortcut);
}
