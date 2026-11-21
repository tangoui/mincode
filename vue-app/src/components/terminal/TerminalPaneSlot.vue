<script setup lang="ts">
import { inject, onBeforeUnmount, onMounted, ref } from "vue";
import { TERMINAL_PANE_SLOTS } from "@/domain/terminal/pane-slots";

// The place of one pane in the split tree; TerminalPaneLayout teleports the pane's terminal here.
const props = defineProps<{ paneId: string }>();
const slots = inject(TERMINAL_PANE_SLOTS);
const element = ref<HTMLElement>();

onMounted(() => {
  if (element.value) slots?.register(props.paneId, element.value);
});
onBeforeUnmount(() => {
  if (element.value) slots?.unregister(props.paneId, element.value);
});
</script>

<template>
  <div ref="element" class="terminal-pane-slot" :data-terminal-pane-slot="paneId" />
</template>
