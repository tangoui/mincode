import type {
  AiLoopConfig,
  AiLoopMessage,
  AiLoopStreamEvent,
  streamAiLoopTurn,
} from "@/services/ai-agent-loop";
import { streamAiTurnWithFailover } from "@/services/ai-model-failover";

// Reasoning models count their thinking against this cap; a tighter cap can be
// used up before any visible text, while a short message stops well below it.
const COMMIT_MESSAGE_MAX_OUTPUT_TOKENS = 2048;

const COMMIT_MESSAGE_SYSTEM_PROMPT = `你是专业的 Git 提交信息生成助手。根据 staged diff 生成符合 Conventional Commits 规范的 commit message。
格式为 type(scope): description，type 可使用 feat、fix、docs、style、refactor、test、chore、perf。
第一行不超过 72 个字符；必要时空一行后添加简洁正文。
只描述 diff 中的实际变更。只输出 commit message，不要解释、引号或 Markdown。`;

export interface GenerateAiCommitMessageOptions {
  config: AiLoopConfig;
  /** Attempt order, first model first; defaults to the config's model. */
  models?: string[];
  patch: string;
  signal?: AbortSignal;
  onDelta?: (delta: string) => void;
  /** A thinking model sent reasoning (never part of the message): the request is alive. */
  onReasoning?: () => void;
  /**
   * Stop when the stream sends nothing for this long. Reasoning counts as activity,
   * so a thinking model is not cut off before its first visible token.
   */
  idleTimeoutMs?: number;
  streamTurn?: typeof streamAiLoopTurn;
}

export function commitMessagePrompt(patch: string): AiLoopMessage[] {
  return [
    { role: "system", content: COMMIT_MESSAGE_SYSTEM_PROMPT },
    {
      role: "user",
      content: `请根据以下 staged 变更摘要和紧凑 diff 生成 commit message：\n\n${patch.slice(0, 12_000)}`,
    },
  ];
}

function cleanCommitMessage(value: string) {
  const trimmed = value.trim();
  if (!trimmed.startsWith("```")) return trimmed;
  return trimmed
    .replace(/^```[^\n]*\n?/, "")
    .replace(/\n?```$/, "")
    .trim();
}

/**
 * Omitting the effort does not disable reasoning: OpenAI-style reasoning models then
 * think at their default (medium) level before the first visible token, which made
 * generation feel frozen. Ask for low effort there. Anthropic only thinks when an
 * effort is set, so it gets none.
 */
export function commitMessageReasoningEffort(config: Pick<AiLoopConfig, "apiFormat">) {
  return config.apiFormat === "anthropic" ? undefined : "low";
}

/** Models without reasoning support reject the parameter; retry those without it. */
function rejectsReasoningEffort(error: unknown) {
  const message = error instanceof Error ? error.message : String(error);
  return /reasoning/i.test(message) && /\b(400|422)\b|unsupported|not supported|unknown|invalid|unrecognized/i.test(message);
}

/** 使用无工具、无历史的流式短请求生成提交信息，首个文本片段可立即展示。 */
export async function generateAiCommitMessage(options: GenerateAiCommitMessageOptions) {
  const patch = options.patch.trim();
  if (!patch) throw new Error("Staged changes are empty.");

  let generated = "";
  const stream = (reasoningEffort: AiLoopConfig["reasoningEffort"]) => streamAiTurnWithFailover({
    config: {
      ...options.config,
      reasoningEffort,
      maxOutputTokens: Math.min(
        options.config.maxOutputTokens || COMMIT_MESSAGE_MAX_OUTPUT_TOKENS,
        COMMIT_MESSAGE_MAX_OUTPUT_TOKENS,
      ),
    },
    models: options.models?.length ? options.models : [options.config.model],
    messages: commitMessagePrompt(patch),
    tools: [],
    signal: options.signal,
    idleTimeoutMs: options.idleTimeoutMs,
    streamTurn: options.streamTurn,
    onEvent: (event: AiLoopStreamEvent) => {
      if (event.type === "reasoning") {
        options.onReasoning?.();
        return;
      }
      if (event.type !== "chunk" || !event.text) return;
      generated += event.text;
      options.onDelta?.(event.text);
    },
  });

  const reasoningEffort = commitMessageReasoningEffort(options.config);
  try {
    await stream(reasoningEffort);
  } catch (error) {
    if (!reasoningEffort || generated || options.signal?.aborted || !rejectsReasoningEffort(error)) throw error;
    await stream(undefined);
  }

  const result = cleanCommitMessage(generated);
  if (!result) throw new Error("AI did not return a commit message.");
  return result;
}
