<script setup lang="ts">
// Per-language plugin linter choice (contributes.linters). Plugin linters are
// off by default: selecting one for a language sends that language's
// documents to the plugin on open and save; "Lint while typing" is a
// separate opt-in, offered only when the linter supports it. Preferences are
// machine-local.
import { computed, onMounted } from "vue";
import { linterTitle } from "@/domain/plugins/linters";
import { useVueI18n } from "@/i18n/messages";
import { usePluginLintersStore } from "@/stores/plugin-linters";

const NONE = "";
const { t, localeChain } = useVueI18n();
const store = usePluginLintersStore();

onMounted(() => void store.loadPreferences());

const rows = computed(() =>
  [...new Set([...store.languages, ...store.unavailableLanguages])].sort().map((language) => {
    const preference = store.preferenceFor(language);
    const selected = store.selectedFor(language);
    return {
      language,
      value: preference?.linter ?? NONE,
      onType: Boolean(preference?.onType),
      available: Boolean(selected),
      supportsType: Boolean(selected?.triggers.includes("type")),
      options: store.forLanguage(language).map((linter) => ({
        value: linter.ref,
        label: linterTitle(linter, localeChain.value),
        plugin: linter.pluginName,
      })),
    };
  })
);

function selectLinter(language: string, value: string) {
  store.setPreference(language, { linter: value === NONE ? undefined : value });
}

function setOnType(language: string, value: boolean) {
  store.setPreference(language, { onType: value });
}
</script>

<template>
  <section v-if="rows.length" class="linter-settings" :aria-label="t('pluginContrib.linters.settingsTitle')">
    <div class="settings-flat-row">
      <strong>{{ t("pluginContrib.linters.settingsTitle") }}</strong>
      <span class="linter-settings__hint">{{ t("pluginContrib.linters.settingsDescription") }}</span>
    </div>
    <div v-for="row in rows" :key="row.language" class="settings-flat-row linter-settings__row">
      <strong><code>{{ row.language }}</code></strong>
      <div class="linter-settings__controls">
        <el-select
          :model-value="row.value"
          :aria-label="`${t('pluginContrib.linters.linter')} · ${row.language}`"
          @change="selectLinter(row.language, $event as string)"
        >
          <el-option :value="NONE" :label="t('pluginContrib.linters.none')" />
          <el-option
            v-for="option in row.options"
            :key="option.value"
            :value="option.value"
            :label="`${option.label} (${option.plugin})`"
          />
          <el-option v-if="row.value !== NONE && !row.available" :value="row.value" :label="row.value" disabled />
        </el-select>
        <el-checkbox
          :model-value="row.onType && row.supportsType"
          :disabled="!row.supportsType"
          @change="setOnType(row.language, Boolean($event))"
        >
          {{ t("pluginContrib.linters.lintWhileTyping") }}
        </el-checkbox>
      </div>
    </div>
    <el-alert
      v-if="store.loaded && store.unavailableLanguages.length"
      class="settings-theme-alert"
      type="warning"
      :closable="false"
      show-icon
      :title="t('pluginContrib.linters.selectionUnavailable', { languages: store.unavailableLanguages.join(', ') })"
    />
  </section>
</template>

<style scoped>
.linter-settings {
  display: contents;
}

.linter-settings__hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

.linter-settings__controls {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  min-width: 0;
}

.linter-settings__controls .el-select {
  width: min(260px, 100%);
}
</style>
