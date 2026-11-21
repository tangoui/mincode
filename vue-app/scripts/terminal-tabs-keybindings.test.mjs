import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { getKeybindingCommand } from "../src/domain/keybindings/catalog.ts";
import { validateKeymapPreset } from "../src/domain/keybindings/preset.ts";
import { HOST_KEYBINDING_RULES, MINCODE_PRESET } from "../src/domain/keybindings/presets/mincode.ts";
import { resolveKeybindings } from "../src/domain/keybindings/resolver.ts";
import { TERMINAL_SKIP_SHELL_COMMANDS, decideTerminalKey } from "../src/domain/terminal/keybindings.ts";
import { messagesEn } from "../src/i18n/locales/en.ts";
import { messagesZh } from "../src/i18n/locales/zh.ts";

// Terminal tab shortcuts: ⌘T / Ctrl+Alt+T (new tab), ⇧⌘[ / Ctrl+PageUp (previous),
// ⇧⌘] / Ctrl+PageDown (next), all scoped to a focused terminal. Only the terminal
// workspace view registers the handlers, so a project-page terminal ignores them.

const SRC = new URL("../src/", import.meta.url);
const COMMANDS = ["terminal:new-tab", "terminal:prev-tab", "terminal:next-tab"];

function rulesOf(command) {
  return MINCODE_PRESET.rules.filter((rule) => rule.command === command);
}

test("the MinCode preset binds the terminal tab commands per platform while a terminal has focus", () => {
  const strip = (rule) => ({ key: rule.key, platform: rule.platform, when: rule.when });
  assert.deepEqual(rulesOf("terminal:new-tab").map(strip), [
    { key: "cmd+t", platform: "mac", when: "terminal.focus" },
    { key: "ctrl+alt+t", platform: "win", when: "terminal.focus" },
  ]);
  assert.deepEqual(rulesOf("terminal:prev-tab").map(strip), [
    { key: "cmd+shift+[", platform: "mac", when: "terminal.focus" },
    { key: "ctrl+pageup", platform: ["win", "linux"], when: "terminal.focus" },
  ]);
  assert.deepEqual(rulesOf("terminal:next-tab").map(strip), [
    { key: "cmd+shift+]", platform: "mac", when: "terminal.focus" },
    { key: "ctrl+pagedown", platform: ["win", "linux"], when: "terminal.focus" },
  ]);
  // ⌘W stays unbound for terminal tabs.
  assert.equal(MINCODE_PRESET.rules.some((rule) => rule.command.startsWith("terminal:") && /\+w$/.test(rule.key ?? "")), false);
  assert.equal(HOST_KEYBINDING_RULES.some((rule) => COMMANDS.includes(rule.command)), false);
  const result = validateKeymapPreset(MINCODE_PRESET);
  assert.equal(result.ok, true, JSON.stringify(result.errors));
});

test("the catalog lists the terminal tab commands as available app commands with i18n titles", () => {
  for (const id of COMMANDS) {
    const entry = getKeybindingCommand(id);
    assert.ok(entry, id);
    assert.equal(entry.handler, "app", id);
    assert.equal(entry.category, "view", id);
    assert.equal(entry.available, true, id);
    assert.equal(entry.showInPalette, id === "terminal:new-tab", id);
    assert.equal(entry.titleKey, `keybindings.command.${id}`);
  }
  assert.equal(messagesZh["keybindings.command.terminal:new-tab"], "新建终端标签");
  assert.equal(messagesZh["keybindings.command.terminal:next-tab"], "下一个终端标签");
  assert.equal(messagesZh["keybindings.command.terminal:prev-tab"], "上一个终端标签");
  for (const id of COMMANDS) assert.equal(typeof messagesEn[`keybindings.command.${id}`], "string", id);
});

test("the terminal tab commands may take keys away from the shell", () => {
  for (const id of COMMANDS) assert.equal(TERMINAL_SKIP_SHELL_COMMANDS.has(id), true, id);
});

function keydown({ code, key, ctrl = false, shift = false, alt = false, meta = false }) {
  return { type: "keydown", key, code, ctrlKey: ctrl, shiftKey: shift, altKey: alt, metaKey: meta };
}

function decide(platform, event, registered) {
  return decideTerminalKey(event, {
    platform,
    resolution: resolveKeybindings({ platform, presets: [MINCODE_PRESET], host: HOST_KEYBINDING_RULES }),
    handles: () => true,
    hasSelection: false,
    sendKeybindingsToShell: false,
    canExecuteApp: (command) => registered.has(command),
  });
}

test("in a terminal the keys reach the window dispatcher only while the terminal view registers the commands", () => {
  const all = new Set(COMMANDS);
  const none = new Set();
  const cases = [
    ["mac", keydown({ code: "KeyT", key: "t", meta: true }), "terminal:new-tab"],
    ["mac", keydown({ code: "BracketLeft", key: "{", meta: true, shift: true }), "terminal:prev-tab"],
    ["mac", keydown({ code: "BracketRight", key: "}", meta: true, shift: true }), "terminal:next-tab"],
    ["win", keydown({ code: "KeyT", key: "t", ctrl: true, alt: true }), "terminal:new-tab"],
    ["win", keydown({ code: "PageUp", key: "PageUp", ctrl: true }), "terminal:prev-tab"],
    ["linux", keydown({ code: "PageDown", key: "PageDown", ctrl: true }), "terminal:next-tab"],
  ];
  for (const [platform, event, command] of cases) {
    const decision = decide(platform, event, all);
    assert.equal(decision.kind, "bubble", `${platform} ${command}`);
    assert.equal(decision.binding?.command, command, `${platform} ${command}`);
  }
  // Project page: no handler registered, so Ctrl+PageUp/Down stay with the shell.
  assert.equal(decide("win", keydown({ code: "PageUp", key: "PageUp", ctrl: true }), none).kind, "shell");
  assert.equal(decide("linux", keydown({ code: "PageDown", key: "PageDown", ctrl: true }), none).kind, "shell");
});

test("only the terminal workspace view registers the terminal tab commands, while it is the active view", async () => {
  const view = await readFile(new URL("views/TerminalWorkspaceView.vue", SRC), "utf8");
  assert.match(view, /useActiveViewCommands\(/);
  for (const id of COMMANDS) assert.match(view, new RegExp(`"${id}"`), id);
  // ⌘T reuses the "+" button handler (limit message, live cwd inheritance).
  assert.match(view, /"terminal:new-tab"[\s\S]*?addShellTab\(\{ shellType: "system" \}\)/);
  assert.match(view, /selectAdjacentTab\(1\)/);
  assert.match(view, /selectAdjacentTab\(-1\)/);
  assert.match(view, /route\.name === "terminal"/);
  const project = await readFile(new URL("views/ProjectWorkspaceView.vue", SRC), "utf8");
  for (const id of COMMANDS) assert.doesNotMatch(project, new RegExp(id), id);
});
