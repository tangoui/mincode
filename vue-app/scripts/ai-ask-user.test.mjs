import assert from "node:assert/strict";
import test from "node:test";
import { formatAiQuestionAnswers, parseAskUserInput } from "../src/domain/ai/ask-user.ts";

const question = { header: "框架", question: "用哪个？" };
const options = [{ label: "Vue", preview: "preview" }, { label: "React" }];

test("native questions support free text, previews and multiple choices", () => {
  const [text, single, multi] = parseAskUserInput({ questions: [
    question, { ...question, options }, { ...question, options, multiSelect: true, allowOther: false },
  ] });
  assert.equal(text.input, "text");
  assert.equal(single.allowOther, true);
  assert.equal(single.options[0].preview, "preview");
  assert.equal(multi.allowOther, false);
  assert.equal(multi.multiSelect, true);
  assert.equal(multi.options[0].preview, undefined);
});

test("invalid questions report which rule was violated", () => {
  for (const invalid of [[], Array(5).fill(question)]) {
    assert.throws(() => parseAskUserInput({ questions: invalid }), /1.*4/);
  }
  for (const invalid of [[options[0]], Array(5).fill(options[0])]) {
    assert.throws(() => parseAskUserInput({ questions: [{ ...question, options: invalid }] }), /2.*4/);
  }
  assert.throws(() => parseAskUserInput({ questions: [{ ...question, header: "长".repeat(13) }] }), /header.*12/);
  assert.throws(() => parseAskUserInput({ questions: [{ ...question, question: "" }] }), /question/);
});

test("answers use option labels, verbatim custom input and per-question notes", () => {
  const questions = parseAskUserInput({ questions: [
    { ...question, options },
    { header: "模块", question: "选哪些？", options: [{ label: "A" }, { label: "B" }], multiSelect: true },
    { header: "语言", question: "用什么？", options },
  ] });
  assert.equal(formatAiQuestionAnswers(questions, [
    { questionId: questions[0].id, values: ["Vue"] },
    { questionId: questions[1].id, values: ["A", "B"] },
    { questionId: questions[2].id, values: [], other: "  自定义  ", note: "已有依赖" },
  ]), "【框架】用哪个？ → Vue\n【模块】选哪些？ → A、B\n【语言】用什么？ →   自定义  （备注：已有依赖）");
});
