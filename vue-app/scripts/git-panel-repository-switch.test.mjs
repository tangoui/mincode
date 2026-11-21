import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";
import { computed, nextTick, reactive, ref, watch } from "vue";
import { createLatestRequestGate } from "../src/domain/async/latest-request-gate.ts";

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

async function settle() {
  for (let index = 0; index < 4; index += 1) await nextTick();
}

async function mountPanel(file, exported) {
  const source = await readFile(new URL(`../src/components/git/${file}`, import.meta.url), "utf8");
  const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1];
  const code = ts.transpileModule(`${script}\nexport { loading, refresh, ${exported.join(", ")} };`, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText;

  const props = reactive({ repoPath: "/repo-a" });
  const requests = [];
  const commands = [];
  const errors = [];
  const confirms = [];
  const load = (repoPath) => {
    const pending = deferred();
    requests.push({ repoPath, ...pending });
    return pending.promise;
  };
  const command = (name) => async (...args) => {
    commands.push([name, ...args]);
  };
  const modules = {
    vue: { computed, ref, watch, onBeforeUnmount() {} },
    "element-plus": {
      ElMessage: { error: (error) => errors.push(error), success() {}, warning() {} },
      ElMessageBox: {
        confirm: () => {
          const pending = deferred();
          confirms.push(pending);
          return pending.promise;
        },
        prompt: () => {
          const pending = deferred();
          confirms.push(pending);
          return pending.promise;
        },
      },
    },
    "@/i18n/messages": { useVueI18n: () => ({ t: (key) => key, intlLocale: ref("en") }) },
    "@/services/desktop-bridge": { invokeBackend: (_command, args) => load(args.repoPath) },
    "@/services/git-service": {
      runGitCommand: command("runGitCommand"),
      listGitWorktrees: load,
      removeGitWorktree: command("removeGitWorktree"),
      lockGitWorktree: command("lockGitWorktree"),
      unlockGitWorktree: command("unlockGitWorktree"),
      pruneGitWorktrees: command("pruneGitWorktrees"),
      getGitLog: load,
      runInteractiveGitRebase: command("runInteractiveGitRebase"),
    },
    "@/composables/use-workspace-state-scope": {
      useWorkspaceStateScope: () => ({ workbench: { activeWorktreePath: "", workspaces: [] } }),
    },
    "@/domain/git/worktree": { workspaceIdsBoundTo: () => [] },
    "@/domain/async/latest-request-gate": { createLatestRequestGate },
  };
  const exports = {};
  new Function(
    "require",
    "exports",
    "defineProps",
    "defineEmits",
    "defineOptions",
    "defineExpose",
    "withDefaults",
    code
  )(
    (name) => modules[name] ?? {},
    exports,
    () => props,
    () => () => {},
    () => {},
    () => {},
    (value) => value
  );
  await settle();
  return { panel: exports, props, requests, commands, errors, confirms };
}

const panels = [
  {
    file: "GitStashPanel.vue",
    list: "stashes",
    action: "dropStash",
    entry: (repo) => ({ index: 0, oid: `${repo}-stash`, message: repo, files: [] }),
    runAction: (panel, entry) => panel.dropStash(entry),
  },
  {
    file: "GitShelfPanel.vue",
    list: "shelves",
    action: "deleteShelf",
    entry: (repo) => ({ id: `${repo}-shelf`, message: repo, files: [], createdAt: 0 }),
    runAction: (panel, entry) => panel.deleteShelf(entry),
  },
  {
    file: "GitTagPanel.vue",
    list: "tags",
    action: "deleteTag",
    entry: (repo) => ({ name: `${repo}-tag` }),
    runAction: (panel, entry) => panel.deleteTag(entry.name),
  },
  {
    file: "GitWorktreePanel.vue",
    list: "worktrees",
    action: "removeWorktree",
    entry: (repo) => ({ path: `${repo}-wt`, is_main: false, is_locked: false, is_prunable: false }),
    runAction: (panel, entry) => panel.removeWorktree(entry),
  },
  {
    file: "GitRebasePanel.vue",
    list: "logs",
    action: "executeRebase",
    entries: (repo) => [
      { hash: `${repo}-2`, message: "second" },
      { hash: `${repo}-1`, message: "first" },
    ],
    runAction: (panel) => panel.executeRebase(),
  },
];

for (const config of panels) {
  const entriesFor = (repo) => config.entries?.(repo) ?? [config.entry(repo)];

  test(`${config.file} drops list results from a previous repository`, async () => {
    const { panel, props, requests, errors } = await mountPanel(config.file, [config.list, config.action]);
    assert.equal(requests.length, 1);
    assert.equal(requests[0].repoPath, "/repo-a");
    assert.equal(panel.loading.value, true);

    props.repoPath = "/repo-b";
    await settle();
    assert.equal(requests.length, 2);
    assert.equal(requests[1].repoPath, "/repo-b");
    assert.deepEqual(panel[config.list].value, []);

    // A 的过期响应既不能覆盖列表，也不能提前结束 B 的加载状态。
    requests[0].resolve(entriesFor("a"));
    await settle();
    assert.deepEqual(panel[config.list].value, []);
    assert.equal(panel.loading.value, true);

    requests[1].resolve(entriesFor("b"));
    await settle();
    assert.deepEqual(panel[config.list].value, entriesFor("b"));
    assert.equal(panel.loading.value, false);

    props.repoPath = "/repo-c";
    await settle();
    props.repoPath = "/repo-d";
    await settle();
    requests[2].reject(new Error("stale failure"));
    await settle();
    assert.deepEqual(errors, []);
    assert.equal(panel.loading.value, true);
  });

  test(`${config.file} does not run a confirmed action on a different repository`, async () => {
    const { panel, props, requests, commands, confirms } = await mountPanel(config.file, [
      config.list,
      config.action,
    ]);
    requests[0].resolve(entriesFor("a"));
    await settle();
    const action = config.runAction(panel, entriesFor("a")[0]);
    assert.equal(confirms.length, 1);

    props.repoPath = "/repo-b";
    await settle();
    confirms[0].resolve();
    await action;
    await settle();
    assert.deepEqual(commands, []);
  });
}
