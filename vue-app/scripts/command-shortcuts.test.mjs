import assert from "node:assert/strict";
import test from "node:test";
import {
  COMMAND_PALETTE_SHORTCUTS,
  commandPaletteShortcutLabel,
  normalizeCommandPaletteShortcut,
} from "../src/domain/commands/command-shortcuts.ts";
import { paletteShortcutFromOverrides, withPaletteShortcut } from "../src/domain/keybindings/persistence.ts";

// Key matching and the Monaco defer rule moved to the keybinding dispatcher;
// keybinding-persistence.test.mjs verifies the equivalent behaviour.

test("normalizes unsupported shortcuts to the default", () => {
  assert.equal(normalizeCommandPaletteShortcut("mod+p"), "mod+p");
  assert.equal(normalizeCommandPaletteShortcut("alt+k"), "mod+k");
  assert.equal(commandPaletteShortcutLabel("mod+shift+p"), "⌘ / Ctrl + Shift + P");
});

test("every legacy select option maps to keymap overrides and back", () => {
  for (const option of COMMAND_PALETTE_SHORTCUTS) {
    assert.equal(paletteShortcutFromOverrides(withPaletteShortcut([], option.id)), option.id);
  }
});
