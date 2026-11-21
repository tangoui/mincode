import type {
  AiLoopConfig,
  AiLoopMessage,
  AiLoopStreamEvent,
  AiLoopToolDefinition,
} from "@/services/ai-agent-loop";
import { streamAiLoopTurn } from "@/services/ai-agent-loop";

export interface StreamAiTurnWithFailoverOptions {
  config: AiLoopConfig;
  models: string[];
  messages: AiLoopMessage[];
  tools: AiLoopToolDefinition[];
  onEvent: (event: AiLoopStreamEvent) => void;
  signal?: AbortSignal;
  onFallback?: (from: string, to: string, error: Error) => void;
  onAttempt?: (model: string) => void;
  /** Optional per-model no-output timeout; omitted for normal parent-agent requests. */
  idleTimeoutMs?: number;
  streamTurn?: typeof streamAiLoopTurn;
}

function asError(value: unknown) {
  return value instanceof Error ? value : new Error(String(value));
}

function isAbortError(value: unknown) {
  return value instanceof DOMException && value.name === "AbortError";
}

function hasVisibleOutput(event: AiLoopStreamEvent) {
  return event.type === "chunk" || event.type === "reasoning" || event.type === "tool_call" || event.type === "truncated";
}

/**
 * Retries an untouched AI request with explicitly configured backup models.
 * Once a model produces any visible output, retries stop to avoid duplicating a
 * partial answer or tool call in the conversation.
 */
export async function streamAiTurnWithFailover(options: StreamAiTurnWithFailoverOptions) {
  const models = [...new Set(options.models.map((model) => model.trim()).filter(Boolean))];
  const streamTurn = options.streamTurn ?? streamAiLoopTurn;
  if (!models.length) throw new Error("No AI model is configured for this profile.");

  let lastError: Error | undefined;
  for (const [index, model] of models.entries()) {
    if (options.signal?.aborted) throw new DOMException("The AI request was cancelled.", "AbortError");
    let receivedOutput = false;
    options.onAttempt?.(model);
    try {
      await streamTurn({ ...options.config, model }, options.messages, options.tools, (event) => {
        receivedOutput ||= hasVisibleOutput(event);
        options.onEvent(event);
      }, options.signal, options.idleTimeoutMs);
      return;
    } catch (error) {
      const requestError = asError(error);
      lastError = requestError;
      const nextModel = models[index + 1];
      if (isAbortError(error) || receivedOutput || !nextModel) throw requestError;
      options.onFallback?.(model, nextModel, requestError);
    }
  }

  throw lastError ?? new Error("AI stream failed.");
}
