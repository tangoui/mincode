export interface AiAgentEventTiming {
  kind?: string;
  status?: string;
  startedAt?: number;
  finishedAt?: number;
}

const RUNNING_EVENT_STATUSES = new Set(["running", "pending", "in_progress"]);

export function normalizeAiEventTimestamp(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) && value > 0
    ? Math.trunc(value)
    : undefined;
}

export function isAiAgentEventRunning(event: AiAgentEventTiming) {
  const status = event.status?.trim().toLowerCase();
  return status
    ? RUNNING_EVENT_STATUSES.has(status)
    : event.kind === "tool";
}

/**
 * Applies stable timestamps while an event is repeatedly upserted by a stream.
 * Existing records without timestamps stay without a fabricated duration when
 * they are merely settled during history restoration.
 */
export function applyAiAgentEventTiming<T extends AiAgentEventTiming>(
  previous: T | undefined,
  incoming: T,
  now = Date.now(),
): T {
  const timestamp = normalizeAiEventTimestamp(now) ?? Date.now();
  const previousStartedAt = normalizeAiEventTimestamp(previous?.startedAt);
  const incomingStartedAt = normalizeAiEventTimestamp(incoming.startedAt);
  const previousFinishedAt = normalizeAiEventTimestamp(previous?.finishedAt);
  const incomingFinishedAt = normalizeAiEventTimestamp(incoming.finishedAt);
  const running = isAiAgentEventRunning(incoming);

  if (running) {
    const continuing = previous && isAiAgentEventRunning(previous);
    return {
      ...incoming,
      startedAt: continuing
        ? previousStartedAt ?? incomingStartedAt ?? timestamp
        : incomingStartedAt ?? timestamp,
      finishedAt: undefined,
    };
  }

  // A newly observed one-shot event starts and finishes at this write, while
  // restored legacy history bypasses this function and remains timeless.
  const startedAt = previous
    ? previousStartedAt ?? incomingStartedAt
    : incomingStartedAt ?? timestamp;
  const finishedAt = startedAt === undefined
    ? undefined
    : previous && !isAiAgentEventRunning(previous)
      ? previousFinishedAt ?? incomingFinishedAt ?? timestamp
      : incomingFinishedAt ?? timestamp;
  return {
    ...incoming,
    startedAt,
    finishedAt,
  };
}

export function aiAgentEventDurationMs(
  event: AiAgentEventTiming,
  now = Date.now(),
) {
  const startedAt = normalizeAiEventTimestamp(event.startedAt);
  if (startedAt === undefined) return undefined;
  const finishedAt = isAiAgentEventRunning(event)
    ? normalizeAiEventTimestamp(now)
    : normalizeAiEventTimestamp(event.finishedAt);
  if (finishedAt === undefined) return undefined;
  return Math.max(0, finishedAt - startedAt);
}

export function aiAgentEventGroupDurationMs(
  events: AiAgentEventTiming[],
  now = Date.now(),
) {
  const startedAt = events
    .map((event) => normalizeAiEventTimestamp(event.startedAt))
    .filter((value): value is number => value !== undefined);
  if (!startedAt.length) return undefined;
  const firstStartedAt = Math.min(...startedAt);
  if (events.some(isAiAgentEventRunning)) {
    return Math.max(0, (normalizeAiEventTimestamp(now) ?? Date.now()) - firstStartedAt);
  }
  const finishedAt = events
    .map((event) => normalizeAiEventTimestamp(event.finishedAt))
    .filter((value): value is number => value !== undefined);
  if (!finishedAt.length) return undefined;
  return Math.max(0, Math.max(...finishedAt) - firstStartedAt);
}

export function formatAiAgentEventDuration(durationMs: number | undefined) {
  if (durationMs === undefined || !Number.isFinite(durationMs)) return "";
  return `${Math.max(0, Math.floor(durationMs / 1_000))}s`;
}
