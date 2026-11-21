/** Recent Evaluate expressions: newest first, distinct, kept in memory only. */

export const EVALUATE_HISTORY_LIMIT = 20;

/** history with expression (trimmed) moved to the front, at most limit entries; blank expressions are ignored. */
export function rememberExpression(history: readonly string[], expression: string, limit = EVALUATE_HISTORY_LIMIT): string[] {
  const trimmed = expression.trim();
  if (!trimmed) return [...history];
  return [trimmed, ...history.filter((item) => item !== trimmed)].slice(0, limit);
}
