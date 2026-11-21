import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const [styles, branchPanel, tagPanel] = await Promise.all([
  readFile(new URL("../src/styles/workspaces/git/repository-workspace.css", import.meta.url), "utf8"),
  readFile(new URL("../src/components/git/GitBranchPanel.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/git/GitTagPanel.vue", import.meta.url), "utf8"),
]);

test("trailing icon buttons keep their own width instead of stretching the row", () => {
  const action = styles.match(
    /\.git-inspector-row > :is\(button, \.el-button\)\.git-inspector-row__action\s*\{(?<body>[\s\S]*?)\n\}/,
  );

  // 没有这条规则，图标按钮会跟着 flex: 1 占掉半行，图标浮在一片空白正中间。
  assert.match(action?.groups?.body ?? "", /flex: none;/);
  assert.match(branchPanel, /class="git-inspector-row__action"\s+:icon="Delete"/);
  assert.match(tagPanel, /class="git-inspector-row__action"\s+:icon="Delete"/);
});

test("branch names align left and truncate inside their row", () => {
  const button = styles.match(
    /\.git-inspector-row > :is\(button, \.el-button\)\s*\{(?<body>[\s\S]*?)\n\}/,
  );
  const label = styles.match(
    /\.git-inspector-row > :is\(button, \.el-button\) > span\s*\{(?<body>[\s\S]*?)\n\}/,
  );

  // el-button 内部是居中的 inline-flex，只写 text-align 不生效。
  assert.match(button?.groups?.body ?? "", /justify-content: flex-start;/);
  assert.match(label?.groups?.body ?? "", /text-overflow: ellipsis;/);
  assert.match(branchPanel, /class="git-inspector-row__name"/);
});

test("long inspector panels scroll inside ElScrollbar instead of the drawer body", async () => {
  const view = await readFile(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8");
  // 抽屉本身滚动会露出浏览器原生滚动条；内容交给 ElScrollbar，抽屉 body 不滚动。
  assert.match(view, /'has-panel-scrollbar': \['branches', 'worktrees', 'tags', 'shelf', 'stash'\]\.includes\(gitInspector\)/);
  for (const kind of ["worktrees", "tags", "shelf", "stash"]) {
    assert.match(
      view,
      new RegExp(`<el-scrollbar\\s+v-else-if="gitInspector === '${kind}'"\\s+class="git-inspector-drawer__scrollbar"`),
      kind,
    );
  }
  assert.match(styles, /\.git-inspector-drawer\.has-panel-scrollbar \.el-drawer__body \{\s*overflow: hidden;/);
  assert.match(styles, /\.git-inspector-drawer\.has-panel-scrollbar :is\(\.git-branch-panel__scrollbar, \.git-inspector-drawer__scrollbar\) \{\s*height: 100%;/);
});
