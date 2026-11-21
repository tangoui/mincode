import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";
import {
  buildWorktreeBinding,
  formatWorktreeChanges,
  groupProjectWorktrees,
  hasWorktreeChanges,
  resolveOwningRepoPath,
  workspaceIdsBoundTo,
} from "../src/domain/git/worktree.ts";
import { buildBatchWorktreePlan } from "../src/domain/git/batch-worktree.ts";

function worktreeEntry(path, overrides = {}) {
  return {
    path,
    is_main: false,
    is_current: false,
    is_bare: false,
    is_detached: false,
    is_locked: false,
    is_prunable: false,
    ...overrides,
  };
}

function workspace(id, worktreePath) {
  return {
    id,
    name: id,
    createdAt: 1,
    updatedAt: 1,
    ...(worktreePath ? { worktree: { repoPath: "/repo", worktreePath } } : {}),
  };
}

test("builds a binding carrying the branch when present", () => {
  const binding = buildWorktreeBinding("/repo", worktreeEntry("/repo/.worktrees/feat", { branch: "feat" }));
  assert.deepEqual(binding, {
    repoPath: "/repo",
    worktreePath: "/repo/.worktrees/feat",
    branch: "feat",
  });
});

test("omits the branch for a detached worktree", () => {
  const binding = buildWorktreeBinding("/repo", worktreeEntry("/repo/.worktrees/detached"));
  assert.deepEqual(binding, { repoPath: "/repo", worktreePath: "/repo/.worktrees/detached" });
});

test("rejects a binding missing either path", () => {
  assert.equal(buildWorktreeBinding("", worktreeEntry("/repo/.worktrees/feat")), undefined);
  assert.equal(buildWorktreeBinding("/repo", worktreeEntry("  ")), undefined);
});

test("resolves the owning repository from the main worktree", () => {
  const entries = [
    worktreeEntry("/repo/.worktrees/feat", { branch: "feat" }),
    worktreeEntry("/repo", { is_main: true, branch: "main" }),
  ];
  assert.equal(resolveOwningRepoPath(entries, "/repo/.worktrees/feat"), "/repo");
});

test("falls back to the opened root when no main worktree is reported", () => {
  assert.equal(resolveOwningRepoPath([], "/repo/.worktrees/feat"), "/repo/.worktrees/feat");
});

test("collects every workspace bound to a removed worktree", () => {
  const workspaces = [
    workspace("workspace:a", "/repo/.worktrees/feat"),
    workspace("workspace:b"),
    workspace("workspace:c", "/repo/.worktrees/feat"),
    workspace("workspace:d", "/repo/.worktrees/other"),
  ];
  assert.deepEqual(workspaceIdsBoundTo(workspaces, "/repo/.worktrees/feat"), [
    "workspace:a",
    "workspace:c",
  ]);
});

test("matches no workspace for an empty worktree path", () => {
  const workspaces = [workspace("workspace:a", "/repo/.worktrees/feat")];
  assert.deepEqual(workspaceIdsBoundTo(workspaces, "   "), []);
});

test("formats change stats with explicit signs", () => {
  assert.deepEqual(formatWorktreeChanges({ additions: 165, deletions: 133 }), {
    additions: "+165",
    deletions: "-133",
  });
  assert.deepEqual(formatWorktreeChanges({ additions: 0, deletions: 0 }), {
    additions: "+0",
    deletions: "-0",
  });
});

test("distinguishes unavailable stats from a clean worktree", () => {
  assert.equal(formatWorktreeChanges(undefined), undefined);
  assert.equal(hasWorktreeChanges(undefined), false);
  assert.equal(hasWorktreeChanges({ additions: 0, deletions: 0 }), false);
  assert.equal(hasWorktreeChanges({ additions: 0, deletions: 2 }), true);
});

function project(root, name = root.split("/").pop()) {
  return { root, name };
}

test("nests a listed worktree under its listed main repository", () => {
  const main = worktreeEntry("/repo", { is_main: true, branch: "master" });
  const linked = worktreeEntry("/repo/.worktrees/feat", {
    branch: "feat/x",
    changes: { additions: 12, deletions: 3 },
  });
  const grouping = groupProjectWorktrees(
    [project("/repo", "min"), project("/repo/.worktrees/feat", "wt-binding")],
    new Map([
      ["/repo", [main, linked]],
      ["/repo/.worktrees/feat", [main, linked]],
    ])
  );

  assert.deepEqual(grouping.roots, [{ root: "/repo", name: "min" }]);
  assert.deepEqual(grouping.childrenByRoot.get("/repo"), [
    {
      root: "/repo/.worktrees/feat",
      name: "wt-binding",
      branch: "feat/x",
      changes: { additions: 12, deletions: 3 },
      isLocked: false,
      isPrunable: false,
    },
  ]);
});

