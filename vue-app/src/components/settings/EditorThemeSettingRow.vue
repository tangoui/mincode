<script setup lang="ts">
// Editor theme selection (Settings > General): follow the UI theme, the
// user's custom colors, or a contributed theme (contributes.editorThemes).
// An unavailable stored plugin theme stays selected (shown as unavailable)
// while the editor follows the UI theme.
import { computed, defineAsyncComponent, ref } from "vue";
import { Edit } from "@element-plus/icons-vue";
import AppHintIcon from "@/components/ui/AppHintIcon.vue";
import { EDITOR_THEME_CUSTOM, EDITOR_THEME_FOLLOW, isPluginEditorThemeId } from "@/domain/themes/editor-theme-id";
import { editorThemeSwatches, type CustomEditorTheme } from "@/domain/themes/editor-theme-data";
import { editorThemeTitle } from "@/domain/themes/editor-themes";
import { useVueI18n } from "@/i18n/messages";
import { useEditorThemesStore } from "@/stores/editor-themes";

const EditorCustomThemeDialog = defineAsyncComponent(() => import("@/components/settings/EditorCustomThemeDialog.vue"));

const props = defineProps<{ theme: string; custom: CustomEditorTheme }>();
const emit = defineEmits<{ change: [patch: { editorColorTheme?: string; editorCustomTheme?: CustomEditorTheme }] }>();
const { t, localeChain } = useVueI18n();
const store = useEditorThemesStore();
const dialogOpen = ref(false);

const pluginOptions = computed(() =>
  store.themes.map((theme) => ({
    value: theme.id,
    label: editorThemeTitle(theme, localeChain.value),
    kind: theme.kind,
    plugin: theme.pluginName,
    swatch: editorThemeSwatches(theme),
  }))
);
const showUnavailable = computed(
  () => isPluginEditorThemeId(props.theme) && store.loaded && !store.themeById(props.theme)
);

function select(value: string) {
  emit("change", { editorColorTheme: value });
  if (value === EDITOR_THEME_CUSTOM) dialogOpen.value = true;
}

function saveCustom(theme: CustomEditorTheme) {
  emit("change", { editorColorTheme: EDITOR_THEME_CUSTOM, editorCustomTheme: theme });
  dialogOpen.value = false;
}
</script>

<template>
  <div class="settings-flat-row">
    <span class="app-inline-label">
      <strong>{{ t("settings.editorTheme") }}</strong>
      <AppHintIcon :text="t('settings.editorTheme.hint')" />
    </span>
    <div class="app-editor-theme-control">
      <el-select :model-value="theme" @update:model-value="select($event as string)">
        <el-option :value="EDITOR_THEME_FOLLOW" :label="t('settings.editorTheme.follow')" />
        <el-option :value="EDITOR_THEME_CUSTOM" :label="t('settings.editorTheme.custom')" />
        <el-option
          v-if="showUnavailable"
          :value="theme"
          :label="t('pluginContrib.editorThemes.unavailableOption', { id: theme })"
          disabled
        />
        <el-option-group v-if="pluginOptions.length" :label="t('pluginContrib.editorThemes.group')">
          <el-option v-for="option in pluginOptions" :key="option.value" :value="option.value" :label="option.label">
            <span class="app-theme-option">
              <span class="app-theme-option__swatch" aria-hidden="true">
                <span v-for="(color, index) in option.swatch" :key="index" :style="{ backgroundColor: color }" />
              </span>
              <span class="app-theme-option__label">{{ option.label }}</span>
              <span class="app-theme-option__meta">
                {{ option.kind === "dark" ? t("pluginContrib.editorThemes.kindDark") : t("pluginContrib.editorThemes.kindLight") }} · {{ option.plugin }}
              </span>
            </span>
          </el-option>
        </el-option-group>
      </el-select>
      <el-button
        v-if="theme === EDITOR_THEME_CUSTOM"
        :icon="Edit"
        :aria-label="t('settings.editorTheme.edit')"
        :title="t('settings.editorTheme.edit')"
        @click="dialogOpen = true"
      />
    </div>
  </div>
  <el-alert
    v-if="showUnavailable"
    class="settings-theme-alert"
    type="warning"
    :closable="false"
    show-icon
    :title="t('pluginContrib.editorThemes.unavailable', { id: theme })"
  />
  <EditorCustomThemeDialog
    v-if="dialogOpen"
    :model-value="dialogOpen"
    :theme="custom"
    :plugin-themes="store.themes"
    @update:model-value="dialogOpen = $event"
    @save="saveCustom"
  />
</template>
