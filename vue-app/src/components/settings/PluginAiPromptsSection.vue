<script setup lang="ts">
/**
 * Settings → AI → Skills: prompts and skills contributed by enabled plugins
 * (`aiPrompts` point). Users can switch each entry off and review its text;
 * the text is rendered as plain text only.
 */
import { computed, onMounted, ref } from "vue";
import { ChatLineSquare, MagicStick } from "@element-plus/icons-vue";
import { pluginAiPromptDescription, pluginAiPromptTitle, type PluginAiPrompt } from "@/domain/ai/plugin-prompts";
import { useVueI18n } from "@/i18n/messages";
import { useAiPluginPromptsStore } from "@/stores/ai-plugin-prompts";

const { t, localeChain } = useVueI18n();
const store = useAiPluginPromptsStore();
const expanded = ref<string | null>(null);

onMounted(() => void store.hydrate());

const entries = computed(() =>
  store.contributed.map((entry) => ({
    entry,
    title: pluginAiPromptTitle(entry, localeChain.value),
    description: pluginAiPromptDescription(entry, localeChain.value),
  })),
);

function toggleContent(entry: PluginAiPrompt) {
  expanded.value = expanded.value === entry.key ? null : entry.key;
}

function variableList(entry: PluginAiPrompt) {
  const names = entry.variables.map((name) => `{{${name}}}`).join(", ");
  return t("pluginContrib.aiPrompts.variables", { names });
}

function onToggle(entry: PluginAiPrompt, value: string | number | boolean) {
  void store.setEnabled(entry.key, value === true);
}
</script>

<template>
  <section v-if="entries.length" class="plugin-ai-prompts" :aria-label="t('pluginContrib.aiPrompts.settingsSection')">
    <header class="plugin-ai-prompts__head">
      <strong>{{ t("pluginContrib.aiPrompts.settingsSection") }}</strong>
      <p>{{ t("pluginContrib.aiPrompts.settingsHint") }}</p>
    </header>
    <div class="settings-ai-card-grid">
      <article v-for="{ entry, title, description } in entries" :key="entry.key" class="settings-ai-entry-card">
        <header class="settings-ai-entry-card__head">
          <span class="settings-ai-entry-card__avatar" data-tone="accent" aria-hidden="true">
            <el-icon><MagicStick v-if="entry.kind === 'skill'" /><ChatLineSquare v-else /></el-icon>
          </span>
          <div class="settings-ai-entry-card__title">
            <strong :title="title">{{ title }}</strong>
            <small :title="entry.pluginName">{{ t("pluginContrib.aiPrompts.fromPlugin", { name: entry.pluginName }) }}</small>
          </div>
          <el-switch
            :model-value="store.isEnabled(entry.key)"
            size="small"
            :aria-label="`${t('pluginContrib.aiPrompts.enabled')}: ${title}`"
            @update:model-value="onToggle(entry, $event)"
          />
        </header>
        <p class="settings-ai-entry-card__desc">{{ description || title }}</p>
        <footer class="settings-ai-entry-card__foot">
          <span class="settings-ai-entry-card__chip">{{
            entry.kind === "skill" ? t("pluginContrib.aiPrompts.pluginSkill") : t("pluginContrib.aiPrompts.suggestionPrompt")
          }}</span>
          <div class="settings-ai-entry-card__actions">
            <el-button text size="small" :aria-expanded="expanded === entry.key" @click="toggleContent(entry)">{{
              expanded === entry.key ? t("pluginContrib.aiPrompts.hideContent") : t("pluginContrib.aiPrompts.viewContent")
            }}</el-button>
          </div>
        </footer>
        <div v-if="expanded === entry.key" class="plugin-ai-prompts__content">
          <small v-if="entry.kind === 'skill'">{{ t("pluginContrib.aiPrompts.invokeHint", { key: entry.key }) }}</small>
          <small v-else-if="entry.variables.length">{{ variableList(entry) }}</small>
          <el-scrollbar max-height="220px">
            <pre>{{ entry.body }}</pre>
          </el-scrollbar>
        </div>
      </article>
    </div>
  </section>
</template>

<style scoped>
.plugin-ai-prompts {
  display: grid;
  gap: 10px;
  margin-top: 16px;
}

.plugin-ai-prompts__head {
  display: grid;
  gap: 4px;
}

.plugin-ai-prompts__head strong {
  color: var(--app-text);
  font-size: 13px;
}

.plugin-ai-prompts__head p {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 12px;
  line-height: 1.5;
}

.plugin-ai-prompts__content {
  display: grid;
  gap: 6px;
  min-width: 0;
}

.plugin-ai-prompts__content small {
  color: var(--app-text-muted);
  font-size: 11px;
  overflow-wrap: anywhere;
}

.plugin-ai-prompts__content pre {
  margin: 0;
  padding: 8px;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-canvas);
  color: var(--app-text);
  font-family: var(--app-font-mono, monospace);
  font-size: 11px;
  line-height: 1.5;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
