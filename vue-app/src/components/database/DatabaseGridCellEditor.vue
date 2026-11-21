<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import type { DatabaseTemporalEditorKind } from "@/domain/database/temporal-editor";

const props = defineProps<{
  value: string;
  editorKind: DatabaseTemporalEditorKind;
  ariaLabel: string;
}>();

const emit = defineEmits<{
  "update-value": [value: string];
  commit: [];
  cancel: [];
}>();

const editor = ref<{ $el?: HTMLElement; focus: () => void; select?: () => void }>();
const pickerPopperClass = "db-grid-inline-editor__popper";
const initialMilliseconds = props.value.match(/\.(\d{1,3})(?:Z|[+-]\d{2}:?\d{2})?$/)?.[1]?.padEnd(3, "0");

function handleDocumentPointerDown(event: PointerEvent) {
  const target = event.target;
  if (!(target instanceof Element)) return;
  if (editor.value?.$el?.contains(target) || target.closest(`.${pickerPopperClass}`)) return;
  emit("commit");
}

function updateValue(value: string | null | undefined) {
  const nextValue = value ?? "";
  if (
    initialMilliseconds &&
    (props.editorKind === "time" || props.editorKind === "datetime") &&
    /\.\d{3}$/.test(nextValue)
  ) {
    emit("update-value", nextValue.replace(/\.\d{3}$/, `.${initialMilliseconds}`));
    return;
  }
  emit("update-value", nextValue);
}

onMounted(() => {
  document.addEventListener("pointerdown", handleDocumentPointerDown, true);
  void nextTick(() => {
    editor.value?.focus();
    if (props.editorKind === "text") editor.value?.select?.();
  });
});

onBeforeUnmount(() => {
  document.removeEventListener("pointerdown", handleDocumentPointerDown, true);
});
</script>

<template>
  <el-input
    v-if="editorKind === 'text'"
    ref="editor"
    class="db-grid-inline-editor"
    :model-value="value"
    :aria-label="ariaLabel"
    @click.stop
    @dblclick.stop
    @update:model-value="updateValue"
    @keydown.enter.prevent="emit('commit')"
    @keydown.esc.prevent="emit('cancel')"
  />
  <el-date-picker
    v-else-if="editorKind === 'date'"
    ref="editor"
    class="db-grid-inline-editor"
    :model-value="value"
    type="date"
    value-format="YYYY-MM-DD"
    :aria-label="ariaLabel"
    :clearable="false"
    :popper-class="pickerPopperClass"
    @click.stop
    @dblclick.stop
    @update:model-value="updateValue"
    @change="emit('commit')"
    @keydown.esc.prevent="emit('cancel')"
  />
  <el-time-picker
    v-else-if="editorKind === 'time'"
    ref="editor"
    class="db-grid-inline-editor"
    :model-value="value"
    value-format="HH:mm:ss.SSS"
    format="HH:mm:ss.SSS"
    :aria-label="ariaLabel"
    :clearable="false"
    :popper-class="pickerPopperClass"
    @click.stop
    @dblclick.stop
    @update:model-value="updateValue"
    @change="emit('commit')"
    @keydown.esc.prevent="emit('cancel')"
  />
  <el-date-picker
    v-else
    ref="editor"
    class="db-grid-inline-editor"
    :model-value="value"
    type="datetime"
    value-format="YYYY-MM-DDTHH:mm:ss.SSS"
    format="YYYY-MM-DD HH:mm:ss.SSS"
    :aria-label="ariaLabel"
    :clearable="false"
    :popper-class="pickerPopperClass"
    @click.stop
    @dblclick.stop
    @update:model-value="updateValue"
    @change="emit('commit')"
    @keydown.esc.prevent="emit('cancel')"
  />
</template>
