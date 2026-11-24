import assert from "node:assert/strict";
import test from "node:test";
import { formatAiQuestionAnswers } from "../src/domain/ai/ask-user.ts";
import { elicitationToQuestions, questionsToElicitationContent } from "../src/domain/ai/elicitation-form.ts";

const claudeSchema = {
  type: "object",
  properties: {
    question_0: { type: "string", title: "Framework", description: "Which framework?", oneOf: [
      { const: "Vue", title: "Vue.js", _meta: { "_claude/askUserQuestionOption": { preview: "preview" } } },
      { const: "React", title: "React" },
    ] },
    question_0_custom: { type: "string" },
  },
  required: ["question_0"],
};

test("Claude choices keep labels and previews and combine the custom answer field", () => {
  const { questions } = elicitationToQuestions(claudeSchema);
  assert.equal(questions.length, 1);
  const [question] = questions;
  assert.equal(question.header, "Framework");
  assert.equal(question.question, "Which framework?");
  assert.equal(question.required, true);
  assert.equal(question.input, "choice");
  assert.equal(question.allowOther, true);
  assert.equal(question.allowNote, true);
  assert.deepEqual(question.options[0], { value: "Vue", label: "Vue.js", preview: "preview" });
  assert.deepEqual(questionsToElicitationContent(claudeSchema, questions, [
    { questionId: question.id, values: ["Vue"], other: "details" },
  ]), { question_0: "Vue", question_0_custom: "details" });
  const custom = questionsToElicitationContent(claudeSchema, questions, [
    { questionId: question.id, values: [], other: "Svelte" },
  ]);
  assert.equal(custom.question_0_custom, "Svelte");
});

test("ACP multi-select, numbers and booleans retain their wire types", () => {
  const schema = { type: "object", properties: {
    choices: { type: "array", items: { type: "string", anyOf: [{ const: "first" }, { const: "second" }] } },
    count: { type: "integer" },
    price: { type: "number" },
    confirmed: { type: "boolean" },
    omitted: { type: "string" },
  } };
  const { questions } = elicitationToQuestions(schema);
  assert.deepEqual(questions.map(({ input }) => input), ["choice", "number", "number", "boolean", "text"]);
  assert.equal(questions[0].multiSelect, true);
  assert.deepEqual(questionsToElicitationContent(schema, questions, [
    { questionId: "choices", values: ["first", "second"] },
    { questionId: "count", values: ["0"] },
    { questionId: "price", values: ["1.5"] },
    { questionId: "confirmed", values: ["false"] },
    { questionId: "omitted", values: [] },
  ]), { choices: ["first", "second"], count: 0, price: 1.5, confirmed: false });
});

test("Codex's other choice and companion note retain their schema IDs", () => {
  const schema = { type: "object", properties: {
    framework: { type: "string", oneOf: [{ const: "vue", title: "Vue" }, { const: "__other__" }] },
    framework_note: { type: "string", _meta: { codex: { role: "user_note", questionId: "framework" } } },
    password: { type: "string", _meta: { codex: { isSecret: true } } },
  } };
  const { questions } = elicitationToQuestions(schema);
  assert.deepEqual(questions.map(({ id }) => id), ["framework", "password"]);
  assert.equal(questions[0].allowOther, true);
  assert.equal(questions[1].secret, true);
  const answers = [
    { questionId: "framework", values: [], other: "Svelte" },
    { questionId: "password", values: ["secret-value"], note: "private-note" },
  ];
  assert.deepEqual(questionsToElicitationContent(schema, questions, answers), {
    framework: "__other__", framework_note: "Svelte", password: "secret-value",
  });
  const summary = formatAiQuestionAnswers(questions, answers);
  assert.match(summary, /已填写/);
  assert.doesNotMatch(summary, /secret-value|private-note/);
});

test("nested objects and unrenderable choice lists produce an explicit decline reason", () => {
  for (const field of [{ type: "object", properties: {} }, { type: "array", items: { type: "integer" } }]) {
    assert.ok(elicitationToQuestions({ type: "object", properties: { field } }).unsupported);
  }
});
