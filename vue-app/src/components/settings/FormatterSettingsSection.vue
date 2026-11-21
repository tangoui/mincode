<script setup lang="ts">
// Per-language formatter choice (contributes.formatters). Built-in formatting
// is the default; picking a plugin formatter for a language makes Format
// Document use it, and "Format on save" is a separate opt-in that only
// applies to a selected plugin formatter. Preferences are machine-local.
import { computed, onMounted } from "vue";
import { formatterTitle } from "@/domain/plugins/formatters";
import { useVueI18n } from "@/i18n/messages";
import { usePluginFormattersStore } from "@/stores/plugin-formatters";

const BUILTIN = "";
const { t, localeChain } = useVueI18n();
const store = usePluginFormattersStore();

onMounted(() => void store.loadPreferences());

const rows = computed(() =>
  [...new Set([...store.languages, ...store.unavailableLanguages])].sort().map((language) => {
    const preference = store.preferenceFor(language);
    const selected = store.selectedFor(language);
    return {
      language,
      value: preference?.formatter ?? BUILTIN,
      formatOnSave: Boolean(preference?.formatOnSave),
      available: Boolean(selected),
      options: store.forLanguage(language).map((formatter) => ({
        value: formatter.ref,
        label: formatterTitle(formatter, localeChain.value),
        plugin: formatter.pluginName,
      })),
    };
  })
);

function selectFormatter(language: string, value: string) {
  store.setPreference(language, { formatter: value === BUILTIN ? undefined : value });
}

function setFormatOnSave(language: string, value: boolean) {
  store.setPreference(language, { formatOnSave: value });
}
</script>

<template>
  <section v-if="rows.length" class="formatter-settings" :aria-label="t('pluginContrib.formatters.settingsTitle')">
    <div class="settings-flat-row">
      <strong>{{ t("pluginContrib.formatters.settingsTitle") }}</strong>
      <span class="formatter-settings__hint">{{ t("pluginContrib.formatters.settingsDescription") }}</span>
    </div>
    <div v-for="row in rows" :key="row.language" class="settings-flat-row formatter-settings__row">
      <strong><code>{{ row.language }}</code></strong>
      <div class="formatter-settings__controls">
        <el-select
          :model-value="row.value"
          :aria-label="`${t('pluginContrib.formatters.formatter')} · ${row.language}`"
          @change="selectFormatter(row.language, $event as string)"
        >
          <el-option :value="BUILTIN" :label="t('pluginContrib.formatters.builtin')" />
          <el-option
            v-for="option in row.options"
            :key="option.value"
            :value="option.value"
            :label="`${option.label} (${option.plugin})`"
          />
          <el-option v-if="row.value !== BUILTIN && !row.available" :value="row.value" :label="row.value" disabled />
        </el-select>
        <el-checkbox
          :model-value="row.formatOnSave"
          :disabled="row.value === BUILTIN"
          @change="setFormatOnSave(row.language, Boolean($event))"
        >
          {{ t("pluginContrib.formatters.formatOnSave") }}
        </el-checkbox>
      </div>
    </div>
    <el-alert
      v-if="store.loaded && store.unavailableLanguages.length"
      class="settings-theme-alert"
      type="warning"
      :closable="false"
      show-icon
      :title="t('pluginContrib.formatters.selectionUnavailable', { languages: store.unavailableLanguages.join(', ') })"
    />
  </section>
</template>

<style scoped>
.formatter-settings {
  display: contents;
}

.formatter-settings__hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

.formatter-settings__controls {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  min-width: 0;
}

.formatter-settings__controls .el-select {
  width: min(260px, 100%);
}
</style>
