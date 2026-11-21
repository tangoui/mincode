<script setup lang="ts">
import { computed } from "vue";
import { DEFAULT_EDITOR_PREFERENCES, type ProjectEditorPreferences } from "@/domain/projects/types";
import { useVueI18n, type MessageKey } from "@/i18n/messages";

const props = defineProps<{ preferences: ProjectEditorPreferences; native: boolean }>();
const emit = defineEmits<{
  change: [patch: Partial<ProjectEditorPreferences>];
  reset: [];
  close: [];
  formatters: [];
}>();
const { t } = useVueI18n();
const modes = ["off", "afterDelay", "onFocusChange", "onWindowChange"] as const;
const numbers = ["on", "off", "relative"] as const;
const whitespace = ["none", "selection", "all"] as const;
const groups = computed(() => [
  { title: "project.editor.saveGroup", keys: ["formatOnSave", "trimTrailingWhitespace", "insertFinalNewline"] },
  { title: "project.editor.editGroup", keys: ["formatOnPaste", "formatOnType"] },
  { title: "project.editor.displayGroup", keys: ["minimap", "indentationGuides", "bracketPairColorization", "stickyScroll"] },
] as const);
function label(key: string) { return t(`project.editor.${key}` as MessageKey); }
</script>

<template>
  <!-- Compact controls: labels are 12px, so controls use the matching small size. -->
  <div class="project-editor-settings">
    <p class="project-editor-settings__hint">{{ t("project.editor.scopeHint") }}</p>
    <el-scrollbar max-height="min(64vh, 540px)">
      <div class="project-editor-settings__row">
        <span>{{ t("project.editor.autoSave") }}</span>
        <el-select size="small" :model-value="preferences.autoSave" :disabled="!native"
          :aria-label="t('project.editor.autoSave')" @change="emit('change', { autoSave: $event })">
          <el-option v-for="mode in modes" :key="mode" :value="mode" :label="label(`autoSave.${mode}`)" />
        </el-select>
      </div>
      <div v-if="preferences.autoSave === 'afterDelay'" class="project-editor-settings__row">
        <span>{{ t("project.editor.autoSaveDelay") }}</span>
        <el-input-number size="small" :model-value="preferences.autoSaveDelay" :min="500" :max="10000" :step="500"
          :disabled="!native" :aria-label="t('project.editor.autoSaveDelay')"
          @update:model-value="emit('change', { autoSaveDelay: $event ?? DEFAULT_EDITOR_PREFERENCES.autoSaveDelay })" />
      </div>
      <p class="project-editor-settings__hint">{{ t(native ? "project.editor.autoSaveHint" : "project.editor.nativeOnly") }}</p>
      <section v-for="group in groups" :key="group.title" class="project-editor-settings__group">
        <h4>{{ t(group.title) }}</h4>
        <div v-for="key in group.keys" :key="key" class="project-editor-settings__row">
          <span>{{ label(key) }}</span>
          <el-switch size="small" :model-value="props.preferences[key]" :aria-label="label(key)"
            @update:model-value="emit('change', { [key]: Boolean($event) })" />
        </div>
      </section>
      <p class="project-editor-settings__hint">{{ t("project.editor.formatHint") }}</p>
      <el-button text size="small" @click="emit('formatters')">{{ t("project.editor.formatters") }}</el-button>
      <div class="project-editor-settings__row">
        <span>{{ t("project.editor.fontSize") }}</span>
        <el-input-number size="small" :model-value="preferences.fontSize" :min="10" :max="28"
          :aria-label="t('project.editor.fontSize')" @update:model-value="emit('change', { fontSize: $event ?? 13 })" />
      </div>
      <div class="project-editor-settings__row">
        <span>{{ t("project.editor.tabSize") }}</span>
        <el-segmented size="small" :model-value="preferences.tabSize" :options="[2, 4]"
          :aria-label="t('project.editor.tabSize')" @update:model-value="emit('change', { tabSize: Number($event) })" />
      </div>
      <div class="project-editor-settings__row">
        <span>{{ t("project.editor.wordWrap") }}</span>
        <el-switch size="small" :model-value="preferences.wordWrap === 'on'" :aria-label="t('project.editor.wordWrap')"
          @update:model-value="emit('change', { wordWrap: $event ? 'on' : 'off' })" />
      </div>
      <div class="project-editor-settings__row">
        <span>{{ t("project.editor.lineNumbers") }}</span>
        <el-select size="small" :model-value="preferences.lineNumbers" :aria-label="t('project.editor.lineNumbers')"
          @change="emit('change', { lineNumbers: $event })">
          <el-option v-for="mode in numbers" :key="mode" :value="mode" :label="label(`lineNumbers.${mode}`)" />
        </el-select>
      </div>
      <div class="project-editor-settings__row">
        <span>{{ t("project.editor.renderWhitespace") }}</span>
        <el-select size="small" :model-value="preferences.renderWhitespace" :aria-label="t('project.editor.renderWhitespace')"
          @change="emit('change', { renderWhitespace: $event })">
          <el-option v-for="mode in whitespace" :key="mode" :value="mode" :label="label(`renderWhitespace.${mode}`)" />
        </el-select>
      </div>
    </el-scrollbar>
    <div class="project-editor-settings__actions">
      <el-button text size="small" @click="emit('reset')">{{ t("project.editor.reset") }}</el-button>
      <el-button type="primary" size="small" @click="emit('close')">{{ t("common.close") }}</el-button>
    </div>
  </div>
</template>
