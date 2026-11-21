import assert from "node:assert/strict";
import test from "node:test";
import { monacoDefaultRules } from "../src/domain/keybindings/monaco-defaults.ts";
import { HOST_KEYBINDING_RULES } from "../src/domain/keybindings/presets/index.ts";
import { resolveKeybindings } from "../src/domain/keybindings/resolver.ts";
import { createTerminalKeyHandler, decideTerminalKey } from "../src/domain/terminal/keybindings.ts";
import { keymapPresetChainOf } from "./keymap-presets-fixture.mjs";

// Phase 4: the xterm custom key handler decides per keydown whether a
// terminal command runs (preventDefault, false), the key bubbles to the window
// dispatcher (false, no preventDefault) or the shell receives it (true).

// Short names used by the matrix below → preset ids (VS Code and JetBrains ship as official keymap plugins).
const CODES = { c: "KeyC", v: "KeyV", f: "KeyF", k: "KeyK", a: "KeyA", l: "KeyL", r: "KeyR", w: "KeyW", p: "KeyP", i: "KeyI", q: "KeyQ" };
// Commands registered at startup (use-app-commands.ts); everything else is not executable yet.
const REGISTERED = new Set(["builtin:command-palette", "builtin:settings", "builtin:ai"]);

function resolution(presetId, platform, user = []) {
  return resolveKeybindings({
    platform,
    presets: keymapPresetChainOf(presetId),
    defaults: monacoDefaultRules(platform),
    host: HOST_KEYBINDING_RULES,
    user,
  });
}

/** "ctrl+shift+c" → keydown event fake. */
function keydown(text, extra = {}) {
  const parts = text.split("+");
  const key = parts.pop();
  const event = {
    type: "keydown",
    key,
    code: CODES[key] ?? key,
    ctrlKey: parts.includes("ctrl"),
    shiftKey: parts.includes("shift"),
    altKey: parts.includes("alt"),
    metaKey: parts.includes("cmd"),
    defaultPrevented: false,
    ...extra,
  };
  event.preventDefault = () => {
    event.defaultPrevented = true;
  };
  return event;
}

function fakeXterm(selected = false) {
  return {
    selected,
    cleared: 0,
    hasSelection() {
      return this.selected;
    },
    clearSelection() {
      this.selected = false;
      this.cleared += 1;
    },
  };
}

function harness({ preset = "mincode", platform = "mac", selected = false, sendKeybindingsToShell = false, user = [], omit = [] } = {}) {
  const xterm = fakeXterm(selected);
  const calls = [];
  const actions = { xterm: () => xterm };
  for (const name of ["copy", "paste", "find", "clear", "selectAll"]) {
    if (!omit.includes(name)) actions[name] = () => calls.push(name);
  }
  const resolved = resolution(preset, platform, user);
  const handler = createTerminalKeyHandler(actions, {
    platform,
    resolution: () => resolved,
    sendKeybindingsToShell: () => sendKeybindingsToShell,
    canExecuteApp: (command) => REGISTERED.has(command),
  });
  /** Returns "run:<action>" | "bubble" | "shell" plus sanity checks on the xterm contract. */
  const press = (text, extra) => {
    const event = keydown(text, extra);
    const before = calls.length;
    const result = handler(event);
    if (result === true) {
      assert.equal(event.defaultPrevented, false, `${text}: shell keys keep default`);
      assert.equal(calls.length, before, `${text}: shell keys run nothing`);
      return "shell";
    }
    if (calls.length > before) {
      assert.equal(event.defaultPrevented, true, `${text}: executed terminal commands preventDefault`);
      return `run:${calls.at(-1)}`;
    }
    assert.equal(event.defaultPrevented, false, `${text}: bubbled keys must reach the dispatcher`);
    return "bubble";
  };
  return { press, xterm, calls };
}

const MATRIX = [
  // [preset, platform, selected, key, expected]
  ["mincode", "mac", true, "cmd+c", "run:copy"],
  ["mincode", "mac", false, "cmd+c", "bubble"],
  ["mincode", "mac", false, "cmd+v", "run:paste"],
  ["mincode", "mac", false, "cmd+f", "run:find"],
  ["mincode", "mac", false, "cmd+a", "run:selectAll"],
  ["mincode", "mac", false, "cmd+k", "bubble"], // palette
  ["mincode", "mac", false, "cmd+q", "bubble"], // any ⌘ chord
  ["mincode", "mac", false, "ctrl+c", "shell"],
  ["mincode", "mac", false, "ctrl+r", "shell"],
  ["mincode", "mac", false, "ctrl+f", "shell"],
  ["mincode", "win", true, "ctrl+c", "run:copy"],
  ["mincode", "win", false, "ctrl+c", "shell"],
  ["mincode", "win", false, "ctrl+shift+c", "run:copy"],
  ["mincode", "win", false, "ctrl+v", "run:paste"],
  ["mincode", "win", false, "ctrl+shift+v", "run:paste"],
  ["mincode", "win", false, "ctrl+f", "run:find"],
  ["mincode", "win", false, "ctrl+shift+l", "shell"],
  ["mincode", "linux", false, "ctrl+shift+l", "shell"],
  ["mincode", "win", false, "ctrl+k", "bubble"], // palette is on the skip-shell list
  ["mincode", "win", false, "ctrl+w", "shell"], // close-tab has !terminal.focus
  ["mincode", "win", false, "ctrl+i", "shell"], // AI toggle is not a skip-shell command (Ctrl+I = Tab)
  ["mincode", "win", false, "ctrl+a", "shell"],
  ["mincode", "linux", true, "ctrl+c", "run:copy"],
];

