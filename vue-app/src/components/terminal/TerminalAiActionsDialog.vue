<script setup lang="ts">
import { computed } from "vue";
import { MagicStick } from "@element-plus/icons-vue";
import {
  buildTerminalAiPrompt,
  prepareTerminalAiContext,
  type TerminalAiAction,
} from "@/domain/terminal/ai-actions";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { useAiAssistantStore } from "@/stores/ai-assistant";

const props = defineProps<{
  modelValue: boolean;
  contextText: string;
  terminalKind: "local" | "ssh";
}>();

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
}>();

interface TerminalAiActionConfig {
  id: TerminalAiAction;
  labelKey: MessageKey;
  descriptionKey: MessageKey;
  promptKey: MessageKey;
  requiresContext: boolean;
}

const ACTIONS: TerminalAiActionConfig[] = [
  {
    id: "explain-command",
    labelKey: "terminal.ai.explainCommand",
    descriptionKey: "terminal.ai.explainCommandDescription",
    promptKey: "terminal.ai.prompt.explainCommand",
    requiresContext: true,
  },
  {
    id: "analyze-output",
    labelKey: "terminal.ai.analyzeOutput",
    descriptionKey: "terminal.ai.analyzeOutputDescription",
    promptKey: "terminal.ai.prompt.analyzeOutput",
    requiresContext: true,
  },
  {
    id: "diagnose-error",
    labelKey: "terminal.ai.diagnoseError",
    descriptionKey: "terminal.ai.diagnoseErrorDescription",
    promptKey: "terminal.ai.prompt.diagnoseError",
    requiresContext: true,
  },
  {
    id: "optimize-command",
    labelKey: "terminal.ai.optimizeCommand",
    descriptionKey: "terminal.ai.optimizeCommandDescription",
    promptKey: "terminal.ai.prompt.optimizeCommand",
    requiresContext: true,
  },
  {
    id: "security-review",
    labelKey: "terminal.ai.securityReview",
    descriptionKey: "terminal.ai.securityReviewDescription",
    promptKey: "terminal.ai.prompt.securityReview",
    requiresContext: true,
  },
  {
    id: "generate-command",
    labelKey: "terminal.ai.generateCommand",
    descriptionKey: "terminal.ai.generateCommandDescription",
    promptKey: "terminal.ai.prompt.generateCommand",
    requiresContext: false,
  },
  {
    id: "ask-context",
    labelKey: "terminal.ai.askContext",
    descriptionKey: "terminal.ai.askContextDescription",
    promptKey: "terminal.ai.prompt.askContext",
    requiresContext: true,
  },
];

const { t } = useVueI18n();
const assistant = useAiAssistantStore();
const preparedContext = computed(() => prepareTerminalAiContext(props.contextText));
const hasContext = computed(() => Boolean(preparedContext.value.text.trim()));
const terminalLabel = computed(() =>
  t(props.terminalKind === "ssh" ? "terminal.ai.terminalSsh" : "terminal.ai.terminalLocal")
);

function chooseAction(action: TerminalAiActionConfig) {
  const context = action.requiresContext ? preparedContext.value.text : "";
  const prompt = buildTerminalAiPrompt({
    instruction: t(action.promptKey),
    terminalLabel: t("terminal.ai.terminalLabel").replace("{type}", terminalLabel.value),
    contextLabel: t("terminal.ai.contextLabel"),
    safetyInstruction: t("terminal.ai.safetyInstruction"),
    context,
  });
  assistant.setDraft(prompt);
  assistant.show("agentic");
  emit("update:modelValue", false);
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="t('terminal.ai.title')"
    width="min(680px, calc(100vw - 32px))"
    class="terminal-ai-dialog"
    append-to-body
    align-center
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="terminal-ai-dialog__body">
      <el-alert
        type="info"
        :closable="false"
        show-icon
        :title="t('terminal.ai.reviewNotice')"
      />

      <div class="terminal-ai-dialog__actions" role="list" :aria-label="t('terminal.ai.actionsLabel')">
        <el-button
          v-for="action in ACTIONS"
          :key="action.id"
          class="terminal-ai-dialog__action"
          :disabled="action.requiresContext && !hasContext"
          plain
          @click="chooseAction(action)"
        >
          <el-icon><MagicStick /></el-icon>
          <span>
            <strong>{{ t(action.labelKey) }}</strong>
            <small>{{ t(action.descriptionKey) }}</small>
          </span>
        </el-button>
      </div>

      <section v-if="hasContext" class="terminal-ai-dialog__context">
        <div class="terminal-ai-dialog__context-header">
          <strong>{{ t('terminal.ai.contextPreview') }}</strong>
          <span>{{ terminalLabel }}</span>
        </div>
        <el-alert
          v-if="preparedContext.redactionCount"
          type="warning"
          :closable="false"
          show-icon
          :title="t('terminal.ai.redacted').replace('{count}', String(preparedContext.redactionCount))"
        />
        <el-alert
          v-if="preparedContext.truncated"
          type="warning"
          :closable="false"
          :title="t('terminal.ai.truncated')"
        />
        <el-input
          :model-value="preparedContext.text"
          type="textarea"
          readonly
          resize="none"
          :rows="Math.min(6, Math.max(3, preparedContext.text.split('\n').length))"
          :aria-label="t('terminal.ai.contextPreview')"
        />
      </section>

      <el-empty
        v-else
        :image-size="48"
        :description="t('terminal.ai.noContext')"
      />
    </div>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">{{ t('common.cancel') }}</el-button>
    </template>
  </el-dialog>
</template>
