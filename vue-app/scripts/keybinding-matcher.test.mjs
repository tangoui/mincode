import assert from "node:assert/strict";
import test from "node:test";
import { keybindingFocusClass, buildKeybindingContext, registerKeybindingContext } from "../src/domain/keybindings/context.ts";
import { CHORD_TIMEOUT_MS, KeybindingMatcher, isTextEditingChord } from "../src/domain/keybindings/matcher.ts";
import { parseKeySequence } from "../src/domain/keybindings/keys.ts";
import { resolveKeybindings } from "../src/domain/keybindings/resolver.ts";

const MODIFIER_CODES = { shift: "ShiftLeft", ctrl: "ControlLeft", alt: "AltLeft", meta: "MetaLeft" };

function key(code, modifiers = {}, extra = {}) {
  return { key: code.replace(/^Key/, "").toLowerCase(), code, ctrlKey: false, shiftKey: false, altKey: false, metaKey: false, ...modifiers, ...extra };
}

function setup(rules, { platform = "mac", doubleModifier = true, monacoMode = "input" } = {}) {
  const clock = { now: 1000 };
  const resolution = resolveKeybindings({ platform, presets: [{ id: "t", title: { zh: "t", en: "t" }, rules }] });
  const matcher = new KeybindingMatcher({
    now: () => clock.now,
    platform,
    resolution: () => resolution,
    doubleModifier: () => doubleModifier,
    monacoMode: () => monacoMode,
  });
  const env = (focus = "other", context = {}) => ({ focus, context, canExecute: () => true });
  return { clock, matcher, env };
}

const RULES = [
  { key: "mod+k", command: "builtin:command-palette" },
  { key: "mod+k mod+s", command: "builtin:keybindings" },
  { key: "mod+,", command: "builtin:settings" },
  { key: "mod+shift+l", command: "builtin:lock-app" },
  { key: "ctrl+tab", command: "workbench:next-tab" },
  { key: "mod+w", command: "workbench:close-tab" },
  { key: "shift shift", command: "builtin:search-everywhere" },
  { key: "escape", command: "ai:stop", when: "ai.panel.focus" },
  { key: "mod+c", command: "project:copy-node", when: "tree.selection" },
];

test("single vs multi-chord precedence follows rule order", () => {
  // RULES lists ⌘K ⌘S after ⌘K, so ⌘K starts a chord.
  const later = setup(RULES);
  assert.equal(later.matcher.handleKeydown(key("KeyK", { metaKey: true }), later.env()).kind, "pending");
  // Listed the other way round, the single chord wins.
  const earlier = setup([RULES[1], RULES[0]]);
  const decision = earlier.matcher.handleKeydown(key("KeyK", { metaKey: true }), earlier.env());
  assert.equal(decision.kind, "execute");
  assert.equal(decision.binding.command, "builtin:command-palette");
});

test("chord pending, completion, Esc cancel, mismatch cancel and timeout", () => {
  const { matcher, env, clock } = setup([{ key: "mod+k mod+s", command: "builtin:keybindings" }, { key: "mod+,", command: "builtin:settings" }]);
  assert.equal(matcher.handleKeydown(key("KeyK", { metaKey: true }), env()).kind, "pending");
  assert.equal(matcher.handlePendingKeydown(key("MetaLeft", { metaKey: true }), env()).kind, "ignore");
  const done = matcher.handlePendingKeydown(key("KeyS", { metaKey: true }), env());
  assert.equal(done.kind, "execute");
  assert.equal(done.binding.command, "builtin:keybindings");
  assert.equal(matcher.pending, null);

  matcher.handleKeydown(key("KeyK", { metaKey: true }), env());
  assert.deepEqual(matcher.handlePendingKeydown(key("Escape"), env()), { kind: "cancel", reason: "escape" });
  matcher.handleKeydown(key("KeyK", { metaKey: true }), env());
  assert.deepEqual(matcher.handlePendingKeydown(key("KeyX"), env()), { kind: "cancel", reason: "mismatch" });

  matcher.handleKeydown(key("KeyK", { metaKey: true }), env());
  clock.now += CHORD_TIMEOUT_MS;
  assert.equal(matcher.handlePendingKeydown(key("KeyS", { metaKey: true }), env()).kind, "ignore");
  assert.equal(matcher.pending, null);
  // Without a pending chord the capture handler never interferes.
  assert.equal(matcher.handlePendingKeydown(key("Comma", { metaKey: true }), env()).kind, "ignore");
});

