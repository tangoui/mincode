import type { AiTimelineItem } from "./timeline-items";

export const AI_MESSAGE_RENDER_BATCH_SIZE = 80;

function safeMessageCount(value: number) {
  return Number.isFinite(value) ? Math.max(0, Math.floor(value)) : 0;
}

export function initialAiMessageRenderStart(messageCount: number) {
  return Math.max(0, safeMessageCount(messageCount) - AI_MESSAGE_RENDER_BATCH_SIZE);
}

/**
 * A (re)mounted thread first renders only the newest messages, so switching to a long
 * conversation shows its end in one cheap frame. The rest of the regular window is filled in
 * one step once the reader scrolls up towards it (filledAiMessageRenderStart).
 */
export const AI_MESSAGE_FIRST_PAINT_SIZE = 16;

export function firstPaintAiMessageRenderStart(messageCount: number) {
  const count = safeMessageCount(messageCount);
  return Math.max(initialAiMessageRenderStart(count), count - AI_MESSAGE_FIRST_PAINT_SIZE);
}

/** The regular window once filled; never narrows a window the user already widened. */
export function filledAiMessageRenderStart(currentStart: number, messageCount: number) {
  return Math.min(safeMessageCount(currentStart), initialAiMessageRenderStart(messageCount));
}

export function earlierAiMessageRenderStart(currentStart: number) {
  return Math.max(0, safeMessageCount(currentStart) - AI_MESSAGE_RENDER_BATCH_SIZE);
}

export function nextAiMessageRenderStart(
  currentStart: number,
  previousMessageCount: number,
  messageCount: number,
) {
  const previousCount = safeMessageCount(previousMessageCount);
  const nextCount = safeMessageCount(messageCount);
  if (nextCount < previousCount) return initialAiMessageRenderStart(nextCount);
  return currentStart === initialAiMessageRenderStart(previousCount)
    ? initialAiMessageRenderStart(nextCount)
    : Math.min(safeMessageCount(currentStart), nextCount);
}

const MAX_TURN_ALIGN_LOOKBACK = 200;

/**
 * Moves the first rendered message back to the user message that opens its turn, so "load
 * earlier" never shows a turn cut in half. An unusually long turn is left cut rather than pulling
 * its whole history into the window.
 */
export function alignAiMessageRenderStartToTurn(messages: ReadonlyArray<{ role: string }>, start: number) {
  const first = Math.max(0, start - MAX_TURN_ALIGN_LOOKBACK);
  for (let index = Math.min(start, messages.length - 1); index >= first; index -= 1) {
    if (messages[index]?.role === "user") return index;
  }
  return first === 0 ? 0 : start;
}
/** Align short turns without mounting a large tool-heavy turn all at once. */
export function alignAiTimelineRenderStartToTurn(items: readonly AiTimelineItem[], start: number) {
  const first = Math.max(0, start - AI_MESSAGE_FIRST_PAINT_SIZE);
  for (let index = Math.min(start, items.length - 1); index >= first; index -= 1) {
    const item = items[index];
    if (item?.kind === "message" && item.message.role === "user") return index;
  }
  return start;
}
