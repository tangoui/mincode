<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import type { ScrollbarInstance } from "element-plus";
import { ArrowUp, Close, Document, EditPen, FullScreen, MagicStick, Paperclip, Promotion, Tools, VideoPause } from "@element-plus/icons-vue";
import { getAttachmentImageSrc, type AiAttachment } from "@/domain/ai/attachments";
import type { AiPromptSuggestion } from "@/domain/ai/tool-command";
import {
  acpConfigCurrentLabel,
  groupAcpConfigSelectOptions,
  type AcpConfigSelectOption,
} from "@/domain/ai/session-config-option";

interface SelectOption { value: string; label: string }
interface ModelSelectOption extends SelectOption { profileId: string; profileLabel: string }
interface Labels {
  placeholder: string; addAttachment: string; sendHint: string; send: string; stop: string; model: string;
  modelGroup: string; profileGroup: string; chooseModel: string; noModels: string; reasoning: string; removeAttachment: string;
  previewAttachment: string; suggestionList: string; suggestionHint: string;
  suggestionTool: string;
  optimizePrompt: string;
  cancelOptimization: string;
}

const props = defineProps<{
  modelValue: string;
  attachments: AiAttachment[];
  suggestions: AiPromptSuggestion[];
  activeSuggestionIndex: number;
  suggestionsVisible: boolean;
  loading: boolean;
  starting: boolean;
  addingAttachments: boolean;
  optimizingPrompt: boolean;
  models: ModelSelectOption[];
  selectedModel: string;
  reasoningOptions: SelectOption[];
  selectedReasoning: string;
  modelSelectionSupported: boolean;
  agentConfigOptions: AcpConfigSelectOption[];
  agentConfigPending: string;
  labels: Labels;
  accept?: string;
  editingQueue?: boolean;
  contextUsage?: { label: string; compactLabel: string; title: string; level: "normal" | "warning" | "danger" };
  updateLabel?: string;
      cancelEditLabel?: string;
  history: string[];
}>();
const emit = defineEmits<{
  "update:modelValue": [value: string]; "update:activeSuggestionIndex": [value: number];
  "update:selectedModel": [value: string]; "update:selectedReasoning": [value: string];
  "add-files": [files: FileList]; "remove-attachment": [id: string]; "preview-attachment": [attachment: AiAttachment];
  "select-suggestion": [suggestion: AiPromptSuggestion]; "dismiss-suggestions": []; send: []; cancel: []; paste: [event: ClipboardEvent];
  "update-queue": []; "cancel-edit": []; "optimize-prompt": []; "cancel-optimization": [];
  "change-agent-config": [payload: { configId: string; value: string }];
}>();
const attachmentInput = ref<HTMLInputElement>();
const promptScrollbar = ref<ScrollbarInstance>();
const historyIndex = ref(-1);
const historyDraft = ref("");

watch(() => props.modelValue, async () => {
  if (historyIndex.value < 0) historyDraft.value = props.modelValue;
  await nextTick();
  promptScrollbar.value?.update();
  const textarea = promptScrollbar.value?.wrapRef?.querySelector("textarea");
  if (textarea && document.activeElement === textarea && textarea.selectionEnd === textarea.value.length) {
    promptScrollbar.value?.setScrollTop(promptScrollbar.value.wrapRef?.scrollHeight ?? 0);
  }
});
watch(() => props.history, () => {
  historyIndex.value = -1;
  historyDraft.value = props.modelValue;
});

function togglePromptOptimization() {
  if (props.optimizingPrompt) emit("cancel-optimization");
  else emit("optimize-prompt");
}

function navigateHistory(direction: -1 | 1) {
  if (!props.history.length) return false;
  if (direction === -1) {
    if (historyIndex.value < 0) historyDraft.value = props.modelValue;
    historyIndex.value = Math.min(props.history.length - 1, historyIndex.value + 1);
    emit("update:modelValue", props.history[historyIndex.value] ?? "");
    return true;
  }
  if (historyIndex.value < 0) return false;
  historyIndex.value -= 1;
  emit("update:modelValue", historyIndex.value < 0 ? historyDraft.value : (props.history[historyIndex.value] ?? ""));
  return true;
}

