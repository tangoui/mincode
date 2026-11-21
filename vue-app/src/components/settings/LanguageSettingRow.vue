<script setup lang="ts">
import { computed } from "vue";
import type { LocalePackEntry } from "@/domain/i18n/locale-pack";
import { useVueI18n } from "@/i18n/messages";
import { localePackState, setLocalePackSelection } from "@/services/locale-pack-loader";

// Built-in languages plus plugin language packs (contributes.localizations).
// Selecting a pack keeps `locale` as its fallback language; pack names are
// third-party text and are rendered as text only.
const props = defineProps<{ locale: "zh" | "en" }>();
const emit = defineEmits<{ "change-locale": [locale: "zh" | "en"] }>();
const { t } = useVueI18n();

const PACK_PREFIX = "pack:";
const SEPARATOR = "\n";

function packValue(pluginId: string, tag: string) {
  return `${PACK_PREFIX}${pluginId}${SEPARATOR}${tag}`;
}

function packLabel(pack: LocalePackEntry) {
  const name = pack.localizedLanguageName === pack.languageName
    ? pack.languageName
    : `${pack.localizedLanguageName} (${pack.languageName})`;
  return `${name} · ${pack.pluginName}`;
}

const packs = computed(() => localePackState.packs.value);
const selection = computed(() => localePackState.selection.value);
const status = computed(() => localePackState.status.value);

const selectedValue = computed(() => {
  const current = selection.value;
  if (!current) return props.locale;
  // A stored pack without a provider is still shown as selected.
  const pack = packs.value.find(item => item.locale === current.tag && (!current.pluginId || item.pluginId === current.pluginId));
  return packValue(pack?.pluginId ?? current.pluginId ?? "", current.tag);
});

const missingSelection = computed(() => {
  const current = selection.value;
  if (!current) return null;
  const known = packs.value.some(item => item.locale === current.tag && (!current.pluginId || item.pluginId === current.pluginId));
  return known ? null : { value: packValue(current.pluginId ?? "", current.tag), label: current.tag };
});

function onChange(value: string) {
  if (value === "zh" || value === "en") {
    setLocalePackSelection(null);
    emit("change-locale", value);
    return;
  }
  if (!value.startsWith(PACK_PREFIX)) return;
  const rest = value.slice(PACK_PREFIX.length);
  const index = rest.indexOf(SEPARATOR);
  if (index < 0) return;
  const pluginId = rest.slice(0, index);
  const tag = rest.slice(index + SEPARATOR.length);
  setLocalePackSelection(pluginId ? { tag, pluginId } : { tag });
}

const activeInfo = computed(() => {
  const value = status.value;
  return value.kind === "active" ? value : null;
});
</script>

<template>
  <div class="settings-flat-row">
    <strong>{{ t("settings.language") }}</strong>
    <el-select :model-value="selectedValue" @change="onChange($event as string)">
      <el-option-group :label="t('pluginContrib.localizations.builtinGroup')">
        <el-option value="zh" label="简体中文" /><el-option value="en" label="English" />
      </el-option-group>
      <el-option-group v-if="packs.length || missingSelection" :label="t('pluginContrib.localizations.group')">
        <el-option
          v-for="pack in packs"
          :key="packValue(pack.pluginId, pack.locale)"
          :value="packValue(pack.pluginId, pack.locale)"
          :label="packLabel(pack)"
        />
        <el-option v-if="missingSelection" :value="missingSelection.value" :label="missingSelection.label" disabled />
      </el-option-group>
    </el-select>
  </div>
  <template v-if="selection">
    <div class="settings-flat-row">
      <strong>{{ t("pluginContrib.localizations.fallbackLanguage") }}</strong>
      <el-select :model-value="locale" @change="emit('change-locale', $event as 'zh' | 'en')">
        <el-option value="zh" label="简体中文" /><el-option value="en" label="English" />
      </el-select>
    </div>
    <p class="settings-language-pack__hint">{{ t("pluginContrib.localizations.fallbackHint") }}</p>
    <p v-if="activeInfo" class="settings-language-pack__hint">
      {{ t("pluginContrib.localizations.providedBy", { plugin: activeInfo.pack.pluginName }) }} ·
      {{ t("pluginContrib.localizations.coverage", { percent: activeInfo.coverage, count: activeInfo.translated }) }}
    </p>
    <p v-if="activeInfo && activeInfo.droppedCount" class="settings-language-pack__hint">
      {{ t("pluginContrib.localizations.droppedKeys", { count: activeInfo.droppedCount }) }}
    </p>
    <p v-if="status.kind === 'loading'" class="settings-language-pack__hint">{{ t("pluginContrib.localizations.loading") }}</p>
    <el-alert
      v-if="status.kind === 'unavailable'"
      type="warning"
      show-icon
      :closable="false"
      :title="t(status.reason === 'failed' ? 'pluginContrib.localizations.loadFailed' : 'pluginContrib.localizations.unavailable', { tag: status.selection.tag })"
    />
  </template>
</template>

<style scoped>
.settings-language-pack__hint {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 12px;
}
</style>
