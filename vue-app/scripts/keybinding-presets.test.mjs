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
import { ALL_KEYMAP_PRESETS, keymapPresetChainOf, loadOfficialKeymapPlugins, registerAllKeymapPresets } from "./keymap-plugin-presets.mjs";
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

test("official keymap plugin: one declarative manifest whose presets register with their plugin", () => {
  const plugins = loadOfficialKeymapPlugins();
  assert.deepEqual(plugins.map((plugin) => plugin.pluginId), ["keymaps"]);
  // The host accepts at most 8 presets per plugin (backend/plugin/keymaps.go keymapMaxPresets).
  assert.ok(plugins[0].presets.length <= 8);
  for (const plugin of plugins) {
    const { manifest } = plugin;
    assert.equal(manifest.apiVersion, 2, plugin.pluginId);
    assert.deepEqual(manifest.runtime, { type: "declarative" }, plugin.pluginId);
    assert.equal(manifest.capabilities, undefined, plugin.pluginId);
    assert.deepEqual(Object.keys(manifest.contributes), ["keymaps"], plugin.pluginId);
    assert.ok(manifest.tags.includes("keymap"), plugin.pluginId);
    for (const preset of plugin.presets) {
      const result = validateKeymapPreset(preset);
      assert.equal(result.ok, true, `${preset.id}: ${JSON.stringify(result.errors)}`);
      assert.ok(preset.id.startsWith(`${plugin.pluginId}.`), preset.id);
      assert.ok(JSON.stringify(preset).length <= KEYMAP_PRESET_LIMITS.maxSerializedBytes, preset.id);
    }
  }
  registerAllKeymapPresets();
  assert.deepEqual(
    listKeymapPresets().map((preset) => preset.id),
    ["mincode", "keymaps.vscode", "keymaps.intellij", "keymaps.eclipse", "keymaps.vim"],
  );
  assert.equal(getKeymapPreset("keymaps.intellij").doubleModifier, true);
  assert.equal(getKeymapPreset("keymaps.vim").editorMode, "vim");
  assert.deepEqual(keymapPresetChain("keymaps.vim").map((preset) => preset.id), ["mincode", "keymaps.vim"]);
  const vscode = getKeymapPreset("keymaps.vscode");
  assert.equal(vscode.source, "plugin");
  assert.equal(vscode.pluginId, "keymaps");
  assert.equal(vscode.pluginName, "键位方案 / Keymaps");
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
  assert.deepEqual(errors({ ...base, rules: [{ key: "mod+k", command: "plugin:acme:run" }] }), []);
  assert.deepEqual(errors({ ...base, rules: [{ key: "mod+k", command: "builtin:settings", platform: "bsd" }] }), ["rules[0].platform"]);
  assert.deepEqual(errors({ ...base, editorMode: "emacs", rules: [] }), ["editorMode"]);
  assert.deepEqual(errors({ ...base, extends: "ext", rules: [] }), ["extends"]);
  assert.deepEqual(errors({ ...base, id: "Bad Id", rules: [] }), ["id"]);
  assert.deepEqual(errors({ id: "x", title: { zh: "x" }, rules: [] }), ["title.en"]);
  const many = Array.from({ length: KEYMAP_PRESET_LIMITS.maxRules + 1 }, () => ({ key: "f1", command: "builtin:settings" }));
  assert.deepEqual(errors({ ...base, rules: many }), ["rules"]);
  assert.equal(validateKeymapPreset({ ...base, editorMode: "vim", rules: [] }).ok, true);
});

