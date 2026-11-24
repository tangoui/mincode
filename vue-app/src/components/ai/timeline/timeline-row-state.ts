import { computed, inject, shallowReactive, type InjectionKey } from "vue";
import type { AiAgentActivityKind } from "@/domain/ai/agent-event-activity";
import type { AiTaskStepStatus } from "@/domain/ai/event-presentations";

export interface AiTimelineLabels {
  activity: Record<AiAgentActivityKind | "detail" | "hideDetail" | "toolName" | "result" | "duration", string>;
  eventStatuses: Record<string, string>;
  taskStepStatuses: Record<AiTaskStepStatus, string>;
  taskSteps: string;
  dismissTaskSteps: string;
  sources: string;
  artifacts: string;
  preview: string;
  collapse: string;
  copy: string;
  copied: string;
  copyResult: string;
  copiedResult: string;
  copyResultFailed: string;
  parameters: string;
}

export const aiTimelineExpansion: InjectionKey<Map<string, boolean>> = Symbol("aiTimelineExpansion");

export function useAiTimelineExpansion(key: () => string, defaultExpanded: () => boolean) {
  const choices = inject(aiTimelineExpansion, () => shallowReactive(new Map<string, boolean>()), true);
  const expanded = computed(() => choices.get(key()) ?? defaultExpanded());
  function toggle() {
    choices.set(key(), !expanded.value);
  }
  return { expanded, toggle };
}
