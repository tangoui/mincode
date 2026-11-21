import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const [selectionDialog, storedFileList, shelfPanel, stashPanel, projectGitPanel, gitService, zh, en] =
  await Promise.all([
    readFile(new URL("../src/components/git/GitChangeSelectionDialog.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/components/git/GitStoredFileList.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/components/git/GitShelfPanel.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/components/git/GitStashPanel.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/components/projects/ProjectGitPanel.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/services/git-service.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/i18n/locales/en.ts", import.meta.url), "utf8"),
  ]);

test("selects changed files before creating shelves and stashes", () => {
  assert.match(selectionDialog, /invokeBackend<GitProjectStatus>\("git_get_status"/);
  assert.match(selectionDialog, /selectedPaths\.value = files\.value\.map\(\(file\) => file\.path\)/);
  assert.match(selectionDialog, /confirm: \[payload: \{ message: string; paths: string\[\] \}\]/);
  assert.match(selectionDialog, /<el-checkbox-group v-model="selectedPaths"/);
  assert.match(selectionDialog, /:disabled="loading \|\| !message\.trim\(\) \|\| selectedPaths\.length === 0"/);

  for (const [panel, command] of [
    [shelfPanel, "git_shelf_create"],
    [stashPanel, "git_stash_push"],
  ]) {
    assert.match(panel, /<GitChangeSelectionDialog/);
    assert.ok(panel.includes(`run("${command}", payload)`));
  }
});

test("expands stored entries to show their file lists", () => {
  assert.match(shelfPanel, /@click="toggleFiles\(shelf\)"/);
  assert.match(shelfPanel, /<GitStoredFileList v-if="expanded\.has\(shelf\.id\)" :files="shelf\.files"/);
  assert.match(stashPanel, /@click="toggleFiles\(stash\)"/);
  assert.match(stashPanel, /<GitStoredFileList v-if="expanded\.has\(stash\.oid\)" :files="stash\.files"/);
  assert.match(storedFileList, /v-for="file in files"/);
  assert.match(storedFileList, /file\.old_path \? `\$\{file\.old_path\} → \$\{file\.path\}` : file\.path/);
  assert.match(gitService, /interface GitStashEntry \{[\s\S]*?files: GitFileEntry\[\];/);
  assert.match(gitService, /interface GitShelfEntry \{[\s\S]*?files: GitFileEntry\[\];/);
});

test("opens the stash file picker from the quick stash action after mounting the panel", () => {
  assert.match(projectGitPanel, /const stashPanel = ref<InstanceType<typeof GitStashPanel> \| null>\(null\)/);
  assert.match(
    projectGitPanel,
    /async function pushStash\(\) \{\s*changeTab\.value = "stash";\s*await nextTick\(\);\s*stashPanel\.value\?\.openCreate\(\);\s*\}/,
  );
  assert.match(projectGitPanel, /<GitStashPanel\s+v-else\s+ref="stashPanel"/);
});

test("provides localized labels for selection and stored file details", () => {
  for (const source of [zh, en]) {
    for (const key of [
      "git.shelfSelectionDescription",
      "git.shelfSelectedFiles",
      "git.stashSelectionDescription",
      "git.stashSelectedFiles",
      "git.selectAllFiles",
      "git.selectedFiles",
      "git.storedFileCount",
      "git.noChangesToStore",
      "git.noStoredFileDetails",
    ]) {
      assert.ok(source.includes(`"${key}"`), `${key} is missing`);
    }
  }
});
