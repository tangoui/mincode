<script setup lang="ts">
/**
 * Settings → AI → Local Agent (ACP): the agents enabled plugins contribute
 * (`acpAgents` point), selectable like the built-in and custom ones. Selecting
 * one only stores its `plugin:<pluginId>/<agentId>` id; nothing is prepared or
 * started here (the AI assistant confirms and launches it).
 *
 * A selected plugin agent missing from the list is reported only once the
 * list is known (`pluginAcpAgentsReady`); the selection is kept either way.
 */
import { computed } from "vue";
import { Check } from "@element-plus/icons-vue";
import AppHintIcon from "@/components/ui/AppHintIcon.vue";
import { parseAcpAdapterId, pluginAcpAdapterId } from "@/domain/ai/acp-agents";
import { resolvePluginTitle } from "@/domain/plugins/title";
import { entryAvatarInitial, entryAvatarTone } from "@/domain/settings/entry-avatar";
import { useVueI18n } from "@/i18n/messages";
import { pluginAcpAgents, pluginAcpAgentsReady } from "@/services/contributions/acp-agents-state";

const props = defineProps<{ selectedId: string }>();
const emit = defineEmits<{ select: [id: string] }>();

const { t, localeChain } = useVueI18n();

const cards = computed(() =>
  pluginAcpAgents.value.map((agent) => ({
    id: pluginAcpAdapterId(agent.pluginId, agent.agentId),
    title: resolvePluginTitle({ title: agent.title, titleLocalized: agent.titleLocalized }, localeChain.value),
    description: agent.description
      ? resolvePluginTitle({ title: agent.description, titleLocalized: agent.descriptionLocalized }, localeChain.value)
      : "",
    pluginName: agent.pluginName,
  })),
);

/** The stored adapter id as parseAcpAdapterId reads it (trimmed). */
const selected = computed(() => props.selectedId.trim());

const selectedUnavailable = computed(
  () =>
    pluginAcpAgentsReady.value &&
    parseAcpAdapterId(selected.value).kind === "plugin" &&
    !cards.value.some((card) => card.id === selected.value),
);
</script>

<template>
  <div v-if="cards.length || selectedUnavailable" class="settings-ai-agent-card">
    <header>
      <span class="app-inline-label">
        <strong>{{ t("settings.acp.pluginTitle") }}</strong>
        <AppHintIcon :text="t('settings.acp.pluginHint')" />
      </span>
    </header>
    <el-alert
      v-if="selectedUnavailable"
      data-plugin-acp-unavailable
      type="warning"
      :closable="false"
      :title="t('settings.acp.pluginUnavailable')"
    />
    <div
      v-if="cards.length"
      class="settings-ai-card-grid"
      role="radiogroup"
      :aria-label="t('settings.acp.pluginTitle')"
    >
      <article
        v-for="card in cards"
        :key="card.id"
        class="settings-ai-entry-card settings-ai-entry-card--selectable"
        :class="{ 'is-active': selected === card.id }"
        role="radio"
        :aria-checked="selected === card.id"
        tabindex="0"
        @click="emit('select', card.id)"
        @keydown.enter.prevent="emit('select', card.id)"
        @keydown.space.prevent="emit('select', card.id)"
      >
        <header class="settings-ai-entry-card__head">
          <span
            class="settings-ai-entry-card__avatar"
            :data-tone="entryAvatarTone(card.title)"
            aria-hidden="true"
            >{{ entryAvatarInitial(card.title) }}</span
          >
          <div class="settings-ai-entry-card__title">
            <strong :title="card.title">{{ card.title }}</strong>
            <small :title="card.pluginName">{{ card.pluginName }}</small>
          </div>
          <el-icon v-if="selected === card.id" class="settings-ai-entry-card__check" aria-hidden="true"
            ><Check
          /></el-icon>
        </header>
        <p v-if="card.description" class="settings-ai-entry-card__desc">{{ card.description }}</p>
        <footer class="settings-ai-entry-card__foot">
          <span class="settings-ai-entry-card__chip">{{ t("settings.registry.source.plugin") }}</span>
        </footer>
      </article>
    </div>
  </div>
</template>