test("keeps a worktree at the top level when its main repository is not listed", () => {
  const main = worktreeEntry("/elsewhere/repo", { is_main: true });
  const linked = worktreeEntry("/repo/.worktrees/feat", { branch: "feat/x" });
  const grouping = groupProjectWorktrees(
    [project("/repo/.worktrees/feat")],
    new Map([["/repo/.worktrees/feat", [main, linked]]])
  );

  assert.deepEqual(grouping.roots, [project("/repo/.worktrees/feat")]);
  assert.equal(grouping.childrenByRoot.size, 0);
});

test("does not nest two worktrees that share an unlisted main repository", () => {
  const main = worktreeEntry("/elsewhere/repo", { is_main: true });
  const entries = [
    main,
    worktreeEntry("/wt/a", { branch: "a" }),
    worktreeEntry("/wt/b", { branch: "b" }),
  ];
  const grouping = groupProjectWorktrees(
    [project("/wt/a"), project("/wt/b")],
    new Map([
      ["/wt/a", entries],
      ["/wt/b", entries],
    ])
  );

  assert.deepEqual(grouping.roots, [project("/wt/a"), project("/wt/b")]);
  assert.equal(grouping.childrenByRoot.size, 0);
});

// worktree 目录常落在主仓库内部，路径前缀匹配会把普通子目录误判成 worktree。
test("never nests by path prefix when git reports no worktree relationship", () => {
  const grouping = groupProjectWorktrees(
    [project("/repo", "min"), project("/repo/vue-app", "vue-app")],
    new Map([
      ["/repo", [worktreeEntry("/repo", { is_main: true, branch: "master" })]],
      ["/repo/vue-app", [worktreeEntry("/repo", { is_main: true, branch: "master" })]],
    ])
  );

  assert.deepEqual(grouping.roots, [
    { root: "/repo", name: "min" },
    { root: "/repo/vue-app", name: "vue-app" },
  ]);
  assert.equal(grouping.childrenByRoot.size, 0);
});

test("keeps non-git and failed lookups at the top level", () => {
  const grouping = groupProjectWorktrees(
    [project("/plain/folder"), project("/failed/lookup")],
    new Map([["/plain/folder", []]])
  );

  assert.deepEqual(grouping.roots, [project("/plain/folder"), project("/failed/lookup")]);
  assert.equal(grouping.childrenByRoot.size, 0);
});

test("preserves the project ordering and carries worktree health flags", () => {
  const main = worktreeEntry("/repo", { is_main: true });
  const locked = worktreeEntry("/repo/wt/locked", { is_locked: true });
  const prunable = worktreeEntry("/repo/wt/gone", { is_prunable: true });
  const entries = [main, locked, prunable];
  const grouping = groupProjectWorktrees(
    [project("/repo/wt/gone"), project("/repo", "min"), project("/repo/wt/locked")],
    new Map([
      ["/repo/wt/gone", entries],
      ["/repo", entries],
      ["/repo/wt/locked", entries],
    ])
  );

  assert.deepEqual(grouping.roots, [{ root: "/repo", name: "min" }]);
  assert.deepEqual(
    grouping.childrenByRoot.get("/repo")?.map((child) => [child.root, child.isLocked, child.isPrunable]),
    [
      ["/repo/wt/gone", false, true],
      ["/repo/wt/locked", true, false],
    ]
  );
});

test("matches worktree paths case-insensitively and ignores trailing separators", () => {
  const main = worktreeEntry("/Repo/", { is_main: true });
  const linked = worktreeEntry("/Repo/wt/feat/", { branch: "feat" });
  const grouping = groupProjectWorktrees(
    [project("/repo", "min"), project("/repo/wt/feat", "feat")],
    new Map([
      ["/repo", [main, linked]],
      ["/repo/wt/feat", [main, linked]],
    ])
  );

  assert.deepEqual(grouping.roots, [{ root: "/repo", name: "min" }]);
  assert.equal(grouping.childrenByRoot.get("/repo")?.length, 1);
});

