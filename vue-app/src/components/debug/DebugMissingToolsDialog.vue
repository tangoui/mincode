<script setup lang="ts">
/**
 * Body of the "missing debugger tools" dialog (an Element Plus message box
 * opened by services/debug-guidance.ts): what the debugger's check reported
 * missing, its hints and the install commands. Plugin-provided text is
 * shown as plain text with invisible characters escaped; a command that
 * contains such characters is display-only (it would not run as shown) and
 * says so. Nothing runs from here.
 */
import { computed } from "vue";
import { visibleTerminalText } from "@/domain/terminal/launch-display";
import { useVueI18n } from "@/i18n/messages";
import type { GuidedMissingTool } from "@/services/debug-guidance";

const props = defineProps<{
  debuggerName: string;
  missing: GuidedMissingTool[];
}>();

const { t } = useVueI18n();
const tools = computed(() =>
  props.missing.map((tool, index) => ({
    key: `${index}:${tool.name}`,
    name: visibleTerminalText(tool.name),
    hint: visibleTerminalText(tool.hint),
    command: tool.installCommand ? visibleTerminalText(tool.installCommand) : "",
    runnable: tool.runnable,
  }))
);
const runnable = computed(() => tools.value.some((tool) => tool.command && tool.runnable));
</script>

<template>
  <div class="app-debug-missing-tools__body">
    <p>{{ t("debug.guidance.missingIntro", { debugger: visibleTerminalText(debuggerName) }) }}</p>
    <ul class="app-debug-missing-tools__list">
      <li v-for="tool in tools" :key="tool.key" class="app-debug-missing-tools__item" data-missing-tool>
        <strong>{{ tool.name }}</strong>
        <p v-if="tool.hint" class="app-debug-missing-tools__hint">{{ tool.hint }}</p>
        <code v-if="tool.command" dir="ltr" data-install-command>{{ tool.command }}</code>
        <p v-if="tool.command && !tool.runnable" class="app-debug-missing-tools__hint" data-command-unsafe>
          {{ t("debug.guidance.commandUnsafe") }}
        </p>
      </li>
      <li v-if="!tools.length" class="app-debug-missing-tools__item">{{ t("debug.guidance.missingUnknown") }}</li>
    </ul>
    <p class="app-debug-missing-tools__note">
      {{ runnable ? t("debug.guidance.runNote") : t("debug.guidance.manualNote") }}
    </p>
  </div>
</template>

<style scoped>
.app-debug-missing-tools__body {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

.app-debug-missing-tools__body p {
  margin: 0;
  overflow-wrap: anywhere;
}

.app-debug-missing-tools__list {
  display: grid;
  gap: 8px;
  max-height: 240px;
  margin: 0;
  padding: 0;
  overflow: auto;
  list-style: none;
}

.app-debug-missing-tools__item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.app-debug-missing-tools__hint,
.app-debug-missing-tools__note {
  color: var(--app-text-secondary);
  font-size: 12px;
}

.app-debug-missing-tools__item code {
  padding: 2px 6px;
  border: 1px solid var(--app-border);
  border-radius: 4px;
  background: var(--app-surface-muted);
  font-family: var(--app-font-mono, ui-monospace, monospace);
  font-size: 12px;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}
</style>
