export interface DeclinedToolResultLabels {
  declined: string;
  declinedWithFeedback: (feedback: string) => string;
}

/**
 * The tool result the model sees when the user declines a call, carrying their instruction
 * when they gave one. An instruction also marks the result `redirect`, so the agent loop
 * skips the rest of the batch and the model re-plans from it.
 */
export function declinedToolResult(
  feedback: string | undefined,
  labels: DeclinedToolResultLabels,
): { content: string; status: "cancelled"; redirect?: true } {
  if (!feedback) return { content: labels.declined, status: "cancelled" };
  return { content: labels.declinedWithFeedback(feedback), status: "cancelled", redirect: true };
}