test("the project tree builds root children through one shared helper", async () => {
  const view = await readFile(
    new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url),
    "utf8"
  );

  // 惰性展开与主动重载走两条代码路径，都必须经过 projectRootChildren，
  // 否则其中一条会漏掉嵌套的 worktree 子节点。
  const lazyLoad = view.match(/const loadTree: LoadFunction[\s\S]*?\n};\n/)?.[0];
  const eagerLoad = view.match(/async function loadProjectTree[\s\S]*?\n}\n/)?.[0];
  assert.ok(lazyLoad, "loadTree implementation must be present");
  assert.ok(eagerLoad, "loadProjectTree implementation must be present");
  assert.match(lazyLoad, /projectRootChildren\(root\)/);
  assert.match(eagerLoad, /projectRootChildren\(activeRoot\)/);

  // worktree 增删会改变归属关系；面板必须把 changed 事件接到重建逻辑上。
  assert.match(view, /<GitWorktreePanel[\s\S]*?@changed="reloadWorktreeGrouping"/);
  // 项目列表变化同样会改变归属关系。
  assert.match(view, /project\.projects\.map\(\(entry\) => entry\.root\)\.join\("\\n"\)/);
});

test("the resource toolbar opens the worktree drawer on the owning repository", async () => {
  const view = await readFile(
    new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url),
    "utf8"
  );
  const open = view.match(/async function openWorktreeInspector[\s\S]*?\n}\n/)?.[0];
  const show = view.match(/async function showWorktreeInspector[\s\S]*?\n}\n/)?.[0];
  assert.ok(open, "openWorktreeInspector implementation must be present");
  assert.ok(show, "showWorktreeInspector implementation must be present");

  // 选中项可能本身就是 linked worktree，不换成主仓库就列不全同组工作树。
  assert.match(show, /resolveOwningRepo\(repoRoot\)/);
  assert.match(show, /gitInspectorRepoPath\.value = repoPath;/);
  assert.match(show, /gitInspector\.value = "worktrees";/);
  assert.match(show, /gitInspectorOpen\.value = true;/);
  // 项目根常是装着多个仓库的外层目录，它自己不是仓库时必须落到名下的子仓库。
  assert.match(open, /await scanInspectorRepositories\(\)/);
  assert.match(view, /listProjectRepositories\(entry\.root\)/);
  assert.match(open, /t\("git\.noRepositories"\)/);
  // 起始项优先当前项目自身，其次它名下的仓库，不能跳到别的项目去。
  // Windows 下扫描结果与项目根的分隔符/盘符大小写可能不同，必须按归一化路径比较。
  assert.match(open, /options\.filter\(\(entry\) => isProjectPathWithin\(entry\.path, root\)\)/);
  assert.match(open, /own\.find\(\(entry\) => projectPathKey\(entry\.path\) === rootKey\) \?\? own\[0\]/);
  // 解析失败时不能带着上一次的仓库路径把抽屉打开。
  const failIndex = show.indexOf("return;");
  const openIndex = show.indexOf("gitInspectorOpen.value = true;");
  assert.ok(failIndex >= 0 && failIndex < openIndex, "must bail out before opening on failure");

  assert.match(view, /@click="openWorktreeInspector"/);
});

// 左侧可能同时放着 min 和 saas，而 saas 下还有多个仓库；只扫当前项目的话，
// 想看别的仓库就得回资源面板换项目再重开抽屉。
test("the worktree drawer lists repositories from every listed project", async () => {
  const view = await readFile(
    new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url),
    "utf8"
  );
  const open = view.match(/async function openWorktreeInspector[\s\S]*?\n}\n/)?.[0];
  const scan = view.match(/async function scanInspectorRepositories[\s\S]*?\n}\n/)?.[0];
  // 所有项目使用同一份快照并发扫描，避免扫描期间项目列表变化。
  assert.match(scan, /const projects = \[\.\.\.project\.projects\]/);
  assert.match(scan, /await Promise\.all\(\s*projects\.map\(/);
  // 一个仓库可能被多个项目扫到（例如同时加了父目录和它的子仓库）。
  assert.match(scan, /if \(seen\.has\(entry\.path\)\) continue;/);
  // 不同项目下可能有同名仓库，选项必须带上项目名。
  assert.match(scan, /\$\{owner\.name\} \/ \$\{entry\.name\}/);
  assert.match(open, /worktreeRepoOptions\.value = options;/);
});

