import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { URL } from "node:url";

import {
  buildSelectedAiSkillsContext,
  selectAiSkillsForPrompt,
} from "../src/domain/ai/skill-context.ts";

const skills = [
  {
    id: "review",
    name: "代码审查助手",
    description: "系统化审查代码变更，检查正确性、安全性、性能和测试",
    content: "REVIEW_SKILL_BODY",
    enabled: true,
  },
  {
    id: "debug",
    name: "系统化调试",
    description: "使用复现、观测、假设、验证流程定位 Bug 根因",
    content: "DEBUG_SKILL_BODY",
    enabled: true,
  },
  {
    id: "disabled",
    name: "禁用技能",
    description: "不会加载",
    content: "DISABLED_SKILL_BODY",
    enabled: false,
  },
];

test("普通问候不加载任何技能正文", () => {
  assert.deepEqual(selectAiSkillsForPrompt(skills, "你好"), []);
  assert.equal(buildSelectedAiSkillsContext(skills, "你好"), "");
});

test("显式 /skill 调用只加载指定技能", () => {
  assert.deepEqual(
    selectAiSkillsForPrompt(skills, "/skill 系统化调试 帮我排查报错").map((skill) => skill.id),
    ["debug"],
  );
  const context = buildSelectedAiSkillsContext(skills, "/skill 系统化调试 帮我排查报错");
  assert.match(context, /DEBUG_SKILL_BODY/);
  assert.doesNotMatch(context, /REVIEW_SKILL_BODY/);
});

test("自然语言按名称或描述关键词最多加载一个最相关技能", () => {
  assert.deepEqual(
    selectAiSkillsForPrompt(skills, "请系统化审查这段代码的安全性和性能").map(
      (skill) => skill.id,
    ),
    ["review"],
  );
});

test("未知或禁用的显式技能不会回退为全量加载", () => {
  assert.deepEqual(selectAiSkillsForPrompt(skills, "/skill 不存在的技能 做点事"), []);
  assert.deepEqual(selectAiSkillsForPrompt(skills, "/skill 禁用技能 做点事"), []);
});


test("内置 Agent 与 ACP 两条发送链都按当前请求选择技能", async () => {
  const drawer = await readFile(
    new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url),
    "utf8",
  );
  assert.match(
    drawer,
    /enabledAiContext\(runContext\.workspaceId, text\)/,
  );
  assert.match(
    drawer,
    /const context = enabledAiContext\(\s*workspaceId,\s*text,\s*!acpContextInjected\.has\(conversationId\),\s*\)/,
  );
});

 test("显式技能支持换行、多空白、ID 与边界匹配", () => {
  for (const prompt of ["/skill 系统化调试\n排查错误", "/skill debug\t排查错误"]) {
    assert.deepEqual(selectAiSkillsForPrompt(skills, prompt).map(s => s.id), ["debug"]);
  }
  assert.deepEqual(selectAiSkillsForPrompt(skills, "/skill debugger"), []);
  assert.deepEqual(selectAiSkillsForPrompt([null, ...skills], "你好"), []);
});
