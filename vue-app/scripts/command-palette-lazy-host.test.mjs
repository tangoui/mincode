import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

test("command palette search engine loads only after the first shortcut request", async () => {
  const [app, host] = await Promise.all([
    readFile(new URL("../src/App.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/components/layout/AppCommandPaletteHost.vue", import.meta.url), "utf8"),
  ]);

  assert.doesNotMatch(app, /from "@\/composables\/use-command-palette"/);
  assert.match(app, /defineAsyncComponent\([\s\S]*AppCommandPaletteHost\.vue/);
  // The shortcut arrives through the keybinding dispatcher as the
  // builtin:command-palette command, which only bumps the request sequence.
  assert.match(app, /useAppCommands\(\{ openPalette: openCommandPalette/);
  assert.match(app, /seq: commandPaletteOpenRequest\.value\.seq \+ 1/);
  assert.match(app, /v-if="commandPaletteOpenRequest\.seq > 0"/);
  assert.match(host, /useCommandPalette\(/);
  assert.match(host, /watch\([\s\S]*props\.openRequest\.seq[\s\S]*openPalette\(\{ prefix: props\.openRequest\.prefix \}\)/);
});
