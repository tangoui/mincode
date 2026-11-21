<script setup lang="ts">
/**
 * Plugin items (contributes.menus) inside a host context menu. Render it as
 * the last child of an Element Plus dropdown menu (AppContextMenu or
 * el-dropdown-menu). Items beyond MENU_VISIBLE_PLUGIN_ITEMS collapse behind an
 * inline "More plugin actions" expander that keeps the menu open (mouse and
 * keyboard). `context` builds the passContext object lazily; it is only called
 * for items that declare passContext.
 */
import { computed, ref, watch } from "vue";
import { ArrowDown, ArrowRight, Connection } from "@element-plus/icons-vue";
import type { PluginContextValues } from "@/domain/commands/when";
import type { PluginContextMenuId } from "@/domain/plugins/menus";
import { useVueI18n } from "@/i18n/messages";
import { runPluginMenuItem, usePluginMenuItems, type ResolvedPluginMenuItem } from "@/services/contributions/menus";

const props = defineProps<{
  menu: PluginContextMenuId;
  /** Host context values of the open menu; `null` while the menu is closed. */
  host: PluginContextValues | null;
  context: () => Record<string, unknown>;
}>();

const emit = defineEmits<{
  /** Emitted after an item was chosen (the dropdown closes itself). */
  run: [];
}>();

const { t } = useVueI18n();
const view = usePluginMenuItems(props.menu, () => props.host);
const expanded = ref(false);
const hasItems = computed(() => view.value.visible.length > 0);

// Every menu opening starts collapsed.
watch(
  () => props.host,
  () => {
    expanded.value = false;
  },
);

function choose(entry: ResolvedPluginMenuItem) {
  // runPluginMenuItem reads the context synchronously, before the host closes its menu.
  void runPluginMenuItem(entry.item, props.context);
  emit("run");
}

function toggleOverflow(event: Event) {
  // A prevented click keeps the Element Plus dropdown open.
  event.preventDefault();
  expanded.value = !expanded.value;
}

function handleOverflowKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" && event.key !== " ") return;
  // Element Plus closes on keyboard activation regardless of preventDefault;
  // stop its handler (merged after this one) and toggle here instead.
  event.preventDefault();
  event.stopImmediatePropagation();
  expanded.value = !expanded.value;
}

function itemLabel(entry: ResolvedPluginMenuItem) {
  return t("pluginContrib.menus.pluginItem", { title: entry.title, plugin: entry.item.pluginName });
}
</script>

<template>
  <template v-if="hasItems">
    <el-dropdown-item
      v-for="(entry, index) in view.visible"
      :key="entry.key"
      :divided="index === 0"
      class="app-plugin-menu-item"
      :title="itemLabel(entry)"
      :aria-label="itemLabel(entry)"
      @click="choose(entry)"
    >
      <el-icon><Connection /></el-icon>
      <span class="app-plugin-menu-item__title">{{ entry.title }}</span>
    </el-dropdown-item>
    <template v-if="view.overflow.length > 0">
      <el-dropdown-item
        class="app-plugin-menu-item app-plugin-menu-item--more"
        :aria-expanded="expanded"
        :aria-label="t('pluginContrib.menus.moreCount', { count: view.overflow.length })"
        @click="toggleOverflow"
        @keydown="handleOverflowKeydown"
      >
        <el-icon><component :is="expanded ? ArrowDown : ArrowRight" /></el-icon>
        <span class="app-plugin-menu-item__title">{{ t('pluginContrib.menus.moreCount', { count: view.overflow.length }) }}</span>
      </el-dropdown-item>
      <template v-if="expanded">
        <el-dropdown-item
          v-for="entry in view.overflow"
          :key="entry.key"
          class="app-plugin-menu-item app-plugin-menu-item--nested"
          :title="itemLabel(entry)"
          :aria-label="itemLabel(entry)"
          @click="choose(entry)"
        >
          <span class="app-plugin-menu-item__indent" aria-hidden="true" />
          <el-icon><Connection /></el-icon>
          <span class="app-plugin-menu-item__title">{{ entry.title }}</span>
        </el-dropdown-item>
      </template>
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