test("execute / bubble / shell per preset and platform", () => {
  for (const [preset, platform, selected, key, expected] of MATRIX) {
    const { press } = harness({ preset, platform, selected });
    assert.equal(press(key), expected, `${preset} ${platform} ${key} (selection: ${selected})`);
  }
});

test("Ctrl+C copy on Windows/Linux clears the selection so the next Ctrl+C interrupts", async () => {
  const { press, xterm } = harness({ platform: "win", selected: true });
  assert.equal(press("ctrl+c"), "run:copy");
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(xterm.hasSelection(), false);
  assert.equal(press("ctrl+c"), "shell");
  // ⌘C on macOS keeps the selection.
  const mac = harness({ platform: "mac", selected: true });
  mac.press("cmd+c");
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(mac.xterm.cleared, 0);
});

test("sendKeybindingsToShell intercepts only ⌘ and Ctrl+Shift chords", () => {
  const win = harness({ platform: "win", selected: true, sendKeybindingsToShell: true });
  assert.equal(win.press("ctrl+c"), "shell");
  assert.equal(win.press("ctrl+v"), "shell");
  assert.equal(win.press("ctrl+f"), "shell");
  assert.equal(win.press("ctrl+k"), "shell");
  assert.equal(win.press("ctrl+shift+c"), "run:copy");
  assert.equal(win.press("ctrl+shift+v"), "run:paste");
  assert.equal(win.press("ctrl+shift+l"), "shell");
  const mac = harness({ platform: "mac", sendKeybindingsToShell: true });
  assert.equal(mac.press("cmd+v"), "run:paste");
  assert.equal(mac.press("cmd+k"), "bubble");
  assert.equal(mac.press("ctrl+r"), "shell");
});

test("IME composition and non-keydown events always reach xterm", () => {
  const { press } = harness({ platform: "win" });
  assert.equal(press("ctrl+v", { isComposing: true }), "shell");
  assert.equal(press("ctrl+v", { keyCode: 229 }), "shell");
  assert.equal(press("ctrl+v", { key: "Process" }), "shell");
  assert.equal(press("ctrl+v", { type: "keyup" }), "shell");
  assert.equal(press("ctrl+v", { type: "keypress" }), "shell");
});

test("missing terminal actions and user overrides fall through correctly", () => {
  // A log viewer without paste: Ctrl+V reaches xterm (disabled stdin), ⌘V bubbles.
  assert.equal(harness({ platform: "win", omit: ["paste"] }).press("ctrl+v"), "shell");
  assert.equal(harness({ platform: "mac", omit: ["paste", "find"] }).press("cmd+v"), "bubble");
  assert.equal(harness({ platform: "win", omit: ["find"] }).press("ctrl+f"), "shell");
  // User removal of Ctrl+V paste gives the key back to the shell (quoted insert).
  const removed = harness({ platform: "win", user: [{ command: "-terminal:paste", key: "ctrl+v" }] });
  assert.equal(removed.press("ctrl+v"), "shell");
  assert.equal(removed.press("ctrl+shift+v"), "run:paste");
  // User binding for clear on Windows.
  const bound = harness({ platform: "win", user: [{ command: "terminal:clear", key: "ctrl+shift+k" }] });
  assert.equal(bound.press("ctrl+shift+k"), "run:clear");
  assert.equal(bound.press("ctrl+k"), "bubble");
});

test("decideTerminalKey ignores bare modifiers and reports the bubbling binding", () => {
  const env = {
    platform: "win",
    resolution: resolution("mincode", "win"),
    handles: () => true,
    hasSelection: false,
    sendKeybindingsToShell: false,
    canExecuteApp: (command) => REGISTERED.has(command),
  };
  assert.equal(decideTerminalKey(keydown("ctrl+Control", { code: "ControlLeft" }), env).kind, "shell");
  const palette = decideTerminalKey(keydown("ctrl+k"), env);
  assert.equal(palette.kind, "bubble");
  assert.equal(palette.binding.command, "builtin:command-palette");
  // Unregistered skip-shell commands do not steal the key.
  assert.equal(decideTerminalKey(keydown("ctrl+k"), { ...env, canExecuteApp: () => false }).kind, "shell");
});
