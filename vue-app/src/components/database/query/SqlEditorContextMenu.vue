<script setup lang="ts">
import { computed } from "vue";
import { Brush, CaretRight, CopyDocument, Scissor, Select, Upload, VideoPlay } from "@element-plus/icons-vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import { useVueI18n } from "@/i18n/messages";
import { useKeybindingsStore } from "@/stores/keybindings";

export type SqlEditorMenuAction = "run" | "run-all" | "format" | "cut" | "copy" | "paste" | "select-all";

defineProps<{
  open: boolean;
  x: number;
  y: number;
  hasSelection: boolean;
}>();
const emit = defineEmits<{
  close: [];
  action: [action: SqlEditorMenuAction];
}>();

const { t } = useVueI18n();
const keybindings = useKeybindingsStore();
const runShortcut = computed(() => keybindings.label("database:run-query"));

function choose(action: SqlEditorMenuAction) {
  emit("action", action);
  emit("close");
}
</script>

<template>
  <AppContextMenu
    :open="open"
    :x="x"
    :y="y"
    :width="236"
    popper-class="db-sql-editor-menu"
    :aria-label="t('database.sqlEditor')"
    @close="emit('close')"
  >
    <el-dropdown-item @click="choose('run')">
      <el-icon><CaretRight /></el-icon>{{ t("database.sqlEditorMenu.run") }}
      <kbd v-if="runShortcut" class="db-sql-editor-menu__shortcut">{{ runShortcut }}</kbd>
    </el-dropdown-item>
    <el-dropdown-item @click="choose('run-all')">
      <el-icon><VideoPlay /></el-icon>{{ t("database.runAllSql") }}
    </el-dropdown-item>
    <el-dropdown-item @click="choose('format')">
      <el-icon><Brush /></el-icon>{{ t("database.formatSql") }}
    </el-dropdown-item>
    <el-dropdown-item divided :disabled="!hasSelection" @click="choose('cut')">
      <el-icon><Scissor /></el-icon>{{ t("database.sqlEditorMenu.cut") }}
    </el-dropdown-item>
    <el-dropdown-item :disabled="!hasSelection" @click="choose('copy')">
      <el-icon><CopyDocument /></el-icon>{{ t("common.copy") }}
    </el-dropdown-item>
    <el-dropdown-item @click="choose('paste')">
      <el-icon><Upload /></el-icon>{{ t("terminal.context.paste") }}
    </el-dropdown-item>
    <el-dropdown-item @click="choose('select-all')">
      <el-icon><Select /></el-icon>{{ t("terminal.context.selectAll") }}
    </el-dropdown-item>
  </AppContextMenu>
</template>
