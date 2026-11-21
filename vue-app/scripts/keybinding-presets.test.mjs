import assert from "node:assert/strict";
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { KEYBINDING_COMMANDS, KEYMAP_COMMAND_MAX_LENGTH, KEYMAP_COMMAND_PATTERN } from "../src/domain/keybindings/catalog.ts";
import { MONACO_DEFAULT_BINDINGS, monacoDefaultRules } from "../src/domain/keybindings/monaco-defaults.ts";
import {
  getKeymapPreset,
  keymapPresetChain,
  KEYMAP_PRESET_LIMITS,
  listKeymapPresets,
  registerKeymapPreset,
  subscribeKeymapPresets,
  validateKeymapPreset,
} from "../src/domain/keybindings/preset.ts";
import { BUILTIN_KEYMAP_PRESETS, HOST_KEYBINDING_RULES, registerBuiltinKeymapPresets } from "../src/domain/keybindings/presets/index.ts";
import { findKeybindingConflicts, resolveKeybindings } from "../src/domain/keybindings/resolver.ts";
import { ALL_KEYMAP_PRESETS, keymapPresetChainOf } from "./keymap-presets-fixture.mjs";
import { messagesEn } from "../src/i18n/locales/en.ts";
import { messagesZh } from "../src/i18n/locales/zh.ts";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const PLATFORMS = ["mac", "win", "linux"];

test("built-in presets pass the external validator and register once", () => {
  for (const preset of BUILTIN_KEYMAP_PRESETS) {
    const result = validateKeymapPreset(preset);
    assert.equal(result.ok, true, `${preset.id}: ${JSON.stringify(result.errors)}`);
  }
  registerBuiltinKeymapPresets();
  registerBuiltinKeymapPresets();
  assert.deepEqual(BUILTIN_KEYMAP_PRESETS.map((preset) => preset.id), ["mincode"]);
  assert.equal(getKeymapPreset("mincode").source, "builtin");
});

test("validator rejects unknown commands, reserved keys, bad whens, bad shape and oversize", () => {
  const base = { id: "ext", title: { zh: "扩展", en: "Ext" } };
  const errors = (data) => {
    const result = validateKeymapPreset(data);
    return result.ok ? [] : result.errors.map((issue) => issue.path);
  };
  assert.deepEqual(errors({ ...base, rules: [{ key: "mod+k", command: "vscode:thing" }] }), ["rules[0].command"]);
  assert.deepEqual(errors({ ...base, rules: [{ key: "cmd+q", command: "builtin:settings", platform: "mac" }] }), ["rules[0].key"]);
  assert.deepEqual(errors({ ...base, rules: [{ key: "ctrl+alt+l", command: "builtin:settings" }] }), ["rules[0].key"]);
  // WebView accelerators are bindable (F12 = go to definition); the settings UI warns instead.
  assert.deepEqual(errors({ ...base, rules: [{ key: "f12", command: "builtin:settings" }] }), []);
  assert.deepEqual(errors({ ...base, rules: [{ key: "mod+k", command: "builtin:settings", when: "a || b" }] }), ["rules[0].when"]);
  assert.deepEqual(errors({ ...base, rules: [{ key: "mod+k", command: "builtin:settings", extra: 1 }] }), ["rules[0].extra"]);
  assert.deepEqual(errors({ ...base, rules: [{ command: "builtin:settings" }] }), ["rules[0].key"]);
  assert.deepEqual(errors({ ...base, rules: [{ command: "-builtin:settings" }] }), []);
  assert.deepEqual(errors({ ...base, rules: [{ key: "mod+k", command: "plugin:acme:run" }] }), ["rules[0].command"]);
  assert.deepEqual(errors({ ...base, rules: [{ key: "mod+k", command: "builtin:settings", platform: "bsd" }] }), ["rules[0].platform"]);
  assert.deepEqual(errors({ ...base, editorMode: "emacs", rules: [] }), ["editorMode"]);
  assert.deepEqual(errors({ ...base, extends: "ext", rules: [] }), ["extends"]);
  assert.deepEqual(errors({ ...base, id: "Bad Id", rules: [] }), ["id"]);
  assert.deepEqual(errors({ id: "x", title: { zh: "x" }, rules: [] }), ["title.en"]);
  const many = Array.from({ length: KEYMAP_PRESET_LIMITS.maxRules + 1 }, () => ({ key: "f1", command: "builtin:settings" }));
  assert.deepEqual(errors({ ...base, rules: many }), ["rules"]);
  assert.equal(validateKeymapPreset({ ...base, editorMode: "vim", rules: [] }).ok, true);
});

// Known, intentional collisions inside a preset (resolved by rule order or context).
const CONFLICT_ALLOWLIST = new Set([
  // VS Code: ⌘K clears the terminal; the ⌘K ⌘S chord applies elsewhere.
  "keymaps.vscode:mac:prefix:builtin:keybindings:terminal:clear",
]);

