import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { workspaceViewLoaders } from "../src/router/workspace-views.ts";

const sourceRoot = fileURLToPath(new URL("../src/", import.meta.url));

test("the frontend contains no plugin store, UI, or workspace view references", () => {
  const forbidden = /@\/stores\/plugin-|@\/plugin-ui|["'](?:plugin-view|service-result)["']/;
  const offenders = readdirSync(sourceRoot, { recursive: true, withFileTypes: true })
    .filter((entry) => entry.isFile() && /\.(?:ts|vue)$/.test(entry.name))
    .map((entry) => join(entry.parentPath, entry.name))
    .filter((file) => forbidden.test(readFileSync(file, "utf8")))
    .map((file) => relative(sourceRoot, file))
    .sort();

  assert.deepEqual(offenders, []);
});

test("the workspace router exposes no plugin views", () => {
  const removed = ["plugins", "plugin-view", "service-result"];
  assert.deepEqual(Object.keys(workspaceViewLoaders).filter((view) => removed.includes(view)), []);
});
