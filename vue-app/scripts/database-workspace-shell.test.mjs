import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  discardDatabaseWorkspaceScope,
  registerDatabaseWorkspaceStore,
  switchDatabaseWorkspaceScope,
  takePendingDatabaseWorkspaceScope,
} from "../src/stores/database-workspace-shell.ts";

test("scope switches before the store exists are handed to the store once", async () => {
  switchDatabaseWorkspaceScope("  ");
  assert.equal(takePendingDatabaseWorkspaceScope(), undefined);
  switchDatabaseWorkspaceScope("workspace:a");
  switchDatabaseWorkspaceScope(" workspace:b ");
  assert.equal(await discardDatabaseWorkspaceScope("workspace:a"), false);
  assert.equal(takePendingDatabaseWorkspaceScope(), "workspace:b");
  assert.equal(takePendingDatabaseWorkspaceScope(), undefined);
});

test("scope operations go to the created store", async () => {
  const calls = [];
  const store = {
    switchWorkspaceScope: (scopeId) => calls.push(["switch", scopeId]),
    discardWorkspaceScope: async (scopeId) => {
      calls.push(["discard", scopeId]);
      return true;
    },
  };
  registerDatabaseWorkspaceStore(() => store);
  switchDatabaseWorkspaceScope(" workspace:c ");
  assert.equal(await discardDatabaseWorkspaceScope("workspace:d"), true);
  assert.equal(takePendingDatabaseWorkspaceScope(), undefined);
  assert.deepEqual(calls, [["switch", "workspace:c"], ["discard", "workspace:d"]]);
});

test("startup shell files do not import the full database workspace store", async () => {
  for (const file of ["../src/App.vue", "../src/components/layout/AppTabBar.vue", "../src/composables/use-workspace-management.ts"]) {
    const source = await readFile(new URL(file, import.meta.url), "utf8");
    assert.doesNotMatch(source, /from "@\/stores\/database-workspace"/, file);
  }
});
