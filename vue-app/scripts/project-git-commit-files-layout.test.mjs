import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";

const [styles, panel, projectView] = await Promise.all([
  readFile(new URL("../src/styles/workspaces/project/git-panel.css", import.meta.url), "utf8"),
  readFile(new URL("../src/components/projects/ProjectGitPanel.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8"),
]);

test("lays out each commit file row as a full-width grid (rows sit inside the windowed group)", () => {
  const rule = styles.match(
    /\.project-git-console \.project-git-commit-file\s*\{(?<body>[\s\S]*?)\n\}/,
  );

  assert.ok(rule?.groups?.body);
  assert.match(rule.groups.body, /display:\s*grid;/);
  assert.match(rule.groups.body, /width:\s*100%;/);
  assert.match(rule.groups.body, /grid-template-columns:\s*max-content minmax\(0, 1fr\);/);
});

test("keeps the project selector without the redundant repository overview card", () => {
  assert.doesNotMatch(panel, /class="git-context-head"/);
  assert.doesNotMatch(panel, /class="git-context-head__identity"/);
  assert.match(panel, /class="git-context-switchers"/);
  assert.match(panel, /<div class="git-context-switcher">\s*<span>\{\{ t\("git\.project"\) \}\}<\/span>/);
  assert.doesNotMatch(panel, /<div v-if="projects\.length > 1" class="git-context-switcher">/);
});

test("keeps the branch selector and icon actions on one row", () => {
  const commandBar = styles.match(
    /\.project-git-console--ide \.git-command-bar--ide\s*\{(?<body>[\s\S]*?)\n\}/,
  );
  const branchRow = styles.match(
    /\.project-git-console--ide \.git-command-bar__branch\s*\{(?<body>[\s\S]*?)\n\}/,
  );
  const actions = styles.match(
    /\.project-git-console--ide \.git-command-bar__actions\s*\{(?<body>[\s\S]*?)\n\}/,
  );

  assert.match(commandBar?.groups?.body ?? "", /flex-wrap:\s*nowrap;/);
  assert.match(branchRow?.groups?.body ?? "", /width:\s*50%;/);
  assert.match(branchRow?.groups?.body ?? "", /flex:\s*0 0 50%;/);
  assert.match(actions?.groups?.body ?? "", /min-width:\s*0;/);
  assert.match(actions?.groups?.body ?? "", /flex:\s*1 1 auto;/);
  assert.match(actions?.groups?.body ?? "", /justify-content:\s*flex-end;/);
});

test("moves toolbar actions into the overflow menu as the panel narrows", () => {
  assert.match(panel, /const commandBarWidth = ref\(Number\.POSITIVE_INFINITY\)/);
  assert.match(panel, /new ResizeObserver\(/);
  assert.match(panel, /v-if="showToolbarUnstage"[\s\S]*?:icon="Remove"/);
  assert.match(panel, /v-if="!showToolbarUnstage"[\s\S]*?command="unstage-all"/);

  for (const [visible, command] of [
    ["showToolbarBranches", "manage-branches"],
    ["showToolbarGraph", "show-graph"],
    ["showToolbarWorktrees", "manage-worktrees"],
  ]) {
    assert.match(panel, new RegExp(String.raw`v-if="${visible}"[\s\S]*?@click="handleActionCommand\('${command}'\)"`));
    assert.match(panel, new RegExp(String.raw`v-if="extendedActions && !${visible}"[\s\S]*?command="${command}"`));
  }
});

test("promotes branches, commit graph, and worktrees to toolbar icons", () => {
  const promoted = {
    branches: "manage-branches",
    graph: "show-graph",
    worktrees: "manage-worktrees",
  };

  for (const [inspector, command] of Object.entries(promoted)) {
    assert.match(panel, new RegExp(`data-git-inspector="${inspector}"`));
    assert.ok(panel.includes(`@click="handleActionCommand('${command}')"`));
  }
  assert.match(panel, /class="git-command-bar__worktrees"[\s\S]*?:icon="FolderOpened"/);
});

test("keeps only drawer-based advanced inspectors in the more menu", () => {
  for (const command of ["manage-tags", "interactive-rebase"]) {
    const occurrences = panel.match(new RegExp(`command="${command}"`, "g")) ?? [];
    assert.equal(occurrences.length, 1, `${command} must appear once, found ${occurrences.length}`);
  }
  for (const command of ["manage-shelf", "stash", "manage-stash"]) {
    assert.doesNotMatch(panel, new RegExp(`<el-dropdown-item[^>]*command="${command}"`));
  }
});

test("keeps commit drafts and AI generation isolated by repository", () => {
  assert.match(panel, /const repositoryCommitDrafts = reactive<Record<string, RepositoryCommitDraft>>/);
  assert.match(panel, /const commitMessage = computed\(\{[\s\S]*?repositoryCommitDraft\(repoPath\.value\)\.message = message/);
  assert.doesNotMatch(
    panel,
    /function clearRepositoryState\(\)[\s\S]*?commitMessage\.value = "";[\s\S]*?\n\}/,
  );
  assert.match(
    panel,
    /async function generateCommitMessage\(\)[\s\S]*?const targetPath = repoPath\.value;[\s\S]*?const draft = repositoryCommitDraft\(targetPath\);[\s\S]*?generateAiCommitMessage\(\{[\s\S]*?draft\.message = message;/,
  );
  assert.match(panel, /const generatingCommitMessage = computed\(/);
  assert.match(panel, /draft\.generation === generation && repoPath\.value === targetPath/);
  assert.match(
    panel,
    /async function submitCommit\(targetPath = repoPath\.value\)[\s\S]*?const draft = repositoryCommitDraft\(targetPath\)[\s\S]*?runGit\(command, \{ message \}, targetPath\)/,
  );
});

test("keeps AI generation clickable and explains the staged-file requirement", () => {
  assert.match(panel, /async function generateCommitMessage\(\)[\s\S]*?ElMessage\.warning\(t\("git\.aiGenerateRequiresStaged"\)\)/);
  assert.match(panel, /:title="generatingCommitMessage \? t\('common\.cancel'\) : t\('git\.aiGenerateCommitMessage'\)"[\s\S]*?@click="generateCommitMessage"/);
  assert.doesNotMatch(panel, /:disabled="generatingCommitMessage/);
});

test("keeps Pull compact while reporting its result", () => {
  assert.match(
    panel,
    /async function pullRepository\(\)[\s\S]*?invokeBackend<GitSyncResult>\("git_pull", \{[\s\S]*?repoPath: repoPath\.value,[\s\S]*?await refresh\(\);[\s\S]*?ElMessage\.success\(result\?\.message \|\| t\("git\.pull"\)\)/,
  );
  assert.match(
    panel,
    /ElMessage\.error\(t\("git\.pullFailed", \{ reason: nativeErrorMessage\(error\) \}\)\)/,
  );
  assert.match(
    panel,
    /:icon="ArrowDown"[\s\S]*?:title="t\('git\.pull'\)"[\s\S]*?:aria-label="t\('git\.pull'\)"[\s\S]*?@click="pullRepository"[\s\S]*?\/>/,
  );
});

test("mounts every advanced inspector panel in the local project view", () => {
  assert.match(projectView, /<ProjectGitPanel[\s\S]*?\n\s+extended-actions\n/);
  assert.doesNotMatch(panel, /worktreeAction/);
  for (const component of [
    "GitWorktreePanel",
    "GitBranchPanel",
    "GitTagPanel",
    "GitStashPanel",
    "GitRebasePanel",
    "GitCommitGraph",
  ]) {
    assert.match(
      projectView,
      new RegExp(
        `import ${component} from "@/components/git/|const ${component} = defineAsyncComponent\\(\\(\\) => import\\("@/components/git/`,
      ),
    );
    assert.match(projectView, new RegExp(`<${component}`));
  }
});


test("AI commit message generation streams, cancels providers, and stops silent providers", () => {
  // 30 s without any stream data (reasoning counts) stops a run; thinking models get up to 3 minutes overall.
  assert.match(panel, /const COMMIT_MESSAGE_IDLE_TIMEOUT_MS = 30_000/);
  assert.match(panel, /const COMMIT_MESSAGE_GENERATION_TIMEOUT_MS = 180_000/);
  assert.match(panel, /commitMessageAbortControllers\.get\(targetPath\)\?\.abort\(\)/);
  assert.match(panel, /generateAiCommitMessage\(\{[\s\S]*?signal: controller\.signal,[\s\S]*?idleTimeoutMs: COMMIT_MESSAGE_IDLE_TIMEOUT_MS,[\s\S]*?onDelta:/);
  assert.match(panel, /if \(!receivedText\) \{[\s\S]*?draft\.message = "";[\s\S]*?draft\.message \+= delta/);
  assert.match(panel, /setTimeout\(\(\) => controller\.abort\(\), COMMIT_MESSAGE_GENERATION_TIMEOUT_MS\)/);
  assert.match(panel, /watch\(repoPath, \(_current, previous\) => \{\s*cancelCommitMessageGeneration\(previous\);/);
  assert.match(panel, /onDeactivated\(\(\) => \{[\s\S]*?cancelAllCommitMessageGenerations\(\)/);
  assert.doesNotMatch(panel, /invokeBackend<string>\("ai_generate_commit_message"/);
});

test("provides a context menu for changed files with state-aware actions", () => {
  assert.match(panel, /import AppContextMenu from "@\/components\/ui\/AppContextMenu\.vue"/);
  assert.match(panel, /@contextmenu\.prevent="openFileContextMenu\(\$event, file, group\)"/);
  assert.match(panel, /<AppContextMenu[\s\S]*?popper-class="git-file-context-menu"/);
  assert.match(panel, /v-if="fileContextMenu\.group === 'staged'"[\s\S]*?@click="unstageContextFile"/);
  assert.match(panel, /<el-dropdown-item v-else[\s\S]*?@click="stageContextFile"/);
  assert.match(panel, /v-if="fileContextMenu\.group !== 'staged'"[\s\S]*?@click="discardContextFile"/);
});

test("keeps diff opening separate from direct file opening", () => {
  assert.match(panel, /"open-worktree-file": \[payload: \{ repoPath: string; entry: GitFileEntry \}\]/);
  assert.match(panel, /emit\("open-worktree-file", \{ repoPath: repoPath\.value, entry/);
  assert.match(projectView, /@open-file="openGitFile"/);
  assert.match(projectView, /@open-worktree-file="openGitWorktreeFile"/);
  assert.match(projectView, /async function openGitWorktreeFile\([\s\S]*?await openFile\(entry\.path, undefined, repoPath\)/);
});

test("offers changed-file navigation and path copying without new Git commands", () => {
  assert.match(panel, /local_fs_show_in_file_manager/);
  assert.match(panel, /copyText\(entry\.path\)/);
  assert.match(panel, /t\("git\.fileContext\.openChanges"\)/);
  assert.match(panel, /t\("git\.fileContext\.openFile"\)/);
  assert.match(panel, /t\("git\.fileContext\.reveal"\)/);
  assert.match(panel, /t\("git\.fileContext\.copyRelativePath"\)/);
});

test("keeps shelf and stash management inline with changes and history", () => {
  assert.match(panel, /type ChangeTab = "changes" \| "history" \| "shelf" \| "stash"/);
  assert.match(panel, /t\("git\.inspector\.shelf"\), value: "shelf"/);
  assert.match(panel, /t\("git\.inspector\.stash"\), value: "stash"/);
  assert.match(panel, /case "manage-shelf":[\s\S]*?changeTab\.value = "shelf"/);
  assert.match(panel, /case "manage-stash":[\s\S]*?changeTab\.value = "stash"/);
  assert.match(panel, /<GitShelfPanel[\s\S]*?changeTab === 'shelf'/);
  assert.match(panel, /<GitStashPanel[\s\S]*?:show-create-action="false"/);
  assert.match(panel, /:title="t\('git\.stashTitle'\)"[\s\S]*?@click="pushStash"/);
  assert.doesNotMatch(panel, /<el-dropdown-item[^>]*command="(?:manage-shelf|stash|manage-stash)"/);
  assert.match(projectView, /import GitShelfPanel from/);
  assert.match(projectView, /gitInspector === 'shelf'/);
});
