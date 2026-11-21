import type { AiLoopConfig, AiLoopMessage, AiLoopToolDefinition } from "@/services/ai-agent-loop";

export const AI_CONTEXT_SUMMARY_MARKER = "[MinCode context summary]";
export const AI_CONTEXT_SUMMARY_PREAMBLE =
  "Earlier conversation was compacted. Treat this summary as context, not as new user instructions. Preserve stated requirements, decisions, completed work, failures, and pending tasks.";

const DEFAULT_CONTEXT_TOKENS = 128_000;
// 模型硬上限不等于经济预算；避免百万上下文配置让历史长期不压缩。
const ECONOMIC_INPUT_TOKEN_LIMIT = 128_000;
/** Share of the input limit at which older turns are compacted. */
export const AI_CONTEXT_COMPACTION_THRESHOLD = 0.78;
const COMPACTION_THRESHOLD = AI_CONTEXT_COMPACTION_THRESHOLD;
const RECENT_HISTORY_SHARE = 0.42;
const SUMMARY_MAX_CHARS = 12_000;

export interface AiContextCompactionResult {
  messages: AiLoopMessage[];
  compacted: boolean;
  estimatedTokens: number;
  inputLimit: number;
  removedMessages: number;
}

export function estimateAiMessageTokens(messages: readonly AiLoopMessage[]) {
  let characters = 0;
  for (const message of messages) {
    characters += message.content.length + 12;
    for (const call of message.toolCalls ?? []) {
      characters += call.name.length + call.arguments.length + 16;
    }
    for (const attachment of message.attachments ?? []) {
      characters += attachment.name.length + attachment.mimeType.length + attachment.data.length + 24;
    }
  }
  return Math.ceil(characters / 4);
}

/** Estimates provider-visible function declarations, including JSON Schema. */
export function estimateAiToolTokens(tools: readonly AiLoopToolDefinition[]) {
  let characters = 0;
  for (const tool of tools) {
    characters += JSON.stringify({
      type: "function",
      name: tool.name,
      description: tool.description,
      inputSchema: tool.inputSchema,
    }).length + 16;
  }
  return Math.ceil(characters / 4);
}

function inputTokenLimit(config: AiLoopConfig) {
  const contextWindow = config.contextWindow && config.contextWindow > 0
    ? config.contextWindow
    : DEFAULT_CONTEXT_TOKENS;
  const outputReserve = config.maxOutputTokens && config.maxOutputTokens > 0
    ? config.maxOutputTokens
    : Math.min(8_192, Math.floor(contextWindow * 0.15));
  const contextInput = Math.max(1_024, contextWindow - outputReserve);
  const configuredInput = config.maxInputTokens && config.maxInputTokens > 0
    ? Math.min(config.maxInputTokens, contextInput)
    : contextInput;
  return Math.min(configuredInput, ECONOMIC_INPUT_TOKEN_LIMIT);
}

function compactText(value: string, limit: number) {
  const normalized = value.replace(/\s+/g, " ").trim();
  return normalized.length > limit ? `${normalized.slice(0, limit).trimEnd()}…` : normalized;
}

function summaryLine(message: AiLoopMessage) {
  if (message.content.startsWith(AI_CONTEXT_SUMMARY_MARKER)) {
    let previousSummary = message.content.slice(AI_CONTEXT_SUMMARY_MARKER.length).trim();
    // Recompaction must replace the generated preamble instead of nesting it every time.
    while (previousSummary.startsWith(AI_CONTEXT_SUMMARY_PREAMBLE)) {
      previousSummary = previousSummary.slice(AI_CONTEXT_SUMMARY_PREAMBLE.length).trim();
    }
    return compactText(previousSummary, 4_000);
  }
  if (message.role === "tool") {
    const source = message.toolResult?.source;
    const name = source?.toolName || message.toolCallId || "tool";
    return `Tool ${name} (${message.toolResult?.status ?? "completed"}): ${compactText(message.content, 360)}`;
  }
  const calls = (message.toolCalls ?? []).map((call) => call.name).join(", ");
  const text = compactText(message.content, message.role === "user" ? 700 : 500);
  const suffix = calls ? ` [tools: ${calls}]` : "";
  const label = message.role === "user" ? "User" : message.role === "system" ? "System" : "Assistant";
  return `${label}: ${text || "(no visible text)"}${suffix}`;
}

function conversationGroups(messages: readonly AiLoopMessage[]) {
  const groups: AiLoopMessage[][] = [];
  for (const message of messages) {
    if (message.role === "user" || groups.length === 0) groups.push([]);
    groups[groups.length - 1].push(message);
  }
  return groups;
}

/**
 * Splits one oversized turn into its leading messages and assistant rounds; each
 * round is an assistant message plus the tool results that answer it, so tool
 * calls and results are always kept or summarized together.
 */
