import { watch } from "vue";
import { hasPendingAiHandoffs, takeAiHandoffs } from "@/services/ai-handoff";
import type { useAiAssistantStore } from "@/stores/ai-assistant";

export function useAiHandoff(
  assistant: ReturnType<typeof useAiAssistantStore>,
  isComposerBusy: () => boolean,
) {
  watch(
    [hasPendingAiHandoffs, () => assistant.workspaceSwitching],
    ([pending, switching]) => {
      if (!pending || switching) return;
      for (const { attachment, prompt, projectRoot } of takeAiHandoffs()) {
        const current = assistant.activeConversation;
        if (!current || assistant.mode !== "agentic" || current.archived || current.external
          || current.running || current.detailsLoaded === false || assistant.agentStarting
          || isComposerBusy() || (projectRoot && projectRoot !== current.projectRoot)) {
          assistant.createConversation("agentic", { cwd: projectRoot || undefined });
        }
        assistant.setDraftAttachments([...assistant.draftAttachments, attachment]);
        assistant.setDraft([assistant.draft.trimEnd(), prompt].filter(Boolean).join("\n\n"));
      }
    },
    { immediate: true },
  );
}
