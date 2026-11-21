import assert from "node:assert/strict";
import test from "node:test";
import {
  formatKeySequence,
  formatKeyText,
  isTerminalFindKey,
  keyboardPlatformFrom,
  keyChordFromEvent,
  parseKeySequence,
  sequenceId,
} from "../src/domain/keybindings/keys.ts";

const event = (code, key, modifiers = {}) => ({
  code,
  key,
  ctrlKey: false,
  shiftKey: false,
  altKey: false,
  metaKey: false,
  ...modifiers,
});

test("platform detection", () => {
  assert.equal(keyboardPlatformFrom("MacIntel"), "mac");
  assert.equal(keyboardPlatformFrom("macOS"), "mac");
  assert.equal(keyboardPlatformFrom("Win32"), "win");
  assert.equal(keyboardPlatformFrom("Windows"), "win");
  assert.equal(keyboardPlatformFrom("Linux x86_64"), "linux");
  assert.equal(keyboardPlatformFrom(undefined), "linux");
});

test("mod expands to meta on macOS and ctrl elsewhere", () => {
  assert.equal(sequenceId(parseKeySequence("mod+shift+p", "mac")), "shift+meta+p");
  assert.equal(sequenceId(parseKeySequence("mod+shift+p", "win")), "ctrl+shift+p");
  assert.equal(sequenceId(parseKeySequence("Mod+Shift+P", "linux")), "ctrl+shift+p");
  assert.equal(sequenceId(parseKeySequence("cmd+alt+up", "win")), "alt+meta+up");
  assert.equal(sequenceId(parseKeySequence("option+f12", "mac")), "alt+f12");
});

test("chords, double modifiers and named keys", () => {
  assert.deepEqual(parseKeySequence("cmd+k cmd+s", "mac"), {
    kind: "chords",
    chords: [
      { ctrl: false, shift: false, alt: false, meta: true, code: "KeyK" },
      { ctrl: false, shift: false, alt: false, meta: true, code: "KeyS" },
    ],
  });
  assert.deepEqual(parseKeySequence("shift shift", "mac"), { kind: "double", modifier: "shift" });
  assert.deepEqual(parseKeySequence("mod mod", "win"), { kind: "double", modifier: "ctrl" });
  assert.equal(sequenceId(parseKeySequence("ctrl+`", "win")), "ctrl+`");
  assert.equal(sequenceId(parseKeySequence("ctrl+shift+-", "mac")), "ctrl+shift+-");
  assert.equal(sequenceId(parseKeySequence("ctrl+pagedown", "win")), "ctrl+pagedown");
  assert.equal(sequenceId(parseKeySequence("escape", "win")), "escape");
  assert.equal(sequenceId(parseKeySequence("esc", "win")), "escape");
});

test("invalid key text is rejected", () => {
  for (const text of ["", "   ", "mod+", "+k", "ctrl+ctrl+k", "hyper+k", "ctrl+shift", "a b c", "shift alt", "ctrl+f25", "x".repeat(80), "ctrl+k shift"]) {
    assert.equal(parseKeySequence(text, "mac"), null, text);
  }
});

test("events map through the physical code, not the produced character", () => {
  // ⌥L on macOS produces "¬"; the physical key is still KeyL.
  assert.equal(sequenceId({ kind: "chords", chords: [keyChordFromEvent(event("KeyL", "¬", { altKey: true }))] }), "alt+l");
  assert.equal(keyChordFromEvent(event("ShiftLeft", "Shift", { shiftKey: true })), null);
  assert.equal(keyChordFromEvent(event("MetaRight", "Meta", { metaKey: true })), null);
  assert.equal(keyChordFromEvent(event("NumpadEnter", "Enter")).code, "Enter");
  assert.equal(keyChordFromEvent(event("Numpad7", "7")).code, "Digit7");
  // Synthetic events without a code fall back to the key.
  assert.equal(keyChordFromEvent(event("", "k", { metaKey: true })).code, "KeyK");
  assert.equal(keyChordFromEvent(event("", "ArrowUp")).code, "ArrowUp");
  assert.equal(keyChordFromEvent(event("", "Process")), null);
});

test("labels use mac glyphs and Windows/Linux text", () => {
  const label = (text, platform) => formatKeyText(text, platform);
  assert.equal(label("mod+shift+p", "mac"), "⇧⌘P");
  assert.equal(label("ctrl+alt+shift+cmd+k", "mac"), "⌃⌥⇧⌘K");
  assert.equal(label("mod+shift+p", "win"), "Ctrl+Shift+P");
  assert.equal(label("mod+k mod+s", "mac"), "⌘K ⌘S");
  assert.equal(label("mod+k mod+s", "linux"), "Ctrl+K Ctrl+S");
  assert.equal(label("shift shift", "mac"), "⇧⇧");
  assert.equal(label("shift shift", "win"), "Shift Shift");
  assert.equal(label("alt+up", "mac"), "⌥↑");
  assert.equal(label("alt+up", "win"), "Alt+Up");
  assert.equal(label("mod+enter", "mac"), "⌘↩");
  assert.equal(label("mod+backspace", "mac"), "⌘⌫");
  assert.equal(label("escape", "mac"), "⎋");
  assert.equal(label("escape", "win"), "Esc");
  assert.equal(label("ctrl+tab", "mac"), "⌃⇥");
  assert.equal(label("mod+/", "win"), "Ctrl+/");
  assert.equal(label("meta+k", "win"), "Win+K");
  assert.equal(label("meta+k", "linux"), "Super+K");
  assert.equal(label("f12", "mac"), "F12");
  assert.equal(label("not a key!", "mac"), "not a key!");
  assert.equal(formatKeySequence(parseKeySequence("mod+space", "mac"), "mac"), "⌘␣");
});

test("terminal find key is ⌘F on macOS and Ctrl+F elsewhere", () => {
  assert.equal(isTerminalFindKey(event("KeyF", "f", { metaKey: true }), "mac"), true);
  assert.equal(isTerminalFindKey(event("KeyF", "f", { ctrlKey: true }), "mac"), false);
  assert.equal(isTerminalFindKey(event("KeyF", "f", { ctrlKey: true }), "win"), true);
  assert.equal(isTerminalFindKey(event("KeyF", "f", { ctrlKey: true }), "linux"), true);
  assert.equal(isTerminalFindKey(event("KeyF", "f", { metaKey: true }), "win"), false);
  assert.equal(isTerminalFindKey(event("KeyF", "F", { ctrlKey: true, shiftKey: true }), "win"), false);
  assert.equal(isTerminalFindKey(event("KeyF", "ƒ", { metaKey: true, altKey: true }), "mac"), false);
});
