import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";
import { computed, reactive, ref, watch, nextTick } from "vue";
import { createLatestRequestGate } from "../src/domain/async/latest-request-gate.ts";

const view = await readFile(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8");
const panel = await readFile(new URL("../src/components/git/GitBranchPanel.vue", import.meta.url), "utf8");

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

function mountLogic() {
  const props = reactive({ repoPath: "/project-a" });
  const requests = [];
  const commands = [];
  const events = [];
  const errors = [];
  const prompt = deferred();
  const script = panel.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1];
  const code = ts.transpileModule(`${script}\nexport { branches, status, loading, refresh, withBranchAction, createBranch, mergeDialogVisible };`, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  const modules = {
    vue: { computed, ref, watch, onBeforeUnmount() {} },
    "element-plus": { ElMessage: { error: (error) => errors.push(error) }, ElMessageBox: { prompt: () => prompt.promise } },
    "@element-plus/icons-vue": {},
    "@/i18n/messages": { useVueI18n: () => ({ t: (key) => key }) },
    "@/services/desktop-bridge": { invokeBackend: (command, args) => {
      const pending = deferred();
      requests.push({ command, ...args, ...pending });
      return pending.promise;
    } },
    "@/services/git-service": {
      runGitCommand: async (...args) => { commands.push(args); },
      getGitMergeState: (repoPath) => {
        const pending = deferred();
        requests.push({ command: "git_get_merge_state", repoPath, ...pending });
        return pending.promise;
      },
    },
    "@/components/git/GitConflictResolver.vue": {},
    "@/domain/async/latest-request-gate": { createLatestRequestGate },
  };
  const exports = {};
  new Function("require", "exports", "defineProps", "defineEmits", "defineOptions", "defineExpose", code)(
    (name) => { assert.ok(modules[name], name); return modules[name]; }, exports,
    () => props, () => (...args) => events.push(args), () => {}, () => {},
  );
  const settle = async (start, name = "main") => {
    requests[start].resolve({ current: name, branches: [{ name, is_current: true, is_remote: false }] });
    requests[start + 1].resolve({ staged: [], unstaged: [], untracked: [] });
    requests[start + 2].resolve({ active: false, conflicts: [] });
    await nextTick();
    await nextTick();
  };
  return { ...exports, props, requests, commands, events, errors, prompt, settle };
}

test("分支管理显示项目名称、路径和可搜索选择器，切换不联动外部工作区", () => {
  assert.match(view, /\["branches", "worktrees", "tags"\]\.includes\(gitInspector\.value\)/);
  assert.match(view, /saved\?\.name/);
  assert.match(view, /const options = await scanInspectorRepositories\(\)/);
  assert.match(view, /:disabled="branchRepoLoading \|\| branchPanelBusy"[\s\S]*?filterable[\s\S]*?@change="selectBranchRepository"/);
  assert.match(view, /class="git-branch-panel__repo-path">\{\{ gitInspectorRepoPath \}\}/);
  const selection = view.match(/function selectBranchRepository\([\s\S]*?\n\}/)[0];
  assert.match(selection, /gitInspectorRepoPath\.value = repoPath/);
  assert.doesNotMatch(selection, /selectSavedProject|selectRepository|resolveOwningRepo/);
  assert.match(view, /if \(!gitInspectorOpen\.value\) gitInspectorRepoPath\.value = repoPath/);
});

test("快速切仓库丢弃旧响应，等待期间清空旧分支", async () => {
  const logic = mountLogic();
  logic.props.repoPath = "/project-b";
  await nextTick();
  assert.equal(logic.branches.value, null);
  await logic.settle(3, "develop");
  await logic.settle(0, "main");
  assert.equal(logic.branches.value.current, "develop");
  assert.equal(logic.loading.value, false);
  assert.equal(logic.requests[3].repoPath, "/project-b");
});

test("旧请求失败不污染新仓库，当前请求失败不保留旧数据", async () => {
  const logic = mountLogic();
  await logic.settle(0);
  const oldRefresh = logic.refresh();
  logic.props.repoPath = "/project-b";
  await nextTick();
  logic.requests[3].reject(new Error("stale"));
  await oldRefresh;
  assert.equal(logic.errors.length, 0);
  assert.equal(logic.loading.value, true);
  logic.requests[6].reject(new Error("current"));
  await nextTick();
  await nextTick();
  assert.equal(logic.errors.length, 1);
  assert.equal(logic.branches.value, null);
  assert.equal(logic.status.value, null);
});

test("确认框期间锁定切换，意外改仓库时不提交命令", async () => {
  const logic = mountLogic();
  await logic.settle(0);
  const action = logic.withBranchAction(logic.createBranch);
  assert.deepEqual(logic.events.at(-1), ["busy", true]);
  logic.props.repoPath = "/project-b";
  await nextTick();
  logic.prompt.resolve({ value: "feature/test" });
  await action;
  assert.equal(logic.commands.length, 0);
});

test("创建分支绑定选中仓库，命令完成并刷新后解锁", async () => {
  const logic = mountLogic();
  await logic.settle(0);
  const action = logic.withBranchAction(logic.createBranch);
  logic.prompt.resolve({ value: "feature/test" });
  await nextTick();
  await nextTick();
  assert.deepEqual(logic.commands, [["git_checkout_branch", "/project-a", { branchName: "feature/test", create: true }]]);
  await new Promise((resolve) => setImmediate(resolve));
  await logic.settle(3, "feature/test");
  await action;
  assert.deepEqual(logic.events.at(-1), ["busy", false]);
  logic.mergeDialogVisible.value = true;
  assert.deepEqual(logic.events.at(-1), ["busy", true]);
  logic.mergeDialogVisible.value = false;
  assert.deepEqual(logic.events.at(-1), ["busy", false]);
});
