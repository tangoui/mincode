import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { messagesEn } from "../src/i18n/locales/en.ts";
import { messagesZh } from "../src/i18n/locales/zh.ts";
import { settingsIntegrations } from "./settings-integrations-module.mjs";

const { aiSkillSource, createManualAiSkill, hasAiSkillNamed } = settingsIntegrations;

const viewPath = new URL("../src/views/SettingsWorkspaceView.vue", import.meta.url);
const stylePath = new URL("../src/styles/workspaces/settings/dialog.css", import.meta.url);

const [viewSource, styleSource] = await Promise.all([
  readFile(viewPath, "utf8"),
  readFile(stylePath, "utf8"),
]);

test("installed skills expose a Markdown editor with safe explicit persistence", () => {
  assert.match(viewSource, /function openSkillEditor\(skill: AiSkill\)/);
  assert.match(viewSource, /async function saveSkillEditor\(\)/);
  assert.match(viewSource, /parseAiSkillMarkdown\(\s*skillEditorText\.value/);
  assert.match(
    viewSource,
    /const updated: AiSkill = \{\s*\.\.\.original,\s*name: parsed\.name,\s*description: parsed\.description,\s*content: parsed\.content/
  );
  assert.match(viewSource, /await persistSettingsSnapshot\(/);
  assert.match(viewSource, /settings\.aiSkills\.splice\(index, 1, original\)/);
  assert.match(viewSource, /v-model="skillEditorOpen"/);
  assert.match(viewSource, /v-model="skillEditorText"/);
  assert.match(viewSource, /@click="openSkillEditor\(skill\)"/);
});

test("the new-skill templates of both languages create a valid manual skill", () => {
  for (const messages of [messagesZh, messagesEn]) {
    const skill = createManualAiSkill(messages["settings.skills.template"], "skill-1");
    assert.equal(skill.id, "skill-1");
    assert.equal(skill.name, "new-skill");
    assert.ok(skill.description);
    assert.equal(skill.origin, "manual");
    assert.equal(skill.sourcePath, undefined);
    assert.equal(skill.enabled, true);
    assert.equal(aiSkillSource(skill), "manual");
  }
});

test("a manual skill needs front matter name and stays within the size limit", () => {
  assert.throws(() => createManualAiSkill("# Notes only", "skill-1"), /skill_name_required/);
  assert.throws(() => createManualAiSkill("---\ndescription: x\n---\nbody", "skill-1"), /skill_name_required/);
  assert.throws(() => createManualAiSkill("   ", "skill-1"), /empty_skill/);
  assert.throws(() => createManualAiSkill(`---\nname: big\n---\n${"x".repeat(513 * 1024)}`, "skill-1"), /skill_too_large/);
  const skill = createManualAiSkill("---\nname: Review\ndocs: https://example.com/review\n---\nCheck the diff.", "skill-2");
  assert.equal(skill.name, "Review");
  assert.equal(skill.docsUrl, "https://example.com/review");
  assert.equal(skill.content, "---\nname: Review\ndocs: https://example.com/review\n---\nCheck the diff.");
});

test("skill sources and duplicate names", () => {
  assert.equal(aiSkillSource({ sourcePath: "/Users/me/skills/review/SKILL.md" }), "local");
  assert.equal(aiSkillSource({ sourcePath: "C:\\skills\\review\\SKILL.md" }), "local");
  assert.equal(aiSkillSource({ sourcePath: "https://raw.githubusercontent.com/o/r/main/SKILL.md" }), "registry");
  assert.equal(aiSkillSource({ sourcePath: "builtin:code-review" }), "registry");
  assert.equal(aiSkillSource({}), "registry");
  const skills = [
    { id: "a", name: "Code Review", content: "x", enabled: true },
    { id: "b", name: "Debug", content: "y", enabled: true },
  ];
  assert.equal(hasAiSkillNamed(skills, "  code review "), true);
  assert.equal(hasAiSkillNamed(skills, "Code Review", "a"), false);
  assert.equal(hasAiSkillNamed(skills, "Refactor"), false);
});

test("the Skills panel creates skills through the shared SKILL.md editor", () => {
  assert.match(viewSource, /@click="openNewSkillEditor"/);
  assert.match(viewSource, /if \(!editingSkillId\.value\) return createSkillFromEditor\(\);/);
  assert.match(viewSource, /createManualAiSkill\(skillEditorText\.value, createId\("skill"\)\)/);
  assert.match(viewSource, /hasAiSkillNamed\(settings\.aiSkills, skill\.name\)/);
  assert.match(viewSource, /t\(SKILL_SOURCE_LABELS\[aiSkillSource\(skill\)\]\)/);
  assert.match(viewSource, /editingSkillId \? t\('settings\.skills\.editTitle'\) : t\('settings\.skills\.new'\)/);
});

test("the Skills panel relies on the settings pane as its only vertical scroller", () => {
  // Since c6f9c9be installed skills render in .settings-ai-card-grid cards inside
  // .settings-ai-panel; none of these containers may become a nested vertical scroller.
  for (const selector of ["settings-ai-panel", "settings-ai-card-grid", "settings-ai-entry-card"]) {
    const rules = [...styleSource.matchAll(new RegExp(`\\.${selector}\\s*\\{([^}]*)\\}`, "g"))];
    assert.ok(rules.length > 0, `missing .${selector} rule`);
    for (const [, rule] of rules) {
      assert.doesNotMatch(rule, /max-height\s*:/, `.${selector} must not cap its height`);
      assert.doesNotMatch(rule, /overflow(?:-y)?\s*:\s*(?:auto|scroll)/, `.${selector} must not scroll`);
    }
  }

  const skillsPanel = viewSource.match(
    /v-else-if="aiTab === 'skills'"([\s\S]*?)v-else-if="aiTab === 'rules'"/
  )?.[1] ?? "";
  assert.match(skillsPanel, /v-for="skill in settings\.aiSkills"/);
  assert.doesNotMatch(skillsPanel, /<el-scrollbar/);
});
