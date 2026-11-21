import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const [view, styles] = await Promise.all([
  readFile(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/styles/workspaces/project/workspace.css", import.meta.url), "utf8"),
]);

test("project tree loads repository branches through the existing batch APIs", () => {
  assert.match(view, /getRepositorySummaries/);
  assert.match(view, /async function refreshRepositoryBranches/);
  assert.match(view, /listProjectRepositories\(entry\.root\)/);
  assert.match(view, /getRepositorySummaries\(paths\)/);
  assert.match(view, /repositoryBranchForPath/);
});

test("repository directories render a compact ellipsized current branch", () => {
  assert.match(view, /repositoryBranch\?: string/);
  assert.match(view, /treeNodeBranch\(data\)/);
  assert.match(view, /<ForkSpoon\s*\/>/);
  assert.match(view, /class="project-tree-node__branch"/);
  assert.match(styles, /\.project-tree-node__branch > span\s*\{[\s\S]*?text-overflow:\s*ellipsis/);
});

test("repository branches use the row's available width and align right", () => {
  assert.match(styles, /\.project-tree-node\s*\{[^}]*flex:\s*1;/);
  assert.match(styles, /\.project-tree-node > span\s*\{[^}]*min-width:\s*0;/);
  const branch = styles.match(/\.project-tree-node__branch\s*\{([^}]*)\}/)?.[1];
  assert.ok(branch);
  assert.match(branch, /flex:\s*none;/);
  assert.match(branch, /max-width:\s*50%;/);
  assert.match(branch, /margin-left:\s*auto;/);
  assert.match(view, /:title="treeNodeBranch\(data\)"/);
  assert.match(styles, /\.project-tree-node__branch \+ \.project-tree-node__stats\s*\{\s*margin-left:\s*0;/);
});
