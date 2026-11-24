export const ASK_USER_TOOL_NAME = "ask_user";
export const ASK_USER_SKIPPED_RESULT = "用户跳过了提问。请按你的最佳判断继续，并在回复里说明你的假设。";

export type AiQuestionInput = "choice" | "text" | "number" | "boolean";

export interface AiQuestionOption {
  value: string;
  label: string;
  description?: string;
  preview?: string;
}

export interface AiQuestion {
  id: string;
  header: string;
  question: string;
  input: AiQuestionInput;
  options: AiQuestionOption[];
  multiSelect: boolean;
  allowOther: boolean;
  secret: boolean;
  required: boolean;
  allowNote?: boolean;
  otherOption?: AiQuestionOption;
  integer?: boolean;
}

export interface AiQuestionAnswer {
  questionId: string;
  values: string[];
  other?: string;
  note?: string;
}

const MAX_QUESTIONS = 4;
const MAX_HEADER_LENGTH = 12;
const MIN_OPTIONS = 2;
const MAX_OPTIONS = 4;

export function askUserInputSchema() {
  return {
    type: "object",
    required: ["questions"],
    additionalProperties: false,
    properties: {
      questions: {
        type: "array",
        minItems: 1,
        maxItems: MAX_QUESTIONS,
        items: {
          type: "object",
          required: ["header", "question"],
          additionalProperties: false,
          properties: {
            header: { type: "string", minLength: 1, maxLength: MAX_HEADER_LENGTH, description: "Short label for the question." },
            question: { type: "string", minLength: 1, description: "The full question." },
            options: {
              type: "array",
              minItems: MIN_OPTIONS,
              maxItems: MAX_OPTIONS,
              description: "Omit this for a free-text question.",
              items: {
                type: "object",
                required: ["label"],
                additionalProperties: false,
                properties: {
                  label: { type: "string", minLength: 1 },
                  description: { type: "string" },
                  preview: { type: "string", description: "Plain-text preview for a single-select option." },
                },
              },
            },
            multiSelect: { type: "boolean", default: false },
            allowOther: { type: "boolean", default: true },
          },
        },
      },
    },
  };
}

function inputObject(value: unknown, path: string): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`${path} must be an object`);
  return value as Record<string, unknown>;
}

function requiredText(value: unknown, path: string): string {
  if (typeof value !== "string" || !value.trim()) throw new Error(`${path} must be a non-empty string`);
  return value.trim();
}

function optionalText(value: unknown, path: string): string | undefined {
  if (value !== undefined && typeof value !== "string") throw new Error(`${path} must be a string`);
  return value;
}

function optionalBoolean(value: unknown, path: string, fallback: boolean): boolean {
  if (value === undefined) return fallback;
  if (typeof value !== "boolean") throw new Error(`${path} must be a boolean`);
  return value;
}

export function parseAskUserInput(input: Record<string, unknown>): AiQuestion[] {
  const { questions } = input;
  if (!Array.isArray(questions) || questions.length < 1 || questions.length > MAX_QUESTIONS) {
    throw new Error(`questions must contain 1 to ${MAX_QUESTIONS} questions`);
  }
  return questions.map((value: unknown, index) => {
    const path = `questions[${index}]`;
    const field = inputObject(value, path);
    const header = requiredText(field.header, `${path}.header`);
    if ([...header].length > MAX_HEADER_LENGTH) {
      throw new Error(`${path}.header must be at most ${MAX_HEADER_LENGTH} characters`);
    }
    const question = requiredText(field.question, `${path}.question`);
    const multiSelect = optionalBoolean(field.multiSelect, `${path}.multiSelect`, false);
    const allowOther = optionalBoolean(field.allowOther, `${path}.allowOther`, true);
    const options: AiQuestionOption[] = [];
    if (field.options !== undefined) {
      if (!Array.isArray(field.options) || field.options.length < MIN_OPTIONS || field.options.length > MAX_OPTIONS) {
        throw new Error(`${path}.options must contain ${MIN_OPTIONS} to ${MAX_OPTIONS} options when provided`);
      }
      for (const [optionIndex, value] of field.options.entries()) {
        const optionPath = `${path}.options[${optionIndex}]`;
        const option = inputObject(value, optionPath);
        const label = requiredText(option.label, `${optionPath}.label`);
        if (options.some((previous) => previous.value === label)) throw new Error(`${optionPath}.label must be unique`);
        const description = optionalText(option.description, `${optionPath}.description`);
        const preview = multiSelect ? undefined : optionalText(option.preview, `${optionPath}.preview`);
        options.push({ value: label, label, ...(description ? { description } : {}), ...(preview ? { preview } : {}) });
      }
    }
    return {
      id: `question-${index}`,
      header,
      question,
      input: options.length ? "choice" : "text",
      options,
      multiSelect: options.length > 0 && multiSelect,
      allowOther: options.length > 0 && allowOther,
      secret: false,
      required: true,
    };
  });
}

export function formatAiQuestionAnswers(
  questions: AiQuestion[],
  answers: AiQuestionAnswer[],
  labels = { filled: "已填写", yes: "是", no: "否", note: (text: string) => `（备注：${text}）` },
): string {
  const byId = new Map(answers.map((answer) => [answer.questionId, answer]));
  return questions.flatMap((question) => {
    const answer = byId.get(question.id);
    if (!answer) return [];
    const values = answer.values.map((value) => {
      if (question.input === "boolean") return value === "true" ? labels.yes : labels.no;
      return question.options.find((option) => option.value === value)?.label ?? value;
    });
    if (answer.other) values.push(answer.other);
    if (!values.length && !answer.note) return [];
    const response = question.secret ? labels.filled : values.join("、") + (answer.note ? labels.note(answer.note) : "");
    const heading = question.header ? `【${question.header}】` : "";
    return [`${heading}${question.question} → ${response}`];
  }).join("\n");
}

export function askUserGuidance(): string {
  return [
    "Use ask_user when multiple reasonable options have meaningfully different consequences or when you need information only the user knows.",
    "Use the code, conversation context, and established conventions for questions you can resolve yourself.",
    "Group related questions in a single call with up to four questions. Put the recommended option first and suffix its label with '(Recommended)' in the user's language.",
    "Use the user's language for questions and choices. Use the approval flow for permission to proceed.",
  ].join("\n");
}
