import type {
  AiLoopConfig,
  AiLoopMessage,
  AiLoopStreamEvent,
  streamAiLoopTurn,
} from "@/services/ai-agent-loop";
import { streamAiTurnWithFailover } from "@/services/ai-model-failover";

const PROMPT_OPTIMIZER_MAX_OUTPUT_TOKENS = 1_600;

const PROMPT_OPTIMIZER_SYSTEM_PROMPT = `你是提示词优化助手。请在不改变用户原始意图的前提下，重写用户提供的提示词：
1. 明确目标、必要上下文、约束、失败边界和预期输出；
2. 保留代码、路径、技术名词、业务条件以及用户明确提出的要求；
3. 不虚构缺失信息，不擅自扩大任务范围；
4. 如存在会影响正确性的关键歧义，将其整理为待确认问题；
5. 使用与原提示词相同的主要语言；
6. 只输出优化后的提示词，不解释优化过程，不添加 Markdown 代码围栏。`;

export interface OptimizeAiPromptOptions {
  config: AiLoopConfig;
  models: string[];
  prompt: string;
  signal?: AbortSignal;
  onFallback?: (from: string, to: string, error: Error) => void;
  streamTurn?: typeof streamAiLoopTurn;
}

export function promptOptimizationMessages(prompt: string): AiLoopMessage[] {
  return [
    { role: "system", content: PROMPT_OPTIMIZER_SYSTEM_PROMPT },
    { role: "user", content: `原始提示词：\n<original_prompt>\n${prompt}\n</original_prompt>` },
  ];
}

/**
 * 使用独立、无工具、无会话历史的流式请求优化输入框草稿。
 * 该请求不会读取附件或项目上下文，并通过 AbortSignal 支持真正取消。
 */
export async function optimizeAiPrompt(options: OptimizeAiPromptOptions) {
  const prompt = options.prompt.trim();
  if (!prompt) throw new Error("Prompt is empty.");

  let optimized = "";
  const configuredOutputTokens = options.config.maxOutputTokens;
  await streamAiTurnWithFailover({
    config: {
      ...options.config,
      // 提示词改写是短任务，不继承会显著增加首字延迟的显式推理档位。
      reasoningEffort: undefined,
      maxOutputTokens: configuredOutputTokens
        ? Math.min(configuredOutputTokens, PROMPT_OPTIMIZER_MAX_OUTPUT_TOKENS)
        : PROMPT_OPTIMIZER_MAX_OUTPUT_TOKENS,
    },
    models: options.models,
    messages: promptOptimizationMessages(prompt),
    tools: [],
    signal: options.signal,
    onFallback: options.onFallback,
    streamTurn: options.streamTurn,
    onEvent: (event: AiLoopStreamEvent) => {
      if (event.type === "chunk" && event.text) optimized += event.text;
    },
  });

  const result = optimized.trim();
  if (!result) throw new Error("AI prompt optimizer returned no text.");
  return result;
}
