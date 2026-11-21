<script setup lang="ts">
/**
 * "Open With…" / "Reopen With…" entries (contributes.customEditors) inside a
 * host dropdown menu (AppContextMenu or el-dropdown-menu). The entry expands
 * inline, keeping the menu open, into the built-in text editor plus every
 * custom editor whose patterns match `relativePath`. Renders nothing when no
 * plugin editor matches. Emits the chosen editor key or "text".
 */
import { computed, ref, watch } from "vue";
import { ArrowDown, ArrowRight, Check, Document, Grid } from "@element-plus/icons-vue";
import { customEditorTitle, TEXT_EDITOR_ASSOCIATION } from "@/domain/plugins/custom-editors";
import { useVueI18n } from "@/i18n/messages";
import { useCustomEditorsStore } from "@/stores/custom-editors";

const props = defineProps<{
  /** Project-relative path of the file (`null` while the menu is closed). */
  relativePath: string | null;
  /** Current editor key, "text", or undefined when the file is not open. */
  current?: string;
  reopen?: boolean;
  divided?: boolean;
}>();

const emit = defineEmits<{
  select: [key: string];
}>();

const { t, localeChain } = useVueI18n();
const store = useCustomEditorsStore();
const expanded = ref(false);

const options = computed(() => (props.relativePath ? store.resolveForPath(props.relativePath).options : []));
const entries = computed(() => [
  { key: TEXT_EDITOR_ASSOCIATION, title: t("pluginContrib.customEditors.textEditor"), description: "" },
  ...options.value.map((editor) => ({
    key: editor.key,
    title: customEditorTitle(editor, localeChain.value),
    description: t("pluginContrib.customEditors.source", {
      editor: customEditorTitle(editor, localeChain.value),
      plugin: editor.pluginName,
    }),
  })),
]);
const label = computed(() => t(props.reopen ? "pluginContrib.customEditors.reopenWith" : "pluginContrib.customEditors.openWith"));

watch(
  () => props.relativePath,
  () => {
    expanded.value = false;
  },
);

function toggle(event: Event) {
  // A prevented click keeps the Element Plus dropdown open.
  event.preventDefault();
  expanded.value = !expanded.value;
}

function handleKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" && event.key !== " ") return;
  event.preventDefault();
  event.stopImmediatePropagation();
  expanded.value = !expanded.value;
}

function choose(key: string) {
  expanded.value = false;
  emit("select", key);
}
</script>

<template>
  <template v-if="options.length > 0">
    <el-dropdown-item
      :divided="divided"
      class="app-plugin-menu-item app-plugin-menu-item--more"
      :aria-expanded="expanded"
      :aria-label="label"
      @click="toggle"
      @keydown="handleKeydown"
    >
      <el-icon><component :is="expanded ? ArrowDown : ArrowRight" /></el-icon>
      <span class="app-plugin-menu-item__title">{{ label }}</span>
    </el-dropdown-item>
    <template v-if="expanded">
      <el-dropdown-item
        v-for="entry in entries"
        :key="entry.key"
        class="app-plugin-menu-item app-plugin-menu-item--nested"
        :title="entry.description || entry.title"
        :aria-label="entry.description || entry.title"
        :aria-current="entry.key === (current ?? '') ? 'true' : undefined"
        @click="choose(entry.key)"
      >
        <span class="app-plugin-menu-item__indent" aria-hidden="true" />
        <el-icon>
          <Check v-if="entry.key === current" />
          <Document v-else-if="entry.key === 'text'" />
          <Grid v-else />
        </el-icon>
        <span class="app-plugin-menu-item__title">{{ entry.title }}</span>
      </el-dropdown-item>
    </template>
  </template>
</template>

<style scoped>
.app-plugin-menu-item__title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.app-plugin-menu-item__indent {
  flex: none;
  width: 12px;
}
</style>
