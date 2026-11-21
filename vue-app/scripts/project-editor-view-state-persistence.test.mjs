import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const store = await readFile(new URL("../src/stores/local-project.ts", import.meta.url), "utf8");

test("cursor and scroll positions do not trigger the 300 ms full workspace save", () => {
  const deepWatch = store.match(/watch\(\s*\[([\s\S]*?)\],\s*schedulePersist,\s*\{ deep: true \}\s*\)/);
  assert.ok(deepWatch, "the persisted-state watcher must still exist");
  assert.doesNotMatch(deepWatch[1], /editorViewStates/);
  assert.match(deepWatch[1], /openPaths/);
});

test("view states are saved on their own slower timer that never postpones a pending save", () => {
  assert.match(store, /const VIEW_STATE_PERSIST_DELAY_MS = 2_000;/);
  assert.match(store, /watch\(editorViewStates, scheduleViewStatePersist\);/);
  const body = store.match(/function scheduleViewStatePersist\(\) \{([\s\S]*?)\n  \}/)?.[1] ?? "";
  assert.match(body, /if \(!hydrated\.value \|\| persistTimer\) return;/);
  assert.doesNotMatch(body, /clearTimeout/);
  assert.match(body, /setTimeout\(\(\) => void flushPersistence\(\), VIEW_STATE_PERSIST_DELAY_MS\)/);
  // Every update replaces the map, which the shallow watch observes.
  assert.match(store, /editorViewStates\.value = \{ \.\.\.editorViewStates\.value, \[path\]: sanitized \};/);
});
