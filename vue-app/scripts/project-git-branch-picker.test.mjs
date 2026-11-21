import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const panel = await readFile(
  new URL("../src/components/projects/ProjectGitPanel.vue", import.meta.url),
  "utf8",
);

test("groups local and remote branches with unambiguous option values", () => {
  assert.match(panel, /const remoteBranches = computed\([\s\S]*?branch\.is_remote/);
  assert.match(panel, /<el-option-group[^>]*:label="t\('git\.localBranches'\)"/);
  assert.match(panel, /<el-option-group[^>]*:label="t\('git\.remoteBranches'\)"/);
  assert.match(panel, /branchOptionValue\(branch\)/);
  assert.match(panel, /selectedBranchValue/);
});

test("marks remote selections when checking out a branch", () => {
  assert.match(panel, /parseBranchOptionValue\(optionValue\)/);
  assert.match(panel, /remoteBranch:\s*selection\.kind === "remote"/);
});
