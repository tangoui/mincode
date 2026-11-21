export type AiNotificationLevel = "success" | "warning" | "error";

export type AiNotificationTitleKey =
  | "notifications.ai.chatFailed"
  | "notifications.ai.taskCompleted"
  | "notifications.ai.taskIncomplete"
  | "notifications.ai.taskFailed"
  | "notifications.ai.modelFallback"
  | "notifications.ai.agentStartFailed"
  | "notifications.ai.agentPromptFailed"
  | "notifications.ai.agentTaskCompleted";

export interface AiTaskNotification {
  level: AiNotificationLevel;
  titleKey: AiNotificationTitleKey;
  message?: string;
  detail?: string;
}

/**
 * Cancellation is an expected user action, so it must not create an error in
 * the notification center. The structural check also covers AbortError values
 * reconstructed by native IPC instead of real DOMException instances.
 */
export function isAiRequestCancelled(error: unknown) {
  return Boolean(
    error
    && typeof error === "object"
    && "name" in error
    && (error as { name?: unknown }).name === "AbortError",
  );
}

function conciseText(value: string | undefined, maxLength: number) {
  const text = value?.replace(/\s+/g, " ").trim();
  if (!text) return undefined;
  return text.length <= maxLength ? text : `${text.slice(0, maxLength - 1)}…`;
}

function safeErrorDetail(error: unknown) {
  const raw = error instanceof Error ? error.message : String(error);
  const redacted = raw
    .replace(/(api[\s_-]*key\s*[=:]\s*)([^\s,;"'}]+)/gi, "$1[REDACTED]")
    .replace(/(authorization\s*[=:]\s*bearer\s+)([^\s,;"'}]+)/gi, "$1[REDACTED]")
    .replace(/(bearer\s+)([a-z0-9._~+/-]{12,})/gi, "$1[REDACTED]")
    .replace(/((?:access|refresh|session)[\s_-]*token\s*[=:]\s*)([^\s,;"'}]+)/gi, "$1[REDACTED]");
  return conciseText(redacted, 500);
}

/** Returns an actionable notification while redacting common credential formats. */
export function aiErrorNotification(
  error: unknown,
  titleKey: Exclude<AiNotificationTitleKey, "notifications.ai.taskCompleted" | "notifications.ai.taskIncomplete" | "notifications.ai.modelFallback" | "notifications.ai.agentTaskCompleted">,
  taskContext?: string,
): AiTaskNotification | undefined {
  if (isAiRequestCancelled(error)) return undefined;
  return {
    level: "error",
    titleKey,
    message: conciseText(taskContext, 160),
    detail: safeErrorDetail(error),
  };
}

/** Maps the end of a complete Agentic run to exactly one contextual notification. */
export function aiAgentTaskNotification(completed: boolean, taskContext?: string): AiTaskNotification {
  return {
    level: completed ? "success" : "warning",
    titleKey: completed ? "notifications.ai.taskCompleted" : "notifications.ai.taskIncomplete",
    message: conciseText(taskContext, 160),
  };
}