const reasoningTriggerLabel = computed(() => {
  const option = props.reasoningOptions.find((item) => item.value === props.selectedReasoning)
    ?? props.reasoningOptions.find((item) => item.value === "default");
  return option ? `${props.labels.reasoning} · ${option.label}` : props.labels.reasoning;
});
const selectedModelOption = computed(() =>
  props.models.find((option) => option.value === props.selectedModel),
);
const groupedModels = computed(() => {
  const groups: Array<{ id: string; label: string; models: ModelSelectOption[] }> = [];
  for (const model of props.models) {
    let group = groups.find((item) => item.id === model.profileId);
    if (!group) {
      group = { id: model.profileId, label: model.profileLabel, models: [] };
      groups.push(group);
    }
    group.models.push(model);
  }
  return groups;
});
const modelTriggerLabel = computed(() => {
  const selected = selectedModelOption.value;
  return selected
    ? `${selected.label} · ${selected.profileLabel}`
    : props.models.length ? props.labels.chooseModel : props.labels.noModels;
});
const onModelCommand = (command: { kind: "model"; value: string }) => {
  emit("update:selectedModel", command.value);
};
const onReasoningCommand = (value: string) => {
  emit("update:selectedReasoning", value);
};

const agentConfigMenus = computed(() => {
  // ACP options belong to the agent session; the built-in picker owns the other
  // modes. Rendering both would stack two sets of model controls side by side.
  if (props.modelSelectionSupported) return [];
  const { merged, standalone } = groupAcpConfigSelectOptions(props.agentConfigOptions);
  // Model first, so a merged trigger reads "<model> · <thought level>" like the built-in picker.
  const sections = [...merged].sort((a, b) => Number(b.category === "model") - Number(a.category === "model"));
  return [...standalone.map((option) => [option]), ...(sections.length ? [sections] : [])];
});
const menuTriggerLabel = (sections: AcpConfigSelectOption[]) =>
  sections.map(acpConfigCurrentLabel).filter(Boolean).join(" · ");
const menuPending = (sections: AcpConfigSelectOption[]) =>
  sections.some((option) => option.id === props.agentConfigPending);
const onAgentConfigCommand = (command: { configId: string; value: string }) => {
  emit("change-agent-config", command);
};

const formatSize = (size: number) => size < 1024 ? `${size} B` : size < 1024 * 1024
  ? `${(size / 1024).toFixed(1)} KB` : `${(size / 1024 / 1024).toFixed(1)} MB`;
const labelFor = (template: string, name: string) => template.replace("{name}", name);
const updateModelValue = (value: string) => {
  historyIndex.value = -1;
  historyDraft.value = value;
  emit("update:modelValue", value);
};
const selectSuggestion = (index: number) => {
  const suggestion = props.suggestions[index];
  if (suggestion) emit("select-suggestion", suggestion);
};
const onKeydown = (event: KeyboardEvent) => {
  if (event.isComposing || event.keyCode === 229) return;
  if (props.suggestionsVisible && props.suggestions.length) {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const change = event.key === "ArrowDown" ? 1 : -1;
      emit("update:activeSuggestionIndex", Math.max(0, Math.min(props.suggestions.length - 1, props.activeSuggestionIndex + change)));
      return;
    }
    if (event.key === "Tab" || (event.key === "Enter" && !event.shiftKey)) {
      event.preventDefault();
      selectSuggestion(props.activeSuggestionIndex);
      return;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      emit("dismiss-suggestions");
      return;
    }
  }
    if ((event.key === "ArrowUp" || event.key === "ArrowDown") && !event.shiftKey && !event.altKey && !event.ctrlKey && !event.metaKey) {
    const textarea = event.target as HTMLTextAreaElement;
    const direction = event.key === "ArrowUp" ? -1 : 1;
    // Preserve native caret movement for non-empty multi-line drafts. Once a
    // history item is selected, both arrows continue browsing that history.
    if (historyIndex.value >= 0 || !textarea.value) {
      if (navigateHistory(direction)) event.preventDefault();
      return;
    }
  }
  if (event.key === "Enter" && !event.shiftKey) {
    event.preventDefault();
    emit("send");
  }
};
const onFiles = (event: Event) => {
  const input = event.target as HTMLInputElement;
  if (input.files?.length) emit("add-files", input.files);
  input.value = "";
};
</script>

