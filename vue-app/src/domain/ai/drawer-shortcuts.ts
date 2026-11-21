/** The conversation next to the active one in list order, without wrapping. */
export function adjacentConversationId(
  orderedIds: readonly string[],
  activeId: string,
  direction: -1 | 1,
): string | undefined {
  const index = orderedIds.indexOf(activeId);
  if (index < 0) return direction > 0 ? orderedIds[0] : orderedIds[orderedIds.length - 1];
  const next = orderedIds[index + direction];
  return next === activeId ? undefined : next;
}
