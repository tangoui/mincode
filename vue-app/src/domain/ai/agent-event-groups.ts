import type {
  AiAssistantEvent,
  AiAssistantMessage,
} from "@/stores/ai-assistant";

export interface AiAnchoredEventGroup {
  anchorIndex: number;
  anchorIndexes: number[];
  events: AiAssistantEvent[];
}

export interface AiAgentEventStatusSummary {
  succeeded: number;
  failed: number;
  running: number;
}

const RUNNING_EVENT_STATUSES = new Set(["running", "pending", "in_progress"]);
const FAILED_EVENT_STATUSES = new Set(["failed", "error", "cancelled"]);

function eventStatus(event: AiAssistantEvent) {
  return event.status ?? (event.kind === "tool" ? "pending" : "completed");
}

/**
 * Moves the events attached to a message that is about to be removed onto the message replacing
 * it (a task summary replacing the streamed reply). Without this they lose their anchor and the
 * next send pulls them into the following turn.
 */
export function reanchorAiEvents(
  events: AiAssistantEvent[],
  fromMessageId: string,
  toMessageId: string,
): AiAssistantEvent[] {
  if (!events.some((event) => event.triggerMessageId === fromMessageId)) return events;
  return events.map((event) =>
    event.triggerMessageId === fromMessageId ? { ...event, triggerMessageId: toMessageId } : event,
  );
}

export function summarizeAiAgentEventStatuses(
  events: AiAssistantEvent[],
): AiAgentEventStatusSummary {
  const summary: AiAgentEventStatusSummary = {
    succeeded: 0,
    failed: 0,
    running: 0,
  };
  for (const event of events) {
    const status = eventStatus(event);
    if (RUNNING_EVENT_STATUSES.has(status)) summary.running += 1;
    else if (FAILED_EVENT_STATUSES.has(status)) summary.failed += 1;
    else summary.succeeded += 1;
  }
  return summary;
}

/**
 * Assigns orphaned activity to the turn that is about to execute. Events with a
 * valid current or legacy anchor keep their original timeline position.
 */
export function anchorUnassignedAiEvents(
  messages: AiAssistantMessage[],
  events: AiAssistantEvent[],
  triggerMessageId: string,
) {
  const messageIds = new Set(
    messages.flatMap((message) => message.id ? [message.id] : []),
  );
  return events.map((event) => {
    const hasCurrentAnchor = Boolean(
      event.triggerMessageId && messageIds.has(event.triggerMessageId),
    );
    const hasLegacyAnchor = Number.isInteger(event.afterMessageIndex)
      && event.afterMessageIndex! >= 0
      && event.afterMessageIndex! < messages.length;
    if (hasCurrentAnchor || hasLegacyAnchor) return event;
    return { ...event, triggerMessageId, afterMessageIndex: undefined };
  });
}

function messageIndexById(messages: AiAssistantMessage[]) {
  const indexes = new Map<string, number>();
  for (let index = 0; index < messages.length; index += 1) {
    const id = messages[index]?.id;
    if (id) indexes.set(id, index);
  }
  return indexes;
}

function anchorIndexFor(
  event: AiAssistantEvent,
  messageIndexes: ReadonlyMap<string, number>,
) {
  const triggerIndex = event.triggerMessageId
    ? messageIndexes.get(event.triggerMessageId)
    : undefined;
  return triggerIndex !== undefined ? triggerIndex : event.afterMessageIndex;
}

function isSilentAssistantAnchor(message: AiAssistantMessage | undefined) {
  // New messages explicitly mark internal activity anchors. Older persisted
  // conversations do not have that flag, but their empty assistant records are
  // equally invisible in the timeline and should not split one tool sequence.
  return Boolean(
    message &&
      message.role === "assistant" &&
      !message.content.trim() &&
      !message.attachments?.length &&
      !message.presentation,
  );
}

function canMergeActivityGroups(
  current: AiAnchoredEventGroup,
  next: AiAnchoredEventGroup,
  messages: AiAssistantMessage[],
) {
  if (!current.events.length || !next.events.length) return false;
  const previousLastAnchor =
    current.anchorIndexes[current.anchorIndexes.length - 1];
  if (
    previousLastAnchor === undefined ||
    next.anchorIndex <= previousLastAnchor
  )
    return false;
  // The previous group's anchor is rendered before that activity card, so it is
  // not a boundary between the current and next cards. Only messages after the
  // current card and through the next anchor can interrupt the work process.
  for (
    let index = previousLastAnchor + 1;
    index <= next.anchorIndex;
    index += 1
  ) {
    if (!isSilentAssistantAnchor(messages[index])) return false;
  }
  return true;
}

/**
 * Keeps agent events on their originating message, while coalescing adjacent
 * silent assistant anchors into one uninterrupted work-process card. Thoughts,
 * plans, mode changes, and tool calls remain available inside the expanded card.
 */
export function groupAiEventsForTimeline(
  messages: AiAssistantMessage[],
  events: AiAssistantEvent[],
  indexedMessages?: ReadonlyMap<string, number>,
): AiAnchoredEventGroup[] {
  const groupedByAnchor = new Map<number, AiAssistantEvent[]>();
  const messageIndexes = indexedMessages ?? messageIndexById(messages);
  for (const event of events) {
    const anchorIndex = anchorIndexFor(event, messageIndexes);
    if (
      anchorIndex === undefined ||
      anchorIndex < 0 ||
      anchorIndex >= messages.length
    )
      continue;
    const anchoredEvents = groupedByAnchor.get(anchorIndex) ?? [];
    anchoredEvents.push(event);
    groupedByAnchor.set(anchorIndex, anchoredEvents);
  }

  const groups = [...groupedByAnchor.entries()]
    .sort(([left], [right]) => left - right)
    .map(([anchorIndex, anchoredEvents]) => ({
      anchorIndex,
      anchorIndexes: [anchorIndex],
      events: anchoredEvents,
    }));
  const merged: AiAnchoredEventGroup[] = [];

  for (const group of groups) {
    const previous = merged[merged.length - 1];
    if (previous && canMergeActivityGroups(previous, group, messages)) {
      previous.anchorIndexes.push(group.anchorIndex);
      previous.events.push(...group.events);
    } else {
      merged.push(group);
    }
  }
  return merged;
}
