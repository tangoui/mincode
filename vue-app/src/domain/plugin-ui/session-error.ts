export type PluginUISessionFailure =
  | "disabled"
  | "not-running"
  | "missing-plugin"
  | "missing-view"
  | "host-unavailable"
  | "origin-rejected"
  | "invalid-response"
  | "unavailable";

export function findPluginUIViewFailure(
  plugins: readonly { id: string }[],
  views: readonly { pluginId: string; id: string }[],
  pluginId: string,
  viewId: string,
): "missing-plugin" | "missing-view" | undefined {
  if (!plugins.some((plugin) => plugin.id === pluginId)) return "missing-plugin";
  if (!views.some((view) => view.pluginId === pluginId && view.id === viewId)) return "missing-view";
  return undefined;
}

function errorText(error: unknown, depth = 0): string {
  if (depth > 2 || error == null) return "";
  if (typeof error === "string") return error;
  if (error instanceof Error) {
    const cause = (error as Error & { cause?: unknown }).cause;
    return `${error.message} ${errorText(cause, depth + 1)}`.trim();
  }
  if (typeof error !== "object" || Array.isArray(error)) return "";
  const value = error as Record<string, unknown>;
  return [value.message, value.error, value.cause]
    .map((candidate) => errorText(candidate, depth + 1))
    .filter(Boolean)
    .join(" ");
}

/**
 * Convert native/session validation failures into safe UI states.
 *
 * The original backend error is intentionally not returned to the component:
 * IPC errors may contain implementation details, while these categories are
 * sufficient to give the user an actionable and localised recovery path.
 */
export function classifyPluginUISessionFailure(error: unknown): PluginUISessionFailure {
  const message = errorText(error).toLowerCase();
  if (/state:\s*disabled\b/.test(message)) return "disabled";
  if (/state:\s*(stopped|starting|stopping|error)\b/.test(message)) return "not-running";
  if (message.includes("plugin view") && message.includes("not found")) return "missing-view";
  if (message.includes("plugin") && message.includes("not found")) return "missing-plugin";
  if (message.includes("plugin ui host is unavailable")) return "host-unavailable";
  if (message.includes("parent origin") || message.includes("application origin is unavailable")) {
    return "origin-rejected";
  }
  if (
    message.includes("invalid plugin ui session response")
    || message.includes("invalid plugin ui asset url")
  ) return "invalid-response";
  return "unavailable";
}
