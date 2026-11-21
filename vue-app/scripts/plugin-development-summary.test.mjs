import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import {
  declarativeContributionCounts,
  declarativeContributionIssues,
  normalizeDevelopmentContributions,
  isDevelopmentCardActive,
  filterDevelopmentCards,
} from "../src/domain/plugins/development-summary.ts";

test("declarative dev sessions report what each contribution point provides", () => {
  const contributions = normalizeDevelopmentContributions({
    themes: { count: 5, issues: ["skipped theme \"x\": low contrast"] },
    keymaps: { count: 0 },
    snippets: { count: 2 },
    "bad key": { count: 9 },
    languages: "not an object",
    aiPrompts: { count: -1, issues: [1, "ok"] },
  });
  assert.deepEqual(Object.keys(contributions).sort(), ["aiPrompts", "keymaps", "snippets", "themes"]);
  assert.deepEqual(contributions.aiPrompts, { count: 0, issues: ["ok"] });

  const manifest = { keymapCount: 0, keymapIssues: [], contributions };
  assert.deepEqual(declarativeContributionCounts(manifest), [
    { point: "themes", count: 5 },
    { point: "snippets", count: 2 },
  ]);
  assert.deepEqual(declarativeContributionIssues(manifest), ["skipped theme \"x\": low contrast", "ok"]);
});

test("hosts without the per-point summary fall back to keymaps", () => {
  const legacy = { keymapCount: 4, keymapIssues: ["skipped keymap"], contributions: normalizeDevelopmentContributions(undefined) };
  assert.deepEqual(declarativeContributionCounts(legacy), [{ point: "keymaps", count: 4 }]);
  assert.deepEqual(declarativeContributionIssues(legacy), ["skipped keymap"]);
  assert.deepEqual(declarativeContributionCounts({ ...legacy, keymapCount: 0 }), []);
});

test("开发项目按会话和启动状态分类", () => {
  for (const runnerStatus of ["starting", "running", "stopping"]) {
    assert.equal(isDevelopmentCardActive({ runnerStatus }), true);
  }
  for (const runnerStatus of [undefined, "stopped", "error"]) {
    assert.equal(isDevelopmentCardActive({ runnerStatus }), false);
  }
  assert.equal(isDevelopmentCardActive({ session: {}, runnerStatus: "stopped" }), true);
});

test("状态筛选叠加搜索，活动项目稳定置顶且不改变原数组", () => {
  const cards = [
    { pluginName: "Alpha", pluginId: "alpha", path: "/alpha" },
    { pluginName: "Beta", pluginId: "beta", path: "/beta", runnerStatus: "starting" },
    { pluginName: "Gamma", pluginId: "gamma", path: "/gamma", session: {} },
    { pluginName: "Delta", pluginId: "delta", path: "/delta", runnerStatus: "error" },
  ];
  const ids = result => result.map(card => card.pluginId);
  assert.deepEqual(ids(filterDevelopmentCards(cards, "all", "")), ["beta", "gamma", "alpha", "delta"]);
  assert.deepEqual(ids(filterDevelopmentCards(cards, "running", "")), ["beta", "gamma"]);
  assert.deepEqual(ids(filterDevelopmentCards(cards, "notRunning", "")), ["alpha", "delta"]);
  assert.deepEqual(ids(filterDevelopmentCards(cards, "running", " /BETA ")), ["beta"]);
  assert.deepEqual(filterDevelopmentCards(cards, "notRunning", "Gamma"), []);
  assert.deepEqual(ids(cards), ["alpha", "beta", "gamma", "delta"]);
  assert.deepEqual(filterDevelopmentCards([], "all", ""), []);
});

test("开发卡片限制内容宽度并允许标签和操作换行，避免原生横向滚动条", async () => {
  const css = await readFile(new URL("../src/styles/workspaces/plugins/workspace.css", import.meta.url), "utf8");
  assert.match(css, /\.plugin-development-panel__card > \.el-card__body\s*\{[^}]*overflow: hidden/);
  assert.match(css, /\.plugin-development-panel__card-header\s*\{[^}]*flex-wrap: wrap/);
  assert.match(css, /\.plugin-development-panel__identity\s*\{[^}]*flex: 1/);
  assert.match(css, /\.plugin-development-panel__actions\s*\{[^}]*flex-wrap: wrap/);
});

test("开发启动失败展示启动器错误，事件刷新不会丢弃审批状态", async () => {
  const source = await readFile(new URL("../src/components/plugins/development/PluginDevelopmentPanel.vue", import.meta.url), "utf8");
  assert.match(source, /v-if="card\.runnerError"/);
  assert.match(source, /:title="card\.runnerError"/);
  assert.match(source, /refreshPending = true/);
  assert.match(source, /clearInterval\(refreshTimer\)/);
});

test("issues are capped for display", () => {
  const issues = Array.from({ length: 30 }, (_, index) => `issue ${index}`);
  const contributions = normalizeDevelopmentContributions({ themes: { count: 1, issues }, snippets: { count: 1, issues } });
  assert.equal(contributions.themes.issues.length, 16);
  assert.equal(declarativeContributionIssues({ keymapCount: 0, keymapIssues: [], contributions }).length, 16);
});
