import assert from "node:assert/strict";
import test from "node:test";
import { Box, Connection, Document, Monitor } from "@element-plus/icons-vue";
import { pluginViewIcon } from "../src/services/plugin-view-icon.ts";

test("plugin view icons map to safe components with a default", () => {
  assert.equal(pluginViewIcon("document"), Document);
  assert.equal(pluginViewIcon(" connection "), Connection);
  assert.equal(pluginViewIcon("terminal"), Monitor);
  for (const icon of [undefined, "", " ", "unknown", "__proto__", "constructor"]) {
    assert.equal(pluginViewIcon(icon), Box);
  }
});

test("other Element Plus icons resolve lazily and are cached", () => {
  const calendar = pluginViewIcon("calendar");
  assert.notEqual(calendar, Box);
  assert.equal(pluginViewIcon(" calendar "), calendar);
  assert.equal(pluginViewIcon("Calendar"), Box);
});

test("the full icon set is loaded from the self-contained global build", async () => {
  const { readFile } = await import("node:fs/promises");
  const source = await readFile(new URL("../src/services/plugin-view-icon.ts", import.meta.url), "utf8");
  // Importing the shared barrel dynamically would merge it into a preloaded startup chunk.
  assert.match(source, /import\("@element-plus\/icons-vue\/global"\)/);
  assert.doesNotMatch(source, /import\("@element-plus\/icons-vue"\)/);
});
