<script setup lang="ts">
/**
 * An expression input (the evaluate dialog, the watch input, the console
 * input) with DAP completions when the session's adapter declares
 * supportsCompletionsRequest (see useDebugCompletions). Without it, or while
 * the program runs, it behaves as a plain input. Input attributes (size,
 * placeholder, clearable, disabled, aria-label, textarea type…) pass through
 * to the input; `class` goes to the element around it.
 *
 * Keys: the list opens with its first item highlighted (as in IDEA and VS
 * Code); ↑/↓ (Home/End, PageUp/PageDown) move in it, Enter accepts the
 * highlighted item and Esc closes it. With no list Enter emits `submit` and
 * the keys keep their input behaviour: Element Plus takes them for its list
 * whatever modifiers are held (preventing them, and Ctrl/⌘+Enter would ask
 * for completions), so these keys — modified or not — are stopped before they
 * reach it and their default (cursor movement, selection) is left alone. A
 * global keybinding on one of them therefore does not fire while typing here.
 * Enter submits with any modifiers, as these inputs did before completions,
 * except Shift+Enter in a multi-line input, which adds a line. An IME
 * composition's keys never submit.
 */
import { computed, ref, useAttrs, watch } from "vue";
import type { DebugSessionView } from "@/domain/debug/session-model";
import { debugCompletionsAvailable, useDebugCompletions } from "@/composables/use-debug-completions";

defineOptions({ inheritAttrs: false });

const props = defineProps<{
  modelValue: string;
  session: DebugSessionView | null;
  /** A textarea: Shift+Enter adds a line, the cursor keys move between lines. */
  multiline?: boolean;
}>();
const emit = defineEmits<{ "update:modelValue": [value: string]; submit: [] }>();

/** What the key handling reads of el-autocomplete. */
interface AutocompleteHandle {
  activated: boolean;
  suggestions: unknown[];
  highlightedIndex: number;
  close: () => void;
  focus: () => void;
}

/** Keys el-autocomplete takes (and prevents) even while its list is closed. */
const LIST_KEYS = new Set(["Enter", "ArrowUp", "ArrowDown", "Home", "End", "PageUp", "PageDown"]);

const attrs = useAttrs();
const inputAttrs = computed(() => {
  const { class: _class, ...rest } = attrs;
  return rest;
});
const autocomplete = ref<AutocompleteHandle | null>(null);
const completions = useDebugCompletions(() => props.session);

function close() {
  completions.cancel();
  autocomplete.value?.close();
}

// The program runs on (or the frame changes): the shown items are of the old stop.
watch(
  () => [debugCompletionsAvailable(props.session), props.session?.id, props.session?.selectedFrameId] as const,
  close
);

function onKeydown(event: KeyboardEvent) {
  if (event.isComposing || event.keyCode === 229) {
    event.stopPropagation();
    return;
  }
  if (!LIST_KEYS.has(event.key)) return;
  const list = autocomplete.value;
  const open = Boolean(list && list.activated && list.suggestions.length > 0);
  const newline = event.key === "Enter" && event.shiftKey && props.multiline === true;
  const submitsPastList = event.key === "Enter" && (list?.highlightedIndex ?? -1) < 0;
  if (open && !newline && !submitsPastList) return;
  event.stopPropagation();
  if (event.key !== "Enter" || newline) return;
  event.preventDefault();
  close();
  emit("submit");
}

defineExpose({ focus: () => autocomplete.value?.focus() });
</script>

<template>
  <div :class="['debug-completion-input', attrs.class]" @keydown.capture="onKeydown">
    <el-autocomplete
      ref="autocomplete"
      v-bind="inputAttrs"
      :model-value="modelValue"
      :fetch-suggestions="completions.fetchSuggestions"
      :debounce="0"
      :trigger-on-focus="false"
      :show-arrow="false"
      highlight-first-item
      hide-loading
      popper-class="debug-completions__popper"
      @update:model-value="emit('update:modelValue', String($event))"
    >
      <template v-if="$slots.prefix" #prefix><slot name="prefix" /></template>
      <template #default="{ item }">
        <span class="debug-completions__label">{{ item.label }}</span>
        <span v-if="item.type" class="debug-completions__type">{{ item.type }}</span>
      </template>
    </el-autocomplete>
  </div>
</template>
