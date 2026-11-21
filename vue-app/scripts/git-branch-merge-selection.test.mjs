import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const read = (path) => readFile(new URL(path, import.meta.url), "utf8");
const [panel, styles, zh, en] = await Promise.all([
  read("../src/components/git/GitBranchPanel.vue"),
  read("../src/styles/workspaces/git/repository-workspace.css"),
  read("../src/i18n/locales/zh.ts"),
  read("../src/i18n/locales/en.ts"),
]);

test("合并分支从已有本地和远程分支中选择", () => {
  assert.match(panel, /const mergeLocalBranches = computed\([\s\S]*?!branch\.is_current/);
  assert.match(panel, /const mergeRemoteBranches = computed\([\s\S]*?branch\.is_remote/);
  assert.match(panel, /<el-dialog[\s\S]*?append-to-body/);
  assert.match(panel, /<el-select[\s\S]*?v-model="selectedMergeBranch"[\s\S]*?filterable/);
  assert.match(panel, /<el-option-group[\s\S]*?t\('git\.localBranches'\)/);
  assert.match(panel, /<el-option-group[\s\S]*?t\('git\.remoteBranches'\)/);
  assert.doesNotMatch(panel, /async function mergeBranch\(\) \{[\s\S]*?promptValue\(/);
});

test("合并分支只提交有效选择并在成功后关闭弹窗", () => {
  assert.match(panel, /if \(!mergeBranchNames\.value\.includes\(branchName\)\) return;/);
  assert.match(panel, /await run\("git_merge_branch", \{ branchName \}\)/);
  assert.match(panel, /if \(await run\("git_merge_branch", \{ branchName \}\)\) \{[\s\S]*?mergeDialogVisible\.value = false/);
  assert.match(panel, /:disabled="!selectedMergeBranch"/);
  assert.match(panel, /:disabled="operationPending \|\| loading \|\| mergeState\.active \|\| mergeBranchNames\.length === 0"/);
});

test("合并分支选择器具有完整文案与全宽布局", () => {
  assert.match(zh, /"git\.mergeBranchPrompt": "选择要合并到当前分支 \{branch\} 的已有分支。"/);
  assert.match(zh, /"git\.mergeBranchPlaceholder": "选择分支"/);
  assert.match(en, /"git\.mergeBranchPlaceholder": "Select a branch"/);
  assert.match(styles, /\.git-merge-branch-dialog__select \{[\s\S]*?width: 100%;/);
});
