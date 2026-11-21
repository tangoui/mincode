import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const [view, panel, styles] = await Promise.all([
  readFile(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/projects/ProjectGitPanel.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/styles/workspaces/project/workspace-chrome.css", import.meta.url), "utf8"),
]);

test("resource toolbar reuses the mounted Git panel for quick actions", () => {
  assert.match(view, /class="project-explorer-toolbar__git-group"/);
  assert.match(view, /runResourceGitAction\('refresh'\)/);
  assert.match(view, /runResourceGitAction\('stage-all'\)/);
  assert.match(view, /runResourceGitAction\('pull'\)/);
  assert.match(view, /runResourceGitAction\('push'\)/);
  assert.match(view, /@command="runResourceGitAction"/);
  assert.match(view, /<ProjectGitPanel[\s\S]*?v-show="sideMode === 'git'"/);
  assert.doesNotMatch(view, /<ProjectGitPanel\s+v-else/);
});

test("resource Git actions follow the deepest repository owning the selected tree node", () => {
  assert.match(view, /const resourceGitSelection = ref<WorkspaceTreeNode \| null>/);
  assert.match(view, /repositoryPaths[\s\S]*?sort\(\(a, b\) => b\.length - a\.length\)/);
  assert.match(view, /await gitPanel\.value\.selectRepository\(targetPath\)/);
  assert.match(panel, /const quickActionState = computed/);
  assert.match(panel, /async function runQuickAction/);
  assert.match(panel, /defineExpose\(\{[\s\S]*?selectRepository[\s\S]*?runQuickAction[\s\S]*?quickActionState/);
});

test("resource Git menu keeps the source-control extended actions and destructive guard", () => {
  for (const command of [
    "fetch",
    "unstage-all",
    "manage-branches",
    "show-graph",
    "manage-worktrees",
    "create-branch",
    "rename-branch",
    "manage-tags",
    "undo-last-commit",
    "stash",
    "manage-stash",
    "interactive-rebase",
    "discard-all",
  ]) {
    assert.match(view, new RegExp(`command="${command}"`));
  }
  assert.match(view, /command="discard-all"[\s\S]*?:disabled="!resourceGitState\.totalChanges"/);
  assert.match(styles, /\.project-explorer-toolbar__git-group\s*\{/);
});
