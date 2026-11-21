import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

// Exercise the store action itself without loading native persistence services.
const source = readFileSync(new URL("../src/stores/local-project.ts", import.meta.url), "utf8");
const action = source.match(/  function rememberProject\(root: string\) \{([\s\S]*?)\n  \}/)?.[1];
assert.ok(action);
function remember(projects, root) {
  runInNewContext(`(function(root) {${action}})(root)`, {
    projects, root, normalizeRoot: (value) => value.trim(),
    projectName: (value) => value.split("/").at(-1), MAX_RECENT_PROJECTS: 12,
  });
}

test("activating the second root preserves order and array identity", () => {
  const entries = [{ root: "/im", name: "im" }, { root: "/email", name: "Custom email" }];
  const projects = { value: entries };
  remember(projects, "/email");
  assert.equal(projects.value, entries);
  assert.equal(projects.value[1].name, "Custom email");
  remember(projects, "/im");
  assert.equal(projects.value, entries);
});

test("new roots are still remembered once and blank roots are ignored", () => {
  const projects = { value: [{ root: "/im", name: "im" }] };
  remember(projects, " /email ");
  assert.equal(projects.value.length, 2);
  assert.equal(projects.value[0].root, "/email");
  const entries = projects.value;
  remember(projects, "/email");
  remember(projects, " ");
  assert.equal(projects.value, entries);
});
