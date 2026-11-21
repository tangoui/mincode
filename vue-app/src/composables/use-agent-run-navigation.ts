import { useRouter } from "vue-router";
import { ElMessage } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import type { AgentRun } from "@/domain/ai/session-progress";
import { loadAiAssistantStore } from "@/stores/ai-assistant-shell";
import { useAiConversationPreferencesStore } from "@/stores/ai-conversation-preferences";
import { useWorkbenchStore } from "@/stores/workbench";

/**
 * Opens an AI run where it lives: its AI assistant conversation, or the
 * sessions view for an external agent session, switching workspace first
 * when needed. Shared by the AI task list and desktop navigation targets.
 */
export function useAgentRunNavigation() {
  const { t } = useVueI18n();
  const router = useRouter();
  const workbench = useWorkbenchStore();
  const conversationPreferences = useAiConversationPreferencesStore();
  void conversationPreferences.hydrate();

  async function switchToRunWorkspace(run: AgentRun) {
    if (conversationPreferences.globalMode && run.navigation.type === "ai-conversation") {
      return true;
    }
    if (run.workspaceId === workbench.activeWorkspaceId) return true;
    if (!workbench.switchWorkspace(run.workspaceId)) return false;
    await (await loadAiAssistantStore()).switchWorkspaceScope(run.workspaceScopeId);
    return true;
  }

  /** Returns whether the run was opened; failures are reported to the user. */
  async function openRun(run: AgentRun) {
    try {
      if (!await switchToRunWorkspace(run)) {
        ElMessage.error(t("ai.taskProgress.workspaceUnavailable"));
        return false;
      }
      if (run.navigation.type === "ai-conversation") {
        const assistant = await loadAiAssistantStore();
        if (!await assistant.switchToConversation(run.navigation.conversationId, run.workspaceScopeId)) {
          ElMessage.error(t("ai.taskProgress.conversationUnavailable"));
          return false;
        }
        assistant.show(run.source === "acp" ? "agent" : "agentic");
      } else {
        workbench.openView("sessions");
        await router.push({
          name: "sessions",
          query: { provider: run.navigation.provider, sessionId: run.navigation.sessionId },
        });
      }
      return true;
    } catch (error) {
      ElMessage.error(t("ai.taskProgress.openFailed", { message: String(error) }));
      return false;
    }
  }

  return { openRun };
}
