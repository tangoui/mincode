import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const source = await readFile(new URL("../src/services/ui-theme.ts", import.meta.url), "utf8");

test("system theme preference resolves to concrete light or dark theme", () => {
  assert.match(source, /export type BuiltinUiTheme = "system"/);
  assert.match(source, /prefers-color-scheme: dark/);
  assert.match(source, /dataset\.themePreference = preference/);
  assert.match(source, /dataset\.theme = normalized/);
});

test("system listener is replaced instead of accumulated", () => {
  assert.match(source, /stopSystemThemeListener\?\.\(\)/);
  assert.match(source, /addEventListener\("change", listener, \{ once: true \}\)/);
  assert.match(source, /removeEventListener\("change", listener\)/);
});
