import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import {
  applyGitConflictChoice,
  hasGitConflictMarkers,
  parseGitConflictBlocks,
} from "../src/domain/git/conflict-resolution.ts";

const read = (path) => readFile(new URL(path, import.meta.url), "utf8");
const [panel, resolver, service, zh, backend, gitOps] = await Promise.all([
  read("../src/components/git/GitBranchPanel.vue"),
  read("../src/components/git/GitConflictResolver.vue"),
  read("../src/services/git-service.ts"),
  read("../src/i18n/locales/zh.ts"),
  read("../../backend/git/git_ops2.go"),
  read("../../backend/git/git_ops.go"),
]);

const raw = [
  "before",
  "<<<<<<< HEAD",
  "current",
  "||||||| base",
  "ancestor",
  "=======",
  "incoming",
  ">>>>>>> feature",
  "after",
  "",
].join("\n");

test("解析并逐块应用 Git diff3 冲突选择", () => {
  const blocks = parseGitConflictBlocks(raw);
  assert.deepEqual(blocks, [{
    start: 1,
    end: 7,
    ours: "current",
    base: "ancestor",
    theirs: "incoming",
  }]);
  assert.equal(applyGitConflictChoice(raw, 0, "ours"), "before\ncurrent\nafter\n");
  assert.equal(applyGitConflictChoice(raw, 0, "theirs"), "before\nincoming\nafter\n");
  assert.equal(applyGitConflictChoice(raw, 0, "both"), "before\ncurrent\nincoming\nafter\n");
  assert.equal(hasGitConflictMarkers(raw), true);
  assert.equal(hasGitConflictMarkers(applyGitConflictChoice(raw, 0, "both")), false);

  const twice = `${raw}middle\n${raw}`;
  const afterFirst = applyGitConflictChoice(twice, 0, "ours");
  assert.equal(parseGitConflictBlocks(afterFirst).length, 1);
  assert.equal(hasGitConflictMarkers(applyGitConflictChoice(afterFirst, 0, "theirs")), false);
});

test("分支面板在真实合并冲突后刷新并打开解决器", () => {
  assert.match(panel, /getGitMergeState\(request\.target\)/);
  assert.match(panel, /catch \(error\) \{[\s\S]*?await refresh\(\);[\s\S]*?emit\("changed"\)/);
  assert.match(panel, /if \(mergeState\.value\.active\)[\s\S]*?conflictResolverVisible\.value = true/);
  assert.match(panel, /<GitConflictResolver[\s\S]*?:repo-path="repoPath"/);
  assert.match(panel, /v-if="mergeState\.active"[\s\S]*?git\.conflict\.openResolver/);
  assert.match(panel, /if \(totalChanges\.value > 0\)[\s\S]*?git\.conflict\.cleanWorktreeRequired/);
});

test("解决器提供三方内容、逐块选择、继续和中止合并", () => {
  assert.match(resolver, /const sourceNames = \["ours", "base", "theirs"\] as const/);
  assert.match(resolver, /applyChoice\(index, 'ours'\)/);
  assert.match(resolver, /applyChoice\(index, 'theirs'\)/);
  assert.match(resolver, /applyChoice\(index, 'both'\)/);
  assert.match(resolver, /resolveGitConflict\(props\.repoPath, resolvedPath, result\.value\)/);
  // 保存目标只能是成功加载的文件；加载失败时不得沿用上一个文件的正文。
  assert.match(resolver, /const resolvedPath = loadedPath\.value;\s*if \(!canSave\.value \|\| !resolvedPath\) return;/);
  assert.match(resolver, /loadedPath\.value === selectedPath\.value/);
  assert.match(resolver, /:disabled="!canSave \|\| blocks\.length > 0"/);
  assert.match(resolver, /function selectConflict\(path: string\) \{\s*selectedPath\.value = path;\s*clearLoadedConflict\(\);/);
  assert.doesNotMatch(resolver, /continueGitMerge|git_continue_merge/);
  assert.match(resolver, /function finishResolution\(\)[\s\S]*?emit\("close"\)/);
  assert.match(resolver, /abortGitMerge\(props\.repoPath\)/);
  assert.match(resolver, /hasGitConflictMarkers\(result\.value\)/);
  assert.match(zh, /"git\.conflict\.abortConfirm"/);
});

test("前后端注册冲突读取、解决和中止命令，但不提供自动提交命令", () => {
  for (const command of ["git_get_merge_state", "git_resolve_conflict", "git_abort_merge"]) {
    assert.ok(service.includes(`"${command}"`), `service missing ${command}`);
    assert.ok(backend.includes(`"${command}"`), `backend missing ${command}`);
  }
  assert.ok(!service.includes("git_continue_merge"));
  assert.ok(!backend.includes("git_continue_merge"));
  assert.match(backend, /gitConflictStageContent\(a\.RepoPath, rel, 1\)/);
  assert.match(backend, /gitConflictStageContent\(a\.RepoPath, rel, 2\)/);
  assert.match(backend, /gitConflictStageContent\(a\.RepoPath, rel, 3\)/);
});


test("分支合并只准备本地更改且不自动提交或推送", () => {
  assert.match(gitOps, /"merge", "--no-ff", "--no-commit", a\.BranchName/);
  const handler = gitOps.match(/func gitMergeBranchHandler\([\s\S]*?\n\}/)?.[0] ?? "";
  assert.doesNotMatch(handler, /git push|"push"|gitCommitHandler|"commit"/);
  assert.match(handler, /手动提交/);
  assert.match(panel, /readyForManualCommitTitle/);
  assert.match(panel, /v-if="mergeState\.conflicts\.length > 0"/);
});
