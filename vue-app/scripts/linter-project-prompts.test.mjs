import assert from "node:assert/strict";
import test from "node:test";
import contract from "../src/domain/plugins/contribution-points.json" with { type: "json" };
import { LINTER_LIMITS, collectLinters, validateLinterEntry } from "../src/domain/plugins/linters.ts";
import {
  LINTER_PROMPTS_STORAGE_KEY,
  linterPromptPlan,
  normalizeLinterPrompts,
  recordLinterPrompt,
} from "../src/domain/plugins/linter-prompts.ts";

const base = { title: "T", action: "lint", triggers: ["open", "save"], debounceMs: 750 };

test("linter entries keep detect.files and enableSetting and drop malformed ones", () => {
  const ok = validateLinterEntry({ ...base, id: "eslint", languages: ["javascript"], detect: { files: ["eslint.config.js", ".github/workflows"] }, enableSetting: "eslint.enabled" });
  assert.equal(ok.ok, true);
  assert.deepEqual(ok.linter.detect, { files: ["eslint.config.js", ".github/workflows"] });
  assert.equal(ok.linter.enableSetting, "eslint.enabled");
  for (const detect of [{ files: [] }, { files: ["../x"] }, { files: ["a*"] }, { files: [1] }, { files: Array(17).fill("a") }]) {
    assert.equal(validateLinterEntry({ ...base, id: "a", languages: ["json"], detect }).ok, false, JSON.stringify(detect));
  }
  assert.equal(validateLinterEntry({ ...base, id: "a", languages: ["json"], enableSetting: "Bad Key" }).ok, false);
});

test("the frontend keeps as many linters per plugin as the contract allows", () => {
  assert.equal(LINTER_LIMITS.maxEntries, contract.points.linters.maxEntries);
  const entries = Array.from({ length: 13 }, (_, index) => ({ ...base, id: `l${index}`, languages: [`lang${index}`] }));
  const result = collectLinters([{ pluginId: "p", pluginName: "P", runtime: "process", activeInWorkspace: true, point: { entries, issues: [] } }]);
  assert.equal(result.linters.length, 13);
});

const eslint = { ref: "lint/eslint", pluginId: "lint", languages: ["javascript", "typescript", "vue"], detect: { files: ["eslint.config.js"] }, enableSetting: "eslint.enabled" };
const markdown = { ref: "lint/markdownlint", pluginId: "lint", languages: ["markdown"], detect: { files: [".markdownlint.json"] } };
const other = { ref: "other/ts", pluginId: "other", languages: ["typescript"] };

test("a prompt offers detected linters for languages without a choice", () => {
  const plan = linterPromptPlan({
    language: "typescript", linters: [eslint, markdown, other], detected: ["lint/eslint", "lint/markdownlint"],
    selections: {}, settings: { "lint/eslint": false }, prompts: normalizeLinterPrompts(undefined), workspaceId: "w1",
  });
  assert.deepEqual(plan, { linter: eslint, languages: ["javascript", "typescript", "vue"], enableSetting: "eslint.enabled" });
});

test("prompts never override another linter, repeat, or appear without detection", () => {
  const common = { linters: [eslint, other], detected: ["lint/eslint"], settings: { "lint/eslint": false }, workspaceId: "w1", prompts: normalizeLinterPrompts(undefined) };
  // TypeScript already uses another linter: only the remaining languages are offered.
  assert.deepEqual(linterPromptPlan({ ...common, language: "vue", selections: { typescript: "other/ts" } }).languages, ["javascript", "vue"]);
  assert.equal(linterPromptPlan({ ...common, language: "typescript", selections: { typescript: "other/ts" } }), undefined);
  // Not detected in this project.
  assert.equal(linterPromptPlan({ ...common, language: "javascript", detected: [], selections: {} }), undefined);
  // Already answered in this workspace, either way.
  for (const answer of ["enabled", "dismissed"]) {
    const prompts = recordLinterPrompt(normalizeLinterPrompts(undefined), "w1", "lint/eslint", answer);
    assert.equal(linterPromptPlan({ ...common, language: "javascript", selections: {}, prompts }), undefined);
    assert.notEqual(linterPromptPlan({ ...common, language: "javascript", selections: {}, prompts, workspaceId: "w2" }), undefined);
  }
});

test("a selected linter whose workspace setting is off is offered again only for the setting", () => {
  const selections = { javascript: "lint/eslint", typescript: "lint/eslint", vue: "lint/eslint" };
  const prompts = normalizeLinterPrompts(undefined);
  const off = linterPromptPlan({ language: "javascript", linters: [eslint], detected: ["lint/eslint"], selections, settings: { "lint/eslint": false }, prompts, workspaceId: "w1" });
  assert.deepEqual(off, { linter: eslint, languages: [], enableSetting: "eslint.enabled" });
  assert.equal(linterPromptPlan({ language: "javascript", linters: [eslint], detected: ["lint/eslint"], selections, settings: { "lint/eslint": true }, prompts, workspaceId: "w1" }), undefined);
  // Without an enableSetting a fully selected linter needs nothing.
  assert.equal(linterPromptPlan({ language: "markdown", linters: [markdown], detected: ["lint/markdownlint"], selections: { markdown: "lint/markdownlint" }, settings: {}, prompts, workspaceId: "w1" }), undefined);
});