test("IME events and auto-repeat", () => {
  const { matcher, env } = setup(RULES);
  assert.equal(matcher.handleKeydown(key("KeyK", { metaKey: true }, { isComposing: true }), env()).kind, "ignore");
  assert.equal(matcher.handleKeydown(key("KeyK", { metaKey: true }, { keyCode: 229 }), env()).kind, "ignore");
  assert.equal(matcher.handleKeydown({ ...key("KeyK", { metaKey: true }), key: "Process" }, env()).kind, "ignore");
  assert.equal(matcher.handleKeydown(key("Comma", { metaKey: true }, { repeat: true }), env()).kind, "consume");
  assert.equal(matcher.handleKeydown(key("Tab", { ctrlKey: true }, { repeat: true }), env()).kind, "execute");
});

test("focus classes: input, dialog, terminal and Monaco", () => {
  const { matcher, env } = setup(RULES);
  // Text-editing chords never leave an input.
  assert.equal(matcher.handleKeydown(key("KeyC", { metaKey: true }), env("input", { "tree.selection": true })).kind, "ignore");
  assert.equal(matcher.handleKeydown(key("KeyC", { metaKey: true }), env("other", { "tree.selection": true })).kind, "ignore");
  assert.equal(matcher.handleKeydown(key("Comma", { metaKey: true }), env("input")).kind, "execute");
  // allowInInput commands (AI panel Esc) work in the composer.
  assert.equal(matcher.handleKeydown(key("Escape"), env("input", { "ai.panel.focus": true })).kind, "execute");
  // Dialogs only admit lock.
  assert.equal(matcher.handleKeydown(key("Comma", { metaKey: true }), env("dialog")).kind, "ignore");
  assert.equal(matcher.handleKeydown(key("KeyL", { metaKey: true, shiftKey: true }), env("dialog")).kind, "execute");
  // Terminal: allowlist or any mac ⌘ chord.
  assert.equal(matcher.handleKeydown(key("KeyW", { metaKey: true }), env("terminal")).kind, "execute");
  assert.equal(matcher.handleKeydown(key("Tab", { ctrlKey: true }), env("terminal")).kind, "execute");
  // Monaco: input rules in phase 2, skipped in phase 3 mode.
  assert.equal(matcher.handleKeydown(key("Comma", { metaKey: true }), env("monaco")).kind, "execute");
  const skip = setup(RULES, { monacoMode: "skip" });
  assert.equal(skip.matcher.handleKeydown(key("Comma", { metaKey: true }), skip.env("monaco")).kind, "ignore");
});

test("terminal on win/linux leaves Ctrl chords to the shell unless allowlisted", () => {
  const { matcher, env } = setup([{ key: "ctrl+j", command: "builtin:ai" }, { key: "ctrl+r", command: "builtin:keybindings" }], { platform: "linux" });
  assert.equal(matcher.handleKeydown(key("KeyJ", { ctrlKey: true }), env("terminal")).kind, "execute");
  assert.equal(matcher.handleKeydown(key("KeyR", { ctrlKey: true }), env("terminal")).kind, "ignore");
});

test("text-editing chords per platform", () => {
  const chord = (text, platform) => parseKeySequence(text, platform).chords[0];
  assert.equal(isTextEditingChord(chord("cmd+a", "mac"), "mac"), true);
  assert.equal(isTextEditingChord(chord("ctrl+e", "mac"), "mac"), true);
  assert.equal(isTextEditingChord(chord("ctrl+e", "win"), "win"), false);
  assert.equal(isTextEditingChord(chord("shift+mod+z", "win"), "win"), true);
  assert.equal(isTextEditingChord(chord("alt+left", "linux"), "linux"), true);
  assert.equal(isTextEditingChord(chord("ctrl+backspace", "win"), "win"), true);
  assert.equal(isTextEditingChord(chord("home", "mac"), "mac"), true);
  assert.equal(isTextEditingChord(chord("mod+k", "mac"), "mac"), false);
});

function tap(matcher, env, clock, modifier = "shift", hold = 50) {
  const flag = `${modifier}Key`;
  const code = MODIFIER_CODES[modifier];
  matcher.handleModifierEvent("keydown", key(code, { [flag]: true }), env);
  clock.now += hold;
  return matcher.handleModifierEvent("keyup", key(code), env);
}

