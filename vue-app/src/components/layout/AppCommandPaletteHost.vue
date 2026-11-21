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
  scope,
  results,
  loading,
  executingId,
  activeProjectName,
  activeProjectRoot,
  projectTarget,
  projectOptions,
  projectScopeAvailable,
  projectSearchError,
  projectSearchTruncated,
  projectSearchEngine,
  projectSearchDurationMs,
  openPalette,
  execute,
} = useCommandPalette({
  router,
  openSettings: () => emit("open-settings"),
});

watch(
  () => props.openRequest.seq,
  () => openPalette({ scope: props.openRequest.scope, prefix: props.openRequest.prefix }),
  { immediate: true },
);
</script>

<template>
  <AppCommandPalette
    v-if="open"
    v-model="open"
    v-model:query="query"
    v-model:scope="scope"
    :results="results"
    :loading="loading"
    :executing-id="executingId"
    :shortcut-label="shortcutLabel"
    :project-name="activeProjectName"
    :project-root="activeProjectRoot"
    v-model:project-target="projectTarget"
    :project-options="projectOptions"
    :project-scope-available="projectScopeAvailable"
    :project-search-error="projectSearchError"
    :truncated="projectSearchTruncated"
    :search-engine="projectSearchEngine"
    :search-duration-ms="projectSearchDurationMs"
    @execute="execute"
  />
</template>
