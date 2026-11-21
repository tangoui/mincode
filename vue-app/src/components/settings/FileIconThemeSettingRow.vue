<script setup lang="ts">
// File icon theme selection (contributes.fileIcons): the built-in MinCode
// icons plus every contributed theme. An unavailable stored selection stays
// selected (shown as unavailable) while the built-in icons are used.
import { computed } from "vue";
import { DEFAULT_FILE_ICON_THEME } from "@/domain/plugins/contribution-settings";
import { fileIconThemeTitle } from "@/domain/projects/file-icon-theme";
import { useVueI18n } from "@/i18n/messages";
import { useFileIconThemeStore } from "@/stores/file-icon-theme";

const props = defineProps<{ theme: string }>();
const emit = defineEmits<{ change: [theme: string] }>();
const { t, localeChain } = useVueI18n();
const store = useFileIconThemeStore();

const pluginOptions = computed(() =>
  store.themes.map((theme) => ({
    value: theme.id,
    label: fileIconThemeTitle(theme, localeChain.value),
    plugin: theme.pluginName,
  }))
);
const showUnavailable = computed(
  () => props.theme !== DEFAULT_FILE_ICON_THEME && !store.themes.some((theme) => theme.id === props.theme)
);
</script>

<template>
  <div class="settings-flat-row">
    <strong>{{ t("pluginContrib.fileIcons.themeLabel") }}</strong>
    <el-select :model-value="theme" @change="emit('change', $event as string)">
      <el-option :value="DEFAULT_FILE_ICON_THEME" :label="t('pluginContrib.fileIcons.builtinTheme')" />
      <el-option-group v-if="pluginOptions.length" :label="t('pluginContrib.fileIcons.group')">
        <el-option
          v-for="option in pluginOptions"
          :key="option.value"
          :value="option.value"
          :label="option.label"
          :title="option.plugin"
        />
      </el-option-group>
      <el-option
        v-if="showUnavailable"
        :value="theme"
        :label="t('pluginContrib.fileIcons.unavailableOption', { id: theme })"
      />
    </el-select>
  </div>
  <el-alert
    v-if="showUnavailable && store.unavailable"
    class="settings-theme-alert"
    type="warning"
    :closable="false"
    show-icon
    :title="t('pluginContrib.fileIcons.unavailable', { id: theme })"
  />
</template>
