export interface AiTokenUsage {
  inputTokens: number;
  outputTokens: number;
  cachedInputTokens: number;
  cacheWriteTokens: number;
  reasoningTokens: number;
  /** Number of model requests that returned provider usage metadata. */
  requestCount: number;
  /** Number of model requests actually attempted, including failed requests and failover. */
  attemptedRequestCount: number;
  models?: string[];
  /** Model of the request that produced the final reply (the last attempt of a successful run). */
  answeredBy?: string;
  /** Prompt size of the turn's last model request as reported by the provider, i.e. the context in use. */
  contextTokens?: number;
}

function tokenCount(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) && value >= 0
    ? Math.floor(value)
    : 0;
}

export function normalizeAiTokenUsage(value: unknown): AiTokenUsage | undefined {
  if (!value || typeof value !== "object") return undefined;
  const raw = value as Partial<AiTokenUsage>;
  const hasProviderTotals = tokenCount(raw.inputTokens) || tokenCount(raw.outputTokens)
    || tokenCount(raw.cachedInputTokens) || tokenCount(raw.cacheWriteTokens) || tokenCount(raw.reasoningTokens);
  // Usage events historically implied one request even if requestCount was absent.
  const requestCount = raw.requestCount === undefined && hasProviderTotals ? 1 : tokenCount(raw.requestCount);
  // Legacy records only stored requestCount. Treat those requests as fully reported.
  const attemptedRequestCount = raw.attemptedRequestCount === undefined
    ? requestCount
    : Math.max(requestCount, tokenCount(raw.attemptedRequestCount));
  const usage: AiTokenUsage = {
    inputTokens: tokenCount(raw.inputTokens),
    outputTokens: tokenCount(raw.outputTokens),
    cachedInputTokens: tokenCount(raw.cachedInputTokens),
    cacheWriteTokens: tokenCount(raw.cacheWriteTokens),
    reasoningTokens: tokenCount(raw.reasoningTokens),
    requestCount,
    attemptedRequestCount,
    models: Array.isArray(raw.models)
      ? [...new Set(raw.models.filter((model): model is string => typeof model === "string" && Boolean(model.trim())).map((model) => model.trim()))]
      : undefined,
    answeredBy: typeof raw.answeredBy === "string" && raw.answeredBy.trim() ? raw.answeredBy.trim() : undefined,
    contextTokens: tokenCount(raw.contextTokens) || undefined,
  };
  return usage.inputTokens || usage.outputTokens || usage.cachedInputTokens
    || usage.cacheWriteTokens || usage.reasoningTokens || usage.attemptedRequestCount
    ? usage
    : undefined;
}

export function mergeAiTokenUsage(
  current: AiTokenUsage | undefined,
  value: AiTokenUsage,
): AiTokenUsage {
  const next = normalizeAiTokenUsage(value) ?? {
    inputTokens: 0,
    outputTokens: 0,
    cachedInputTokens: 0,
    cacheWriteTokens: 0,
    reasoningTokens: 0,
    requestCount: 0,
    attemptedRequestCount: 0,
  };
  if (!current) return next;
  return {
    inputTokens: current.inputTokens + next.inputTokens,
    outputTokens: current.outputTokens + next.outputTokens,
    cachedInputTokens: current.cachedInputTokens + next.cachedInputTokens,
    cacheWriteTokens: current.cacheWriteTokens + next.cacheWriteTokens,
    reasoningTokens: current.reasoningTokens + next.reasoningTokens,
    requestCount: current.requestCount + next.requestCount,
    attemptedRequestCount: current.attemptedRequestCount + next.attemptedRequestCount,
    models: [...new Set([...(current.models ?? []), ...(next.models ?? [])])],
    answeredBy: next.answeredBy ?? current.answeredBy,
    contextTokens: next.contextTokens ?? current.contextTokens,
  };
}

/** Applies independently observed request attempts without changing provider token totals. */
export function withAiTokenRequestStats(
  usage: AiTokenUsage | undefined,
  attemptedRequestCount: number,
  models: string[] = [],
): AiTokenUsage | undefined {
  const normalized = normalizeAiTokenUsage(usage) ?? {
    inputTokens: 0,
    outputTokens: 0,
    cachedInputTokens: 0,
    cacheWriteTokens: 0,
    reasoningTokens: 0,
    requestCount: 0,
    attemptedRequestCount: 0,
  };
  return normalizeAiTokenUsage({
    ...normalized,
    attemptedRequestCount: Math.max(normalized.requestCount, tokenCount(attemptedRequestCount)),
    models: [...new Set([...(normalized.models ?? []), ...models])],
    answeredBy: normalized.answeredBy,
    contextTokens: normalized.contextTokens,
  });
}

export function unreportedAiTokenRequestCount(usage: AiTokenUsage) {
  return Math.max(0, usage.attemptedRequestCount - usage.requestCount);
}

export function compactTokenCount(value: number) {
  if (value < 1_000) return String(value);
  if (value < 1_000_000) return `${(value / 1_000).toFixed(value < 10_000 ? 1 : 0)}K`;
  return `${(value / 1_000_000).toFixed(value < 10_000_000 ? 1 : 0)}M`;
}

/**
 * The model to show for a reply: the recorded answering model, or the only model
 * involved for replies stored before that was recorded.
 */
export function aiReplyModel(usage: AiTokenUsage | undefined) {
  if (usage?.answeredBy) return usage.answeredBy;
  return usage?.models?.length === 1 ? usage.models[0] : undefined;
}