test("presets: the built-in preset resolves on every platform without unexpected internal conflicts", () => {
  for (const preset of ALL_KEYMAP_PRESETS) {
    for (const platform of PLATFORMS) {
      const resolution = resolveKeybindings({ platform, presets: keymapPresetChainOf(preset.id) });
      assert.deepEqual(resolution.issues, [], `${preset.id}/${platform}`);
      const conflicts = findKeybindingConflicts(resolution.bindings)
        .map((conflict) => `${preset.id}:${platform}:${conflict.kind}:${conflict.first.command}:${conflict.second.command}`)
        .filter((id) => !CONFLICT_ALLOWLIST.has(id));
      assert.deepEqual(conflicts, [], `${preset.id}/${platform} conflicts`);
    }
  }
  // Host rules resolve on every platform too.
  for (const platform of PLATFORMS) {
    assert.deepEqual(resolveKeybindings({ platform, presets: [], host: HOST_KEYBINDING_RULES }).issues, []);
    assert.deepEqual(resolveKeybindings({ platform, presets: [], defaults: monacoDefaultRules(platform) }).issues, []);
  }
});

test("catalog commands have zh and en titles", () => {
  for (const command of KEYBINDING_COMMANDS) {
    assert.ok(messagesZh[command.titleKey], `zh ${command.titleKey}`);
    assert.ok(messagesEn[command.titleKey], `en ${command.titleKey}`);
  }
  for (const key of ["keybindings.chordPending", "keybindings.chordCancelled"]) {
    assert.ok(messagesZh[key] && messagesEn[key], key);
  }
});

function listJs(directory) {
  if (!existsSync(directory)) return [];
  return readdirSync(directory).flatMap((name) => {
    const path = join(directory, name);
    if (statSync(path).isDirectory()) return listJs(path);
    return name.endsWith(".js") ? [path] : [];
  });
}

test("every referenced Monaco command exists in a module MinCode imports", () => {
  const runtimePath = join(root, "src/services/monaco-runtime.js");
  const runtime = readFileSync(runtimePath, "utf8");
  const registers = [...runtime.matchAll(/import\s+"([^"]+\/register\.js)"/g)].map((match) => resolve(dirname(runtimePath), match[1]));
  assert.ok(registers.length > 10, "monaco-runtime.js imports feature register.js files");
  const monacoRoot = join(root, "node_modules/monaco-editor/esm/vs/editor");
  // Follow relative imports from each register.js through feature modules
  // (editor/contrib and editor/standalone), so a command defined in a sibling
  // module the feature entry imports (e.g. gotoSymbol/goToCommands.js) counts.
  const featureRoots = [join(monacoRoot, "contrib"), join(monacoRoot, "standalone")];
  const reachable = new Set();
  const queue = [...registers];
  while (queue.length) {
    const file = queue.pop();
    if (!existsSync(file)) continue;
    const source = readFileSync(file, "utf8");
    for (const match of source.matchAll(/(?:import|export)\s+(?:[^'"]*?from\s+)?['"](\.[^'"]+)['"]/g)) {
      const target = resolve(dirname(file), match[1]);
      if (reachable.has(target) || !featureRoots.some((prefix) => target.startsWith(prefix))) continue;
      reachable.add(target);
      queue.push(target);
    }
  }
  // Core editor commands are always part of the editor API.
  const files = [...reachable, join(monacoRoot, "browser/coreCommands.js"), join(monacoRoot, "browser/editorExtensions.js")];
  const contribFiles = new Set(listJs(join(monacoRoot, "contrib")));
  const sources = files.map((file) => readFileSync(file, "utf8")).join("\n");
  const ids = new Set([
    ...KEYBINDING_COMMANDS.map((command) => command.monacoCommand).filter((id) => id && !id.startsWith("mincode.")),
    ...MONACO_DEFAULT_BINDINGS.map((binding) => binding.command),
  ]);
  assert.ok(contribFiles.size > 50, "Monaco contrib sources are installed");
  for (const id of ids) {
    const quoted = new RegExp(`['"]${id.replace(/\./g, "\\.")}['"]`);
    assert.ok(quoted.test(sources), `${id} is not defined in a Monaco module imported by monaco-runtime.js`);
  }
});

test("every mincode.* Monaco command in the catalog is registered by the keymap service", () => {
  const service = readFileSync(join(root, "src/services/monaco-keymap.ts"), "utf8");
  const ids = KEYBINDING_COMMANDS.map((command) => command.monacoCommand).filter((id) => id?.startsWith("mincode."));
  assert.ok(ids.length >= 2);
  for (const id of ids) assert.ok(service.includes(`registerCommand("${id}"`), `${id} has no Monaco command handler`);
});
