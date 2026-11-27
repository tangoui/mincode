import type { AgentRunPhase } from "@/domain/ai/session-progress";
import { useVueI18n } from "@/i18n/messages";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import { redactSensitiveText } from "@/services/ai-tools/shared";
import { notifyApp } from "@/services/notifications";
import type { AppNotificationInput } from "@/stores/notifications";

const { t } = useVueI18n();
const repeatWindowMs = 10_000;

function preview(value: string | undefined, length: number) {
  if (!value) return "";
  const safe = redactSensitiveText(value).replace(/\s+/g, " ").trim();
  return Array.from(safe).slice(0, length).join("");
}

interface RunNotice {
  conversationId: string;
  workspaceScopeId: string;
  title: string;
  phase: AgentRunPhase;
  detail?: string;
  reply?: string;
  systemEnabled: boolean;
}

function notificationContent(notice: RunNotice): Pick<AppNotificationInput, "level" | "title" | "message"> | undefined {
  switch (notice.phase) {
    case "completed":
      return {
        level: "success",
        title: t("notifications.ai.runCompleted", { title: preview(notice.title, 120) }),
        message: preview(notice.reply, 80) || undefined,
      };
    case "waiting_approval":
      return {
        level: "warning",
        title: t("notifications.ai.runApproval", { summary: preview(notice.detail, 120) }),
      };
    case "waiting_input":
      return { level: "warning", title: t("notifications.ai.runQuestion") };
    case "failed":
      return {
        level: "error",
        title: t("notifications.ai.runFailed", { summary: preview(notice.detail, 120) }),
      };
  }
}

export function createAiRunNotifier() {
  const recent = new Map<string, number>();
  return (notice: RunNotice) => {
    const content = notificationContent(notice);
    if (!content) return;
    const system = notice.systemEnabled && isDesktopRuntime() && document.visibilityState !== "visible";
    if (system) {
      const now = Date.now();
      for (const [key, sentAt] of recent) {
        if (now - sentAt >= repeatWindowMs) recent.delete(key);
      }
      const key = JSON.stringify([notice.conversationId, notice.phase]);
      if (recent.has(key)) return;
      recent.set(key, now);
    }
    notifyApp({
      ...content,
      source: "ai",
      target: {
        kind: "ai-conversation",
        conversationId: notice.conversationId,
        workspaceScopeId: notice.workspaceScopeId,
      },
    }, { suppressSystem: !notice.systemEnabled });
  };
}