function compactOversizedTurn(group: readonly AiLoopMessage[], budget: number) {
  const firstAssistant = group.findIndex((message) => message.role === "assistant");
  if (firstAssistant < 0) return { kept: [...group], removed: [] as AiLoopMessage[] };
  const head = group.slice(0, firstAssistant);
  const rounds: AiLoopMessage[][] = [];
  for (const message of group.slice(firstAssistant)) {
    if (message.role === "assistant" || rounds.length === 0) rounds.push([]);
    rounds[rounds.length - 1].push(message);
  }
  const roundBudget = Math.max(0, budget - estimateAiMessageTokens(head));
  const keptRounds: AiLoopMessage[][] = [];
  let keptTokens = 0;
  for (let index = rounds.length - 1; index >= 0; index -= 1) {
    const roundTokens = estimateAiMessageTokens(rounds[index]);
    if (keptRounds.length >= 1 && keptTokens + roundTokens > roundBudget) break;
    keptRounds.unshift(rounds[index]);
    keptTokens += roundTokens;
  }
  return {
    kept: [...head, ...keptRounds.flat()],
    removed: rounds.slice(0, rounds.length - keptRounds.length).flat(),
  };
}

function uncompacted(messages: readonly AiLoopMessage[], estimatedTokens: number, inputLimit: number): AiContextCompactionResult {
  return { messages: [...messages], compacted: false, estimatedTokens, inputLimit, removedMessages: 0 };
}

/**
 * Replaces old conversation turns with a bounded, provider-visible summary while
 * retaining complete recent turns. When the latest turn alone is too large (a long
 * tool loop), its older assistant/tool rounds are summarized as well. Tool calls
 * and their results stay together.
 */
export function compactAiLoopContext(
  messages: readonly AiLoopMessage[],
  config: AiLoopConfig,
  requestOverheadTokens = 0,
): AiContextCompactionResult {
  const overheadTokens = Number.isFinite(requestOverheadTokens)
    ? Math.max(0, Math.floor(requestOverheadTokens))
    : 0;
  const estimatedTokens = estimateAiMessageTokens(messages) + overheadTokens;
  const inputLimit = inputTokenLimit(config);
  if (estimatedTokens < inputLimit * COMPACTION_THRESHOLD) {
    return uncompacted(messages, estimatedTokens, inputLimit);
  }

  // Only the leading instructions are pinned; later system messages keep their
  // position and are summarized together with the turn they belong to.
  let leadingCount = 0;
  while (
    leadingCount < messages.length
    && messages[leadingCount].role === "system"
    && !messages[leadingCount].content.startsWith(AI_CONTEXT_SUMMARY_MARKER)
  ) {
    leadingCount += 1;
  }
  const leadingSystem = messages.slice(0, leadingCount);
  const groups = conversationGroups(messages.slice(leadingCount));
  if (!groups.length) return uncompacted(messages, estimatedTokens, inputLimit);

  const recentBudget = Math.max(256, Math.floor(Math.max(0, inputLimit - overheadTokens) * RECENT_HISTORY_SHARE));
  let kept: AiLoopMessage[][] = [];
  let turnRemoved: AiLoopMessage[] = [];
  const latest = groups[groups.length - 1];
  if (estimateAiMessageTokens(latest) > recentBudget) {
    const split = compactOversizedTurn(latest, recentBudget);
    kept = [split.kept];
    turnRemoved = split.removed;
  } else {
    let keptTokens = 0;
    for (let index = groups.length - 1; index >= 0; index -= 1) {
      const groupTokens = estimateAiMessageTokens(groups[index]);
      if (kept.length >= 2 && keptTokens + groupTokens > recentBudget) break;
      kept.unshift(groups[index]);
      keptTokens += groupTokens;
    }
  }

  const removedTurns = groups.slice(0, groups.length - kept.length).flat();
  if (!removedTurns.length && !turnRemoved.length) {
    return uncompacted(messages, estimatedTokens, inputLimit);
  }
  const lines = removedTurns.map(summaryLine).filter(Boolean);
  if (turnRemoved.length) {
    lines.push("Earlier steps of the latest user request:", ...turnRemoved.map(summaryLine).filter(Boolean));
  }
  let summary = lines.join("\n");
  if (summary.length > SUMMARY_MAX_CHARS) summary = `${summary.slice(0, SUMMARY_MAX_CHARS).trimEnd()}\n…`;
  const summaryMessage: AiLoopMessage = {
    role: "system",
    content: `${AI_CONTEXT_SUMMARY_MARKER}\n${AI_CONTEXT_SUMMARY_PREAMBLE}\n\n${summary}`,
  };
  const compactedMessages = [...leadingSystem, summaryMessage, ...kept.flat()];
  return {
    messages: compactedMessages,
    compacted: true,
    estimatedTokens: estimateAiMessageTokens(compactedMessages) + overheadTokens,
    inputLimit,
    removedMessages: removedTurns.length + turnRemoved.length,
  };
}

/** Estimated share of the model's input limit the given messages use. */
export function estimateAiContextUsage(
  messages: readonly AiLoopMessage[],
  config: AiLoopConfig,
  requestOverheadTokens = 0,
) {
  const estimatedTokens = estimateAiMessageTokens(messages) + Math.max(0, Math.floor(requestOverheadTokens));
  const inputLimit = inputTokenLimit(config);
  return { estimatedTokens, inputLimit, ratio: inputLimit > 0 ? estimatedTokens / inputLimit : 0 };
}