test("double modifier timing, resets and suppression", () => {
  const { matcher, env, clock } = setup(RULES);
  assert.equal(tap(matcher, env(), clock).kind, "ignore");
  clock.now += 200;
  const fired = tap(matcher, env(), clock);
  assert.equal(fired.kind, "execute");
  assert.equal(fired.binding.command, "builtin:search-everywhere");

  // Too slow between taps.
  tap(matcher, env(), clock);
  clock.now += 500;
  assert.equal(tap(matcher, env(), clock).kind, "ignore");
  clock.now += 1000;

  // Held too long.
  tap(matcher, env(), clock);
  clock.now += 100;
  assert.equal(tap(matcher, env(), clock, "shift", 400).kind, "ignore");
  clock.now += 1000;

  // Another key in between resets.
  tap(matcher, env(), clock);
  matcher.handleModifierEvent("keydown", key("KeyA", { shiftKey: true }), env());
  clock.now += 100;
  assert.equal(tap(matcher, env(), clock).kind, "ignore");
  clock.now += 1000;

  // Composition within 500 ms suppresses.
  matcher.noteComposition();
  clock.now += 100;
  tap(matcher, env(), clock);
  clock.now += 100;
  assert.equal(tap(matcher, env(), clock).kind, "ignore");
  clock.now += 1000;

  // Different modifier does not pair.
  tap(matcher, env(), clock, "ctrl");
  clock.now += 100;
  assert.equal(tap(matcher, env(), clock).kind, "ignore");
  clock.now += 1000;

  // Dialog focus and disabled option.
  tap(matcher, env("dialog"), clock);
  clock.now += 100;
  assert.equal(tap(matcher, env("dialog"), clock).kind, "ignore");
  const off = setup(RULES, { doubleModifier: false });
  tap(off.matcher, off.env(), off.clock);
  off.clock.now += 100;
  assert.equal(tap(off.matcher, off.env(), off.clock).kind, "ignore");
});

test("Monaco skip mode leaves chords to Monaco but still fires double-modifier taps", () => {
  const { matcher, env, clock } = setup(RULES, { monacoMode: "skip" });
  // Chords in Monaco are mirrored by services/monaco-keymap.ts, not the window dispatcher.
  assert.equal(matcher.handleKeydown(key("KeyK", { metaKey: true }), env("monaco")).kind, "ignore");
  tap(matcher, env("monaco"), clock);
  clock.now += 100;
  const fired = tap(matcher, env("monaco"), clock);
  assert.equal(fired.kind, "execute");
  assert.equal(fired.binding.command, "builtin:search-everywhere");
});

function element({ tag = "DIV", within = [], type = "", editable = false } = {}) {
  return {
    tagName: tag,
    isContentEditable: editable,
    getAttribute: (name) => (name === "type" ? type : null),
    closest: (selector) => (selector.split(",").some((part) => within.includes(part.trim())) ? {} : null),
  };
}

test("focus classification and context building", () => {
  assert.equal(keybindingFocusClass(element({ tag: "TEXTAREA", within: [".monaco-editor"] })), "monaco");
  // The database SQL editor is CodeMirror (a contenteditable): it is an editor, not a plain input.
  assert.equal(keybindingFocusClass(element({ editable: true, within: [".app-code-editor"] })), "monaco");
  assert.equal(keybindingFocusClass(element({ tag: "TEXTAREA", within: [".xterm"] })), "terminal");
  assert.equal(keybindingFocusClass(element({ tag: "INPUT", within: [".el-dialog"] })), "input");
  assert.equal(keybindingFocusClass(element({ tag: "INPUT", type: "checkbox" })), "other");
  assert.equal(keybindingFocusClass(element({ within: [".el-drawer"] })), "dialog");
  assert.equal(keybindingFocusClass(element({ editable: true })), "input");
  assert.equal(keybindingFocusClass(null), "other");

  const dispose = registerKeybindingContext("ai.running", () => true);
  const context = buildKeybindingContext({
    target: element({ tag: "TEXTAREA", within: [".ai-assistant-drawer__panel"] }),
    platform: "mac",
  });
  assert.equal(context["ai.running"], true);
  assert.equal(context["ai.panel.focus"], true);
  assert.equal(context["input.focus"], true);
  assert.equal(context["editor.focus"], false);
  dispose();
  assert.equal(buildKeybindingContext({ target: null, platform: "mac" })["ai.running"], undefined);
});
