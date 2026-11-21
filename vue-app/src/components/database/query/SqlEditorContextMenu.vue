<script setup lang="ts">
// Context menu of the CodeMirror SQL editor (SqlCodeEditor.vue): run, format, the clipboard
// actions and the plugin items contributed to `sql/editor/context` (contributes.menus), in
// the same Element Plus menu the terminal and tabs use. Monaco's SQL editor showed the plugin
// items in Monaco's own menu (services/monaco-menus.ts); the item set, its `when` keys and the
// passContext object are unchanged (domain/plugins/menus.ts).
import { computed } from "vue";
import { Brush, CaretRight, CopyDocument, Scissor, Select, Upload, VideoPlay } from "@element-plus/icons-vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import PluginMenuItems from "@/components/plugins/PluginMenuItems.vue";
import { editorHostContext, menuPassContext, type EditorMenuTarget } from "@/domain/plugins/menus";
import { useVueI18n } from "@/i18n/messages";
import { useKeybindingsStore } from "@/stores/keybindings";

export type SqlEditorMenuAction = "run" | "run-all" | "format" | "cut" | "copy" | "paste" | "select-all";

const props = defineProps<{
  open: boolean;
  x: number;
  y: number;
  /** What the menu was opened on: kind "sql", the selection and the connection's engine. */
  target: EditorMenuTarget;
}>();
const emit = defineEmits<{
  close: [];
  action: [action: SqlEditorMenuAction];
}>();

const { t } = useVueI18n();
const keybindings = useKeybindingsStore();
const runShortcut = computed(() => keybindings.label("database:run-query"));
const hasSelection = computed(() => Boolean(props.target.selection));

// Plugin items are evaluated only while the menu is open.
const pluginMenuHost = computed(() => (props.open ? editorHostContext(props.target) : null));
function pluginMenuContext() {
  return menuPassContext("sql/editor/context", props.target);
}

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
    <PluginMenuItems menu="sql/editor/context" :host="pluginMenuHost" :context="pluginMenuContext" @run="emit('close')" />
  </AppContextMenu>
</template>