test("keymap command ids follow the plugin host's pattern", () => {
  // The host validates plugin keymap files with keymapCommandPattern (keymaps.go); keep the frontend copy equal.
  const goSource = readFileSync(resolve(root, "../backend/plugin/keymaps.go"), "utf8");
  const goPattern = goSource.match(/keymapCommandPattern\s*=\s*regexp\.MustCompile\(`([^`]+)`\)/)?.[1];
  assert.equal(KEYMAP_COMMAND_PATTERN.source, goPattern);
  assert.match(goSource, /keymapMaxCommandLength\s*=\s*int\(keymapSpec\.Limit\("commandMaxLength"\)\)/);
  const contract = JSON.parse(readFileSync(resolve(root, "../plugins/api/contribution-points.json"), "utf8"));
  assert.equal(KEYMAP_COMMAND_MAX_LENGTH, contract.points.keymaps.limits.commandMaxLength);
  // Every catalog command must be bindable from a plugin keymap.
  for (const command of KEYBINDING_COMMANDS) {
    assert.match(command.id, KEYMAP_COMMAND_PATTERN, command.id);
    assert.ok(command.id.length <= KEYMAP_COMMAND_MAX_LENGTH, command.id);
  }
  // A bare id is refused by the frontend validator as it is by the host.
  const base = { id: "ext", title: { zh: "扩展", en: "Ext" } };
  const result = validateKeymapPreset({ ...base, rules: [{ key: "f8", command: "debug.stepOver" }] });
  assert.deepEqual(result.ok ? [] : result.errors.map((issue) => issue.path), ["rules[0].command"]);
  assert.equal(validateKeymapPreset({ ...base, rules: [{ key: "f8", command: "project:debug.stepOver" }] }).ok, true);
  assert.equal(validateKeymapPreset({ ...base, rules: [{ command: "-project:debug.stepOver" }] }).ok, true);
});

test("registry: plugin presets, disposal, built-in protection, chains", () => {
  registerBuiltinKeymapPresets();
  let notified = 0;
  const unsubscribe = subscribeKeymapPresets(() => notified++);
  assert.throws(() => registerKeymapPreset({ id: "mincode", title: { zh: "x", en: "x" }, rules: [] }, { source: "plugin", pluginId: "evil" }));
  assert.throws(() => registerKeymapPreset({ id: "bad", title: { zh: "x", en: "x" }, rules: [{ key: "cmd+q", command: "builtin:ai", platform: "mac" }] }, { source: "plugin" }));
  // Plugins own the `<pluginId>.` namespace and may extend only MinCode or their own presets.
  assert.throws(() => registerKeymapPreset({ id: "other.x", title: { zh: "x", en: "x" }, rules: [] }, { source: "plugin", pluginId: "acme" }));
  assert.throws(() => registerKeymapPreset({ id: "acme.x", title: { zh: "x", en: "x" }, rules: [] }, { source: "plugin" }));
  assert.throws(() =>
    registerKeymapPreset({ id: "acme.x", title: { zh: "x", en: "x" }, extends: "keymaps.vscode", rules: [] }, { source: "plugin", pluginId: "acme" }),
  );
  const disposeBase = registerKeymapPreset(
    { id: "acme.base", title: { zh: "基础", en: "Base" }, extends: "mincode", rules: [{ key: "f3", command: "builtin:quick-open" }] },
    { source: "plugin", pluginId: "acme" },
  );
  const dispose = registerKeymapPreset(
    { id: "acme.child", title: { zh: "子方案", en: "Child" }, extends: "acme.base", rules: [{ key: "mod+shift+r", command: "builtin:quick-open" }] },
    { source: "plugin", pluginId: "acme", pluginName: "Acme" },
  );
  assert.equal(getKeymapPreset("acme.child").pluginId, "acme");
  assert.equal(getKeymapPreset("acme.child").pluginName, "Acme");
  assert.deepEqual(keymapPresetChain("acme.child").map((preset) => preset.id), ["mincode", "acme.base", "acme.child"]);
  assert.equal(keymapPresetChain("missing"), null);
  const cyclic = { a: { id: "a", extends: "b", rules: [] }, b: { id: "b", extends: "a", rules: [] } };
  assert.equal(keymapPresetChain("a", (id) => cyclic[id]), null);
  disposeBase();
  // A missing ancestor makes the chain unavailable (the store falls back to MinCode).
  assert.equal(keymapPresetChain("acme.child"), null);
  dispose();
  assert.equal(getKeymapPreset("acme.child"), undefined);
  assert.equal(notified, 4);
  unsubscribe();
});

// Known, intentional collisions inside a preset (resolved by rule order or context).
const CONFLICT_ALLOWLIST = new Set([
  // VS Code: ⌘K clears the terminal; the ⌘K ⌘S chord applies elsewhere.
  "keymaps.vscode:mac:prefix:builtin:keybindings:terminal:clear",
]);

test("presets: every built-in and plugin preset resolves on every platform without unexpected internal conflicts", () => {
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
