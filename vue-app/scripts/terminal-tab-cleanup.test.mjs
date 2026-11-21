import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { releasedTerminalPanes } from "../src/domain/terminal/tab-cleanup.ts";

test("panes no open terminal tab shows any more are released; moved or hidden panes are kept", () => {
  assert.deepEqual(releasedTerminalPanes(["a", "b", "c"], ["b", "c"]), ["a"]);
  assert.deepEqual(releasedTerminalPanes(["a", "b", "b"], []), ["a", "b"]);
  assert.deepEqual(releasedTerminalPanes(["a"], ["a", "d"]), []);
  assert.deepEqual(releasedTerminalPanes([], ["a"]), []);
});

test("closing terminal tabs closes their shells and drops their cached screens", async () => {
  const source = await readFile(new URL("../src/composables/use-terminal-tab-cleanup.ts", import.meta.url), "utf8");
  assert.match(source, /close_local_shell/);
  assert.match(source, /deleteTerminalSnapshot\(paneId\)/);
  assert.match(source, /flush: "post"/);
  const app = await readFile(new URL("../src/App.vue", import.meta.url), "utf8");
  assert.match(app, /useTerminalTabCleanup\(\)/);
});