<template>
  <footer class="ai-assistant-composer">
    <input ref="attachmentInput" class="ai-assistant-attachment-input" type="file" multiple :accept="accept" @change="onFiles" />
    <el-scrollbar v-if="attachments.length" class="ai-assistant-attachments" :native="false">
      <div v-for="attachment in attachments" :key="attachment.id" class="ai-attachment-chip" :class="{ 'is-image': attachment.kind === 'image' }" :title="`${attachment.name} · ${formatSize(attachment.size)}`">
        <el-button v-if="attachment.kind === 'image' && getAttachmentImageSrc(attachment)" class="ai-attachment-chip__preview" text native-type="button" :aria-label="labelFor(labels.previewAttachment, attachment.name)" @click="emit('preview-attachment', attachment)">
          <el-image :src="getAttachmentImageSrc(attachment)" :alt="attachment.name" fit="cover" /><FullScreen aria-hidden="true" />
        </el-button>
        <template v-else><Document aria-hidden="true" /><span><strong>{{ attachment.name }}</strong><small>{{ formatSize(attachment.size) }}</small></span></template>
        <el-button class="ai-attachment-chip__remove" text native-type="button" :icon="Close" :data-attachment-remove="attachment.id" :aria-label="labelFor(labels.removeAttachment, attachment.name)" @click="emit('remove-attachment', attachment.id)" />
      </div>
    </el-scrollbar>
    <div class="ai-assistant-composer__input">
      <el-scrollbar v-if="suggestionsVisible && suggestions.length" class="ai-tool-suggest" role="listbox" :aria-label="labels.suggestionList">
        <button v-for="(suggestion, index) in suggestions" :key="suggestion.id" type="button" role="option" :aria-selected="index === activeSuggestionIndex" :class="{ 'is-active': index === activeSuggestionIndex }" @mouseenter="emit('update:activeSuggestionIndex', index)" @mousedown.prevent="selectSuggestion(index)">
          <Tools aria-hidden="true" />
          <span>
            <span class="ai-tool-suggest__title">
              <strong>{{ suggestion.name }}</strong>
              <el-tag size="small" effect="plain">{{ props.labels.suggestionTool }}</el-tag>
            </span>
            <small>{{ suggestion.description }}</small>
          </span>
        </button>
        <div>{{ labels.suggestionHint }}</div>
      </el-scrollbar>
      <el-scrollbar ref="promptScrollbar" class="ai-assistant-composer__prompt-scrollbar" max-height="158px" always>
        <el-input :model-value="modelValue" type="textarea" :autosize="{ minRows: 2 }" :placeholder="labels.placeholder" @update:model-value="updateModelValue" @paste="emit('paste', $event)" @keydown="onKeydown" />
      </el-scrollbar>
    </div>
    <div class="ai-assistant-composer__actions">
      <div class="ai-assistant-composer__tools">
        <el-dropdown
          v-if="modelSelectionSupported"
          class="ai-assistant-model-menu"
          trigger="click"
          placement="top-start"
          max-height="320px"
          :teleported="false"
          @command="onModelCommand"
        >
          <!-- 运行中不禁用：模型只在发送时读取一次并被请求闭包捕获，切换只影响下一条
               消息（含队列中待发消息），不会污染进行中的回答。 -->
          <el-button text class="ai-assistant-model-menu__trigger" :aria-label="labels.model">
            <span>{{ modelTriggerLabel }}</span>
            <el-icon aria-hidden="true"><ArrowUp /></el-icon>
          </el-button>
          <template #dropdown>
            <el-dropdown-menu class="ai-assistant-model-menu__list">
              <li class="ai-assistant-model-menu__group" role="presentation">{{ labels.modelGroup }}</li>
              <li v-if="!models.length" class="ai-assistant-model-menu__empty" role="presentation">{{ labels.noModels }}</li>
              <template v-for="group in groupedModels" :key="group.id">
                <li class="ai-assistant-model-menu__profile" role="presentation">
                  {{ labelFor(labels.profileGroup, group.label) }}
                </li>
                <el-dropdown-item
                  v-for="model in group.models"
                  :key="model.value"
                  :command="{ kind: 'model', value: model.value }"
                  :class="{ 'is-active': model.value === selectedModel }"
                >
                  <span class="ai-assistant-model-menu__check" aria-hidden="true">{{ model.value === selectedModel ? "✓" : "" }}</span>
                  <span class="ai-assistant-model-menu__label">{{ model.label }}</span>
                  <span class="ai-assistant-model-menu__profile-label">{{ model.profileLabel }}</span>
                </el-dropdown-item>
              </template>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        <el-dropdown
          v-if="modelSelectionSupported && reasoningOptions.length"
          class="ai-assistant-model-menu ai-assistant-model-menu--reasoning"
          trigger="click"
          placement="top-start"
          max-height="320px"
          :teleported="false"
          @command="onReasoningCommand"
        >
          <el-button text class="ai-assistant-model-menu__trigger" :aria-label="labels.reasoning" :title="labels.reasoning">
            <span>{{ reasoningTriggerLabel }}</span>
            <el-icon aria-hidden="true"><ArrowUp /></el-icon>
          </el-button>
          <template #dropdown>
            <el-dropdown-menu class="ai-assistant-model-menu__list ai-assistant-model-menu__list--compact">
              <li class="ai-assistant-model-menu__group" role="presentation">{{ labels.reasoning }}</li>
              <el-dropdown-item
                v-for="option in reasoningOptions"
                :key="`reasoning-${option.value}`"
                :command="option.value"
                :class="{ 'is-active': option.value === selectedReasoning }"
              >
                <span class="ai-assistant-model-menu__check" aria-hidden="true">{{ option.value === selectedReasoning ? "✓" : "" }}</span>
                <span class="ai-assistant-model-menu__label">{{ option.label }}</span>
              </el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        <el-dropdown
          v-for="sections in agentConfigMenus"
          :key="sections.map((option) => option.id).join('-')"
          class="ai-assistant-model-menu"
          trigger="click"
          placement="top-start"
          max-height="320px"
          :teleported="false"
          :disabled="loading || menuPending(sections)"
          @command="onAgentConfigCommand"
        >
          <el-button text class="ai-assistant-model-menu__trigger" :disabled="loading || menuPending(sections)" :aria-label="sections.map((option) => option.name).join(', ')">
            <span>{{ menuTriggerLabel(sections) }}</span>
            <el-icon aria-hidden="true"><ArrowUp /></el-icon>
          </el-button>
          <template #dropdown>
            <el-dropdown-menu class="ai-assistant-model-menu__list">
              <template v-for="section in sections" :key="section.id">
                <li v-if="sections.length > 1" class="ai-assistant-model-menu__group" role="presentation">{{ section.name }}</li>
                <el-dropdown-item
                  v-for="choice in section.choices"
                  :key="`${section.id}-${choice.value}`"
                  :command="{ configId: section.id, value: choice.value }"
                  :class="{ 'is-active': choice.value === section.currentValue }"
                >
                  <span class="ai-assistant-model-menu__check" aria-hidden="true">{{ choice.value === section.currentValue ? "✓" : "" }}</span>
                  <span class="ai-assistant-model-menu__label">{{ choice.label }}</span>
                </el-dropdown-item>
              </template>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        <slot name="approval-control" />
        <div class="ai-assistant-composer__attachment-tools">
          <el-tooltip
            :content="labels.addAttachment"
            placement="top"
            :teleported="false"
            :show-after="300"
          >
            <el-button
              text
              class="ai-assistant-composer__tool-button"
              :icon="Paperclip"
              :loading="addingAttachments"
              :aria-label="labels.addAttachment"
              @click="attachmentInput?.click()"
            />
          </el-tooltip>
          <slot name="delegation-control" />
          <span
            v-if="contextUsage"
            class="ai-assistant-composer__context"
            :class="`is-${contextUsage.level}`"
            :title="contextUsage.title"
          >
            <span class="sr-only">{{ contextUsage.label }}</span>
            <span class="ai-assistant-composer__context-label" aria-hidden="true">{{ contextUsage.label }}</span>
            <span class="ai-assistant-composer__context-percent" aria-hidden="true">{{ contextUsage.compactLabel }}</span>
          </span>
        </div>
        <small>{{ labels.sendHint }}</small>
      </div>
      <div class="ai-assistant-composer__primary-actions">
        <el-tooltip
          :content="optimizingPrompt ? labels.cancelOptimization : labels.optimizePrompt"
          placement="top"
          :teleported="false"
          :show-after="300"
        >
          <el-button
            text
            class="ai-assistant-composer__optimize"
            :icon="optimizingPrompt ? VideoPause : MagicStick"
            :class="{ 'is-loading': optimizingPrompt }"
            :disabled="!optimizingPrompt && !modelValue.trim()"
            :aria-label="optimizingPrompt ? labels.cancelOptimization : labels.optimizePrompt"
            @click="togglePromptOptimization"
          />
        </el-tooltip>
        <el-tooltip
          v-if="loading"
          :content="labels.stop"
          placement="top"
          :teleported="false"
          :show-after="300"
        >
          <el-button
            class="ai-assistant-composer__stop"
            text
            :icon="VideoPause"
            :aria-label="labels.stop"
            @click="emit('cancel')"
          />
        </el-tooltip>
        <template v-else-if="editingQueue">
          <el-button text size="small" @click="emit('cancel-edit')">{{ cancelEditLabel }}</el-button>
          <el-button type="warning" :icon="EditPen" @click="emit('update-queue')">{{ updateLabel }}</el-button>
        </template>
        <el-button
          v-else
          class="ai-assistant-composer__send"
          text
          :icon="Promotion"
          :loading="starting"
          :aria-label="labels.send"
          @click="emit('send')"
        />
      </div>
    </div>
  </footer>
</template>
