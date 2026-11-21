<script setup lang="ts">
import { watch } from "vue";
import { useRouter } from "vue-router";
import AppCommandPalette from "@/components/layout/AppCommandPalette.vue";
import { useCommandPalette } from "@/composables/use-command-palette";
import type { CommandPaletteOpenRequest } from "@/domain/commands/command-palette";

const props = defineProps<{
  openRequest: CommandPaletteOpenRequest;
  shortcutLabel: string;
}>();
const emit = defineEmits<{ "open-settings": [] }>();
const router = useRouter();
const {
  open,
  query,
  results,
  loading,
  executingId,
  openPalette,
  execute,
} = useCommandPalette({
  router,
  openSettings: () => emit("open-settings"),
});

watch(
  () => props.openRequest.seq,
  () => openPalette({ prefix: props.openRequest.prefix }),
  { immediate: true },
);
</script>

<template>
  <AppCommandPalette
    v-if="open"
    v-model="open"
    v-model:query="query"
    :results="results"
    :loading="loading"
    :executing-id="executingId"
    :shortcut-label="shortcutLabel"
    @execute="execute"
  />
</template>
