import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

import {
  aiRuleAppliesToWorkspace,
  applicableAiRules,
} from "../src/domain/ai/rules.ts";

const read = (path) => readFile(new URL(path, import.meta.url), "utf8");

const rules = [
  { id: "legacy", title: "Legacy", content: "legacy", enabled: true },
  { id: "global", title: "Global", content: "global", enabled: true, scope: "global" },
  {
    id: "workspace-a",
    title: "Workspace A",
    content: "workspace-a",
    enabled: true,
    scope: "workspace",
    workspaceId: "workspace:a",
  },
  {
    id: "workspace-b",
    title: "Workspace B",
    content: "workspace-b",
    enabled: true,
    scope: "workspace",
    workspaceId: "workspace:b",
  },
];

test("旧规则兼容为全局规则，工作区规则仅匹配所属工作区", () => {
  assert.equal(aiRuleAppliesToWorkspace(rules[0], "workspace:b"), true);
  assert.equal(aiRuleAppliesToWorkspace(rules[2], "workspace:a"), true);
  assert.equal(aiRuleAppliesToWorkspace(rules[2], "workspace:b"), false);
  assert.deepEqual(
    applicableAiRules(rules, "workspace:a").map((rule) => rule.id),
    ["legacy", "global", "workspace-a"],
  );
});

test("规则设置和两条 AI 注入链统一使用会话工作区作用域", async () => {
  const [settings, drawer, zh, en] = await Promise.all([
    read("../src/views/SettingsWorkspaceView.vue"),
    read("../src/components/ai/AiAssistantDrawer.vue"),
    read("../src/i18n/locales/zh.ts"),
    read("../src/i18n/locales/en.ts"),
  ]);

  assert.match(settings, /ruleScope = ref<AiRuleScope>\("global"\)/);
  assert.match(settings, /ruleWorkspaceId = ref\(workbench\.activeWorkspaceId\)/);
  assert.match(settings, /workbench\.workspaces\.map\(\(workspace\) =>/);
  assert.match(settings, /applicableAiRules\(settings\.aiRules, availableRuleWorkspaceId\(\), ruleScope\.value\)/);
  assert.match(settings, /v-model="ruleWorkspaceId"/);
  assert.match(settings, /rule\.scope = ruleScope\.value/);
  assert.match(settings, /rule\.workspaceId = workspaceId/);
  assert.match(settings, /workspaceId,/);
  assert.match(settings, /const workspaceId = ruleScope\.value === "workspace" \? availableRuleWorkspaceId\(\) : undefined/);
  assert.match(drawer, /enabledAiContext\(workspaceId: string, includeRules = true\)/);
  assert.match(drawer, /enabledAiContext\(runContext\.workspaceId\)/);
  assert.match(drawer, /acpPromptText\(text, conversationId, runContext\.workspaceId\)/);
  assert.match(zh, /工作区规则仅对所选工作区生效/);
  assert.match(zh, /"settings\.rules\.workspace": "工作区"/);
  assert.match(zh, /"settings\.rules\.selectWorkspace": "选择工作区"/);
  assert.match(en, /workspace rules apply only to the selected workspace/);
  assert.match(en, /"settings\.rules\.workspace": "Workspace"/);
  assert.match(en, /"settings\.rules\.selectWorkspace": "Select workspace"/);
});