// 多仓库工程一次只能看一个仓库的工作树，抽屉里必须能换，否则得回资源面板重开。
test("the worktree drawer can switch between repositories in the project", async () => {
  const view = await readFile(
    new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url),
    "utf8"
  );
  // 切换直接复用打开逻辑，两条路径不会各走一套解析。
  assert.match(view, /@change="showWorktreeInspector"/);
  assert.match(view, /:model-value="worktreeRepoSelection"/);
  // 只有一个仓库时切换器没有意义。
  assert.match(view, /v-if="worktreeRepoOptions\.length > 1"/);
  // 仓库多起来时靠滚动找太慢。
  assert.match(view, /:model-value="worktreeRepoSelection"[\s\S]*?filterable/);
  // 解析后的主仓库未必在选项里，回显必须用用户点的那一项。
  const show = view.match(/async function showWorktreeInspector[\s\S]*?\n}\n/)?.[0];
  assert.match(show, /worktreeRepoSelection\.value = repoRoot;/);
  // 切换器与下方创建表单共用同一套字段外观，否则两块表单看着不是一套。
  assert.match(view, /class="git-advanced-field git-advanced-field--wide"[\s\S]*?worktreeRepoSelection/);
});

// el-input 的根元素是 div，select() 只存在于内部 input；选错元素会静默失焦，
// 导致重命名框收不到 Enter。
test("inline rename focuses the inner input element", async () => {
  const view = await readFile(
    new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url),
    "utf8"
  );
  assert.match(view, /querySelector<HTMLInputElement>\("\.project-tree-rename input"\)\?\.select\(\)/);
});

test("the worktree drawer title names the repository it is showing", async () => {
  const view = await readFile(
    new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url),
    "utf8"
  );
  const title = view.match(/const gitInspectorTitle = computed\([\s\S]*?\n}\);\n/)?.[0];
  assert.ok(title, "gitInspectorTitle implementation must be present");

  // 分支和工作树可来自任意已打开的项目，都需要在标题里点名仓库。
  assert.match(title, /\["branches", "worktrees", "tags"\]\.includes\(gitInspector\.value\)[\s\S]*?return title;/);
  // 用户重命名过的项目名优先于目录名。
  assert.match(title, /project\.projects\.find\(\(entry\) => entry\.root === repoPath\)/);
  assert.match(title, /saved\?\.name \|\|[\s\S]*?projectBasename\(repoPath\)/);
});

test("the branch and worktree drawers can be resized and restore their width", async () => {
  const [view, styles] = await Promise.all([
    readFile(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8"),
    readFile(
      new URL("../src/styles/workspaces/git/repository-workspace.css", import.meta.url),
      "utf8"
    ),
  ]);

  assert.match(view, /gitInspector\.value === "branches" \|\| gitInspector\.value === "worktrees"/);
  assert.match(view, /gitInspectorWidth\.value}px/);
  assert.match(view, /@pointerdown="startGitInspectorResize"/);
  assert.match(view, /@keydown="resizeGitInspectorWithKeyboard"/);
  assert.match(view, /savePersistedValue\(GIT_INSPECTOR_WIDTH_KEY/);
  assert.match(styles, /\.git-inspector-drawer__resize-handle/);
  assert.match(styles, /cursor: ew-resize/);
});

// 建 worktree 的入口只保留抽屉一处；右键对话框重新出现会让两套流程再度分叉。
test("no second worktree-creation entry survives outside the drawer", async () => {
  const view = await readFile(
    new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url),
    "utf8"
  );
  assert.doesNotMatch(view, /workspaceCreate/);
  assert.doesNotMatch(view, /createWorkspaceForNode/);
  assert.doesNotMatch(view, /addGitWorktree/);
});

// 点击工作树后首屏必须直接提供批量能力，不能再让用户经过单建表单或二次入口。
test("the worktree drawer opens directly into batch creation", async () => {
  const panel = await readFile(
    new URL("../src/components/git/GitWorktreePanel.vue", import.meta.url),
    "utf8"
  );
  assert.match(panel, /<slot name="header" \/>\s*<GitBatchWorktreePanel/);
  assert.doesNotMatch(panel, /class="git-worktree-create"/);
  assert.doesNotMatch(panel, /showBatchPanel|setBatchPanelVisible/);
  assert.doesNotMatch(panel, /addGitWorktree|chooseWorktreePath|createWorktree/);
});

function batchRepository(overrides = {}) {
  return {
    name: "api",
    relative_path: "services/api",
    currentBranch: "main",
    branchNames: ["main", "origin/main"],
    localBranchNames: ["main"],
    worktrees: [],
    ...overrides,
  };
}

test("builds batch worktree paths from branch slug and repository relative path", () => {
  assert.deepEqual(
    buildBatchWorktreePlan(batchRepository(), {
      targetRoot: "/worktrees",
      branch: "feat/orders v2",
      baseBranch: "main",
    }),
    {
      worktreePath: "/worktrees/feat-orders-v2/services/api",
      baseBranch: "main",
    },
  );
});

