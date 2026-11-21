<script setup lang="ts">
import { defineAsyncComponent, ref } from "vue";
import { Edit } from "@element-plus/icons-vue";
import AppHintIcon from "@/components/ui/AppHintIcon.vue";
import { EDITOR_THEME_CUSTOM, EDITOR_THEME_FOLLOW } from "@/domain/themes/editor-theme-id";
import { type CustomEditorTheme } from "@/domain/themes/editor-theme-data";
import { useVueI18n } from "@/i18n/messages";

const EditorCustomThemeDialog = defineAsyncComponent(() => import("@/components/settings/EditorCustomThemeDialog.vue"));

defineProps<{ theme: string; custom: CustomEditorTheme }>();
const emit = defineEmits<{ change: [patch: { editorColorTheme?: string; editorCustomTheme?: CustomEditorTheme }] }>();
const { t } = useVueI18n();
const dialogOpen = ref(false);

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
  <EditorCustomThemeDialog
    v-if="dialogOpen"
    :model-value="dialogOpen"
    :theme="custom"
    @update:model-value="dialogOpen = $event"
    @save="saveCustom"
  />
</template>