test("prompt answers are machine-local, bounded and normalized", () => {
  assert.equal(LINTER_PROMPTS_STORAGE_KEY.startsWith("app_"), false);
  assert.deepEqual(normalizeLinterPrompts({ version: 1, byWorkspace: { w1: { "a/b": "enabled", "a/c": "maybe", bad: "enabled" } } }).byWorkspace, { w1: { "a/b": "enabled" } });
  let prompts = normalizeLinterPrompts(null);
  for (let index = 0; index < 300; index++) prompts = recordLinterPrompt(prompts, `w${index}`, "a/b", "dismissed");
  assert.ok(Object.keys(prompts.byWorkspace).length <= 256);
  assert.equal(prompts.byWorkspace.w299["a/b"], "dismissed");
});

import { createLinterProjectPrompts } from "../src/services/linter-project-prompts.ts";

function fakePromptDeps(overrides = {}) {
  const calls = { detect: 0, confirm: [], selected: [], enabled: [], relint: [], saved: [] };
  const selections = {};
  const deps = {
    workspaceId: () => "w1",
    projectRoot: () => "/repo",
    linters: () => [{ ...eslint, title: "ESLint", pluginName: "External Linters", id: "eslint", action: "lint", triggers: ["open"], debounceMs: 750 }],
    selections: () => ({ ...selections }),
    detect: async () => { calls.detect += 1; return { root: "/repo", linters: ["lint/eslint"] }; },
    settingValue: async () => false,
    loadPrompts: async () => undefined,
    savePrompts: async (prompts) => { calls.saved.push(prompts); },
    confirm: async (plan) => { calls.confirm.push(plan); return "enable"; },
    selectLinter: (language, ref) => { selections[language] = ref; calls.selected.push(language); },
    enableSetting: async (pluginId, key) => { calls.enabled.push(`${pluginId}:${key}`); },
    relint: (ref) => calls.relint.push(ref),
    reportError: (error) => { throw error; },
    ...overrides,
  };
  return { deps, calls };
}

test("accepting the prompt turns the workspace setting on, then selects the linter, once per workspace", async () => {
  const { deps, calls } = fakePromptDeps();
  const prompts = createLinterProjectPrompts(deps);
  await prompts.onModelOpened("typescript");
  assert.equal(calls.confirm.length, 1);
  assert.deepEqual(calls.enabled, ["lint:eslint.enabled"]);
  assert.deepEqual(calls.selected, ["javascript", "typescript", "vue"]);
  assert.deepEqual(calls.relint, ["lint/eslint"]);
  assert.equal(calls.saved.at(-1).byWorkspace.w1["lint/eslint"], "enabled");
  await prompts.onModelOpened("javascript");
  assert.equal(calls.confirm.length, 1, "asked again");
  assert.equal(calls.detect, 1, "detection is cached per project");
});

test("dismissing is remembered; closing the dialog only defers it for this session", async () => {
  const dismissed = fakePromptDeps({ confirm: async () => "dismiss" });
  const one = createLinterProjectPrompts(dismissed.deps);
  await one.onModelOpened("vue");
  assert.deepEqual(dismissed.calls.selected, []);
  assert.deepEqual(dismissed.calls.enabled, []);
  assert.equal(dismissed.calls.saved.at(-1).byWorkspace.w1["lint/eslint"], "dismissed");

  let shown = 0;
  const later = fakePromptDeps({ confirm: async () => { shown += 1; return "later"; } });
  const two = createLinterProjectPrompts(later.deps);
  await two.onModelOpened("vue");
  await two.onModelOpened("typescript");
  assert.equal(shown, 1);
  assert.equal(later.calls.saved.length, 0);
});

test("no prompt without a project, without detection, or in the browser preview", async () => {
  for (const overrides of [{ projectRoot: () => "" }, { detect: async () => ({ root: "/repo", linters: [] }) }, { detect: async () => { throw new Error("no host"); } }]) {
    const { deps, calls } = fakePromptDeps(overrides);
    await createLinterProjectPrompts(deps).onModelOpened("typescript");
    assert.equal(calls.confirm.length, 0);
  }
  const { deps, calls } = fakePromptDeps();
  await createLinterProjectPrompts(deps).onModelOpened("python");
  assert.equal(calls.detect, 0, "a language without detectable linters never calls the host");
});

test("the prompt waits for the saved linter choices before deciding", async () => {
  let loaded = false;
  const { deps, calls } = fakePromptDeps({
    ready: async () => { await new Promise((resolve) => setTimeout(resolve, 5)); loaded = true; },
    selections: () => (loaded ? { javascript: "lint/eslint", typescript: "lint/eslint", vue: "lint/eslint" } : {}),
    settingValue: async () => true,
  });
  await createLinterProjectPrompts(deps).onModelOpened("typescript");
  assert.equal(calls.confirm.length, 0, "asked before the saved choices were loaded");
});

test("settings use the prompt's workspace and a failed setting write still selects the linter", async () => {
  const seen = [];
  const errors = [];
  const { deps, calls } = fakePromptDeps({
    settingValue: async (pluginId, key, workspaceId) => { seen.push(`read:${workspaceId}`); return false; },
    enableSetting: async (pluginId, key, workspaceId) => { seen.push(`write:${workspaceId}`); throw new Error("configuration workspace id is invalid"); },
    reportError: (error) => errors.push(String(error)),
  });
  await createLinterProjectPrompts(deps).onModelOpened("typescript");
  assert.deepEqual(seen, ["read:w1", "write:w1"]);
  assert.deepEqual(calls.selected, ["javascript", "typescript", "vue"]);
  assert.equal(errors.length, 1);
  assert.equal(calls.saved.at(-1).byWorkspace.w1["lint/eslint"], "enabled");
});