test("uses repository name when the selected root is itself a repository", () => {
  const plan = buildBatchWorktreePlan(
    batchRepository({ name: "root-api", relative_path: "." }),
    { targetRoot: "/worktrees", branch: "feat/orders", baseBranch: "main" },
  );
  assert.equal(plan.worktreePath, "/worktrees/feat-orders/root-api");
});

test("preflights missing bases and branch conflicts per repository", () => {
  assert.equal(
    buildBatchWorktreePlan(batchRepository(), {
      targetRoot: "/worktrees",
      branch: "feat/orders",
      baseBranch: "release",
    }).issue,
    "base_branch_not_found",
  );
  assert.equal(
    buildBatchWorktreePlan(
      batchRepository({ localBranchNames: ["main", "feat/orders"] }),
      { targetRoot: "/worktrees", branch: "feat/orders", baseBranch: "main" },
    ).issue,
    "branch_exists",
  );
});

test("preflights existing branches without treating them as new branches", () => {
  assert.deepEqual(
    buildBatchWorktreePlan(batchRepository({ localBranchNames: ["main", "release"] }), {
      targetRoot: "/worktrees",
      branch: "release",
      baseBranch: "",
      branchMode: "existing",
    }),
    {
      worktreePath: "/worktrees/release/services/api",
      baseBranch: "",
    },
  );
  assert.equal(
    buildBatchWorktreePlan(batchRepository(), {
      targetRoot: "/worktrees",
      branch: "release",
      baseBranch: "",
      branchMode: "existing",
    }).issue,
    "branch_not_found",
  );
  assert.equal(
    buildBatchWorktreePlan(
      batchRepository({
        worktrees: [worktreeEntry("/worktrees/release/api", { branch: "release" })],
      }),
      {
        targetRoot: "/worktrees",
        branch: "release",
        baseBranch: "",
        branchMode: "existing",
      },
    ).issue,
    "branch_not_found",
  );
});

test("rejects an existing branch already checked out by another worktree", () => {
  assert.equal(
    buildBatchWorktreePlan(
      batchRepository({
        localBranchNames: ["main", "release"],
        worktrees: [worktreeEntry("/linked/release", { branch: "refs/heads/release" })],
      }),
      {
        targetRoot: "/worktrees",
        branch: "release",
        baseBranch: "",
        branchMode: "existing",
      },
    ).issue,
    "branch_in_use",
  );
});

test("batch worktree creation is visible by default and only locks the drawer while busy", async () => {
  const [view, panel, batchPanel, styles] = await Promise.all([
    readFile(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/components/git/GitWorktreePanel.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/components/git/GitBatchWorktreePanel.vue", import.meta.url), "utf8"),
    readFile(
      new URL("../src/styles/workspaces/git/repository-workspace.css", import.meta.url),
      "utf8",
    ),
  ]);

  assert.match(view, /:close-on-click-modal="!worktreeBatchActive && !branchPanelBusy"/);
  assert.match(view, /:close-on-press-escape="!worktreeBatchActive && !branchPanelBusy"/);
  assert.match(view, /@batch-active="worktreeBatchActive = \$event"/);
  assert.match(view, /@closed="worktreeBatchActive = false; branchPanelBusy = false"/);
  assert.match(panel, /batchActive: \[active: boolean\]/);
  assert.match(panel, /<GitBatchWorktreePanel/);
  assert.match(panel, /@busy="emit\('batchActive', \$event\)"/);
  assert.doesNotMatch(panel, /showBatchPanel|setBatchPanelVisible/);
  assert.match(batchPanel, /emit\("busy", true\)/);
  assert.match(batchPanel, /emit\("busy", false\)/);
  assert.doesNotMatch(batchPanel, /v-loading=\"scanning\"/);
  assert.doesNotMatch(batchPanel, /emit\(["']close["']/);
  assert.match(batchPanel, /const formIssue = computed/);
  assert.match(batchPanel, /plan\.issue && plan\.issue !== formIssue\.value/);
  assert.match(batchPanel, /formIssue === 'invalid_branch'/);
  assert.match(batchPanel, /repository\.branchMode === "new"/);
  assert.match(batchPanel, /repository\.branchMode === 'existing'/);
  assert.match(batchPanel, /commonLocalBranches/);
  assert.match(styles, /container-type: inline-size/);
  assert.match(styles, /overflow-x: hidden/);
  assert.match(styles, /@container \(max-width: 620px\)/);
  assert.match(styles, /grid-template-areas:/);
  assert.match(view, /DEFAULT_GIT_INSPECTOR_WIDTH = 720/);
  assert.match(view, /MIN_WORKTREE_INSPECTOR_WIDTH = 620/);
});
