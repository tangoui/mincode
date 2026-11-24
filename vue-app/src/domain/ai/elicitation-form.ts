import type { AiQuestion, AiQuestionAnswer, AiQuestionOption } from "./ask-user";

type Field = Record<string, unknown>;

interface Companion {
  id: string;
  kind: "claude" | "codex";
  secret: boolean;
}

function asRecord(value: unknown): Field | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Field : undefined;
}

function text(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined;
}

function codexMeta(field: Field | undefined): Field | undefined {
  return asRecord(asRecord(field?._meta)?.codex);
}

function companionFields(fields: Field): Map<string, Companion> {
  const companions = new Map<string, Companion>();
  for (const [id, value] of Object.entries(fields)) {
    const field = asRecord(value);
    if (field?.type !== "string" || field.oneOf !== undefined || field.enum !== undefined) continue;
    const meta = asRecord(field._meta);
    const codex = codexMeta(field);
    const air = asRecord(asRecord(meta?.jetbrains)?.air);
    const claude = asRecord(air?.customAnswer ?? meta?.["_jetbrains/air/custom_answer"]);
    const codexQuestionId = codex?.role === "user_note" ? text(codex.questionId) : undefined;
    const claudeQuestionId = text(claude?.questionId) ?? /^(question_\d+)_custom$/.exec(id)?.[1];
    const questionId = codexQuestionId ?? claudeQuestionId;
    if (!questionId || questionId === id || !Object.prototype.hasOwnProperty.call(fields, questionId)) continue;
    companions.set(questionId, { id, kind: codexQuestionId ? "codex" : "claude", secret: codex?.isSecret === true });
  }
  return companions;
}

function choiceOptions(field: Field, multiSelect: boolean): AiQuestionOption[] | undefined {
  const choices = field.oneOf ?? field.anyOf;
  if (choices === undefined && field.enum === undefined) return [];
  const options: AiQuestionOption[] = [];
  if (choices !== undefined) {
    if (!Array.isArray(choices) || !choices.length) return undefined;
    for (const entry of choices) {
      const option = asRecord(entry);
      if (typeof option?.const !== "string") return undefined;
      const label = text(option.title) ?? option.const;
      const description = text(option.description);
      const meta = asRecord(asRecord(option._meta)?.["_claude/askUserQuestionOption"]);
      const preview = multiSelect ? undefined : text(meta?.preview);
      options.push({ value: option.const, label, ...(description ? { description } : {}), ...(preview ? { preview } : {}) });
    }
  } else {
    if (!Array.isArray(field.enum) || !field.enum.length) return undefined;
    for (const [index, value] of field.enum.entries()) {
      if (typeof value !== "string") return undefined;
      const label = Array.isArray(field.enumNames) ? text(field.enumNames[index]) ?? value : value;
      options.push({ value, label });
    }
  }
  return new Set(options.map((option) => option.value)).size === options.length ? options : undefined;
}

function mapQuestion(id: string, field: Field, required: boolean, companion: Companion | undefined): AiQuestion | undefined {
  const title = text(field.title);
  const description = text(field.description);
  const multiSelect = field.type === "array";
  const question: AiQuestion = {
    id,
    header: (companion?.kind === "claude" ? title : description) ?? "",
    question: (companion?.kind === "claude" ? description ?? title : title ?? description) ?? id,
    input: "text",
    options: [],
    multiSelect,
    allowOther: false,
    allowNote: Boolean(companion),
    secret: codexMeta(field)?.isSecret === true || companion?.secret === true,
    required,
  };
  if (field.type === "number" || field.type === "integer") {
    return { ...question, input: "number", integer: field.type === "integer" };
  }
  if (field.type === "boolean") return { ...question, input: "boolean" };
  if (field.type !== "string" && !multiSelect) return undefined;
  const items = multiSelect ? asRecord(field.items) : field;
  if (!items || (items.type !== undefined && items.type !== "string")) return undefined;
  const options = choiceOptions(items, multiSelect);
  if (!options || (multiSelect && !options.length)) return undefined;
  if (!options.length) return question;
  const otherOption = companion?.kind === "codex" ? options.find((option) => /^__other(?:_\d+)?__$/.test(option.value)) : undefined;
  return {
    ...question,
    input: "choice",
    options: options.filter((option) => option !== otherOption),
    allowOther: companion?.kind === "claude" || Boolean(otherOption),
    ...(otherOption ? { otherOption } : {}),
  };
}

export function elicitationToQuestions(schema: unknown): { questions: AiQuestion[] } | { unsupported: string } {
  const form = asRecord(schema);
  const properties = asRecord(form?.properties);
  if (form?.type !== "object" || !properties) return { unsupported: "Expected an object form with properties" };
  const required = new Set(Array.isArray(form.required) ? form.required : []);
  const companions = companionFields(properties);
  const companionIds = new Set([...companions.values()].map(({ id }) => id));
  const questions: AiQuestion[] = [];
  for (const [id, value] of Object.entries(properties)) {
    if (companionIds.has(id)) {
      if (required.has(id)) return { unsupported: `Required companion field: ${id}` };
      continue;
    }
    const field = asRecord(value);
    const question = field && mapQuestion(id, field, required.has(id), companions.get(id));
    if (!question) return { unsupported: `Unsupported form field: ${id}` };
    questions.push(question);
  }
  return { questions };
}

export function questionsToElicitationContent(
  schema: unknown,
  questions: AiQuestion[],
  answers: AiQuestionAnswer[],
): Record<string, unknown> {
  const properties = asRecord(asRecord(schema)?.properties) ?? {};
  const companions = companionFields(properties);
  const byId = new Map(answers.map((answer) => [answer.questionId, answer]));
  const content: [string, unknown][] = [];
  for (const question of questions) {
    const answer = byId.get(question.id);
    if (!answer) continue;
    const values = [...answer.values];
    if (answer.other && question.otherOption) values.push(question.otherOption.value);
    const first = values[0];
    if (first !== undefined) {
      if (question.multiSelect) content.push([question.id, values]);
      else if (question.input === "number") content.push([question.id, Number(first)]);
      else if (question.input === "boolean") content.push([question.id, first === "true"]);
      else content.push([question.id, first]);
    }
    const companion = companions.get(question.id);
    if (companion) {
      const companionText = [answer.other, answer.note].filter(Boolean).join("\n");
      if (companionText) content.push([companion.id, companionText]);
    }
  }
  return Object.fromEntries(content);
}
