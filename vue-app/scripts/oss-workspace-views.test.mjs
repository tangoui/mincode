import assert from "node:assert/strict";
import test from "node:test";
import { workspaceViewLoaders } from "../src/router/workspace-views.ts";

for (const view of ["projects", "browser"]) {
  test(`does not register a loader for the removed ${view} view`, () => {
    assert.ok(!Object.keys(workspaceViewLoaders).includes(view));
  });
}
