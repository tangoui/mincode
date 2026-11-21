import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const [panel, service, styles, zh, en] = await Promise.all([
  readFile(new URL("../src/components/projects/ProjectGitPanel.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/services/git-service.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/styles/workspaces/project/git-panel.css", import.meta.url), "utf8"),
  readFile(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/i18n/locales/en.ts", import.meta.url), "utf8"),
]);

test("replaces the repository dropdown with a clickable list", () => {
  assert.doesNotMatch(panel, /:model-value="repoPath"/);
  assert.doesNotMatch(panel, /t\('git\.repositoryPlaceholder'\)/);
  assert.match(panel, /class="git-repository-list"/);
  assert.match(panel, /class="git-repository-row"/);
});

test("switches repositories by activating a whole row", () => {
  const row = panel.match(/<div\s+v-for="repository in repositories"(?<body>[\s\S]*?)>/);

  assert.ok(row?.groups?.body);
  assert.match(row.groups.body, /role="button"/);
  assert.match(row.groups.body, /tabindex="0"/);
  assert.match(row.groups.body, /@click="selectRepository\(repository\.path\)"/);
  assert.match(row.groups.body, /@keydown\.enter\.prevent="selectRepository\(repository\.path\)"/);
  assert.match(row.groups.body, /@keydown\.space\.prevent="selectRepository\(repository\.path\)"/);
  assert.match(row.groups.body, /'is-active': repository\.path === repoPath/);
});

test("hides the list when the project owns a single repository", () => {
  assert.match(panel, /<section v-if="showRepositoryList" class="git-repository-list">/);
  assert.match(
    panel,
    /const showRepositoryList = computed\(\(\) => repositories\.value\.length > 1\);/,
  );
});

test("renders branch, ahead/behind and change count per row", () => {
  assert.match(panel, /class="git-repository-row__branch"/);
  assert.match(panel, /class="git-repository-row__sync"/);
  assert.match(panel, /class="git-repository-row__changes"/);
  assert.match(panel, /repositoryChangeCount\(repository\.path\)/);
});

test("loads row summaries through one batched backend call", () => {
  assert.match(service, /"git_get_repository_summaries"/);
  assert.match(panel, /await getRepositorySummaries\(paths\)/);
  // 列表不可见时不该为看不见的行跑 N 次 git status。
  assert.match(
    panel,
    /if \(!showRepositoryList\.value \|\| !repositoriesOpen\.value \|\| !isDesktopRuntime\(\)\) return;/,
  );
});

test("styles the repository rows with a capped, scrollable height", () => {
  const scroll = styles.match(
    /\.project-git-console \.git-repository-list__scroll \.el-scrollbar__wrap\s*\{(?<body>[\s\S]*?)\n\}/,
  );
  const row = styles.match(/\.project-git-console \.git-repository-row\s*\{(?<body>[\s\S]*?)\n\}/);

  assert.match(scroll?.groups?.body ?? "", /max-height:/);
  assert.match(row?.groups?.body ?? "", /text-overflow: ellipsis|grid-template-columns:/);
});

test("indents repository rows under their own group header", () => {
  const list = styles.match(
    /\.project-git-console \.git-repository-list\s*\{(?<body>[\s\S]*?)\n\}/,
  );
  const row = styles.match(/\.project-git-console \.git-repository-row\s*\{(?<body>[\s\S]*?)\n\}/);

  // 与变更分组同一条左边距，行的高亮不能比文件行更贴近面板边缘。
  assert.match(list?.groups?.body ?? "", /padding: 0 4px 3px;/);
  // 折叠头是 7px 内边距 + 1em 箭头 + 4px 间距，仓库名要落在"存储库"标题下面。
  assert.match(row?.groups?.body ?? "", /padding: 0 5px 0 20px;/);
});

test("translates the repository list labels", () => {
  for (const locale of [zh, en]) {
    assert.match(locale, /"git\.repositories":/);
    assert.match(locale, /"git\.repositoryChanges":/);
    assert.match(locale, /"git\.repositorySync":/);
  }
});
