<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { analyzeTerminalPaste, stripTerminalTrailingNewlines } from "@/domain/terminal/context-menu";
import { useVueI18n } from "@/i18n/messages";

const props = withDefaults(
  defineProps<{
    modelValue: boolean;
    text: string;
    /** False for command snippets: trailing newlines are always stripped and the option is hidden. */
    allowTrailingNewline?: boolean;
  }>(),
  { allowTrailingNewline: true }
);

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  confirm: [text: string];
}>();

const { t } = useVueI18n();
const keepTrailingNewline = ref(false);
const draft = ref("");
const analysis = computed(() => analyzeTerminalPaste(draft.value));
const pasteText = computed(() =>
  keepTrailingNewline.value && props.allowTrailingNewline ? analysis.value.text : stripTerminalTrailingNewlines(analysis.value.text)
);

watch(
  () => [props.modelValue, props.text] as const,
  ([visible, text]) => {
    if (!visible) return;
    draft.value = analyzeTerminalPaste(text).text;
    keepTrailingNewline.value = false;
  },
  { immediate: true }
);

function confirmPaste() {
  if (!pasteText.value) return;
  emit("confirm", pasteText.value);
  emit("update:modelValue", false);
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="t('terminal.pasteConfirmTitle')"
    width="min(600px, calc(100vw - 32px))"
    class="terminal-paste-dialog"
    append-to-body
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="terminal-paste-dialog__body">
      <el-alert
        v-if="analysis.dangerousPatterns.length"
        type="warning"
        :closable="false"
        show-icon
        :title="t('terminal.pasteDangerousWarning')"
        :description="t('terminal.pasteDangerousDetail').replace('{count}', String(analysis.dangerousPatterns.length))"
      />
      <p class="terminal-paste-dialog__summary">
        {{ t('terminal.pasteLineCount').replace('{count}', String(analysis.lineCount)) }}
      </p>
      <el-input
        v-model="draft"
        type="textarea"
        resize="none"
        :rows="Math.min(12, Math.max(4, analysis.lineCount + 1))"
        :aria-label="t('terminal.pastePreview')"
      />
      <div v-if="allowTrailingNewline" class="terminal-paste-dialog__option">
        <div>
          <strong>{{ t('terminal.pasteKeepNewline') }}</strong>
          <small>{{ t('terminal.pasteKeepNewlineHint') }}</small>
        </div>
        <el-switch v-model="keepTrailingNewline" :disabled="!analysis.hasTrailingNewline" />
      </div>
    </div>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">{{ t('common.cancel') }}</el-button>
      <el-button type="primary" :disabled="!pasteText" @click="confirmPaste">
        {{ t('terminal.pasteConfirmAction') }}
      </el-button>
    </template>
  </el-dialog>
</template>
