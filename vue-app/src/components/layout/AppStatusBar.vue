<script setup lang="ts">
/**
 * Plugin status bar (contributes.statusBar). Rendered by App.vue in the main
 * window only, and only while at least one item is visible. Items are plain
 * text + optional host icon; plugin HTML is never rendered. Every tooltip
 * names the contributing plugin so plugin text cannot pose as host status.
 */
import { computed, onBeforeUnmount, onMounted } from "vue";
import { useRoute } from "vue-router";
import { ElMessage } from "element-plus";
import { Loading } from "@element-plus/icons-vue";
import {
  resolveStatusBarItems,
  visibleStatusBarItems,
  type StatusBarItem,
} from "@/domain/plugins/status-bar";
import { useVueI18n } from "@/i18n/messages";
import { currentKeyboardPlatform } from "@/services/keyboard-platform";
import { runStatusBarCommand, startStatusBarContextSync } from "@/services/contributions/status-bar";
import { pluginViewIcon } from "@/services/plugin-view-icon";
import { useStatusBarStore } from "@/stores/status-bar";

const { t, locale } = useVueI18n();
const route = useRoute();
const store = useStatusBarStore();
const platform = currentKeyboardPlatform();

const items = computed(() =>
  visibleStatusBarItems(
    resolveStatusBarItems(store.contributions, locale.value),
    store.contexts,
    { platform, view: typeof route.name === "string" ? route.name : "" },
  ),
);
const leftItems = computed(() => items.value.filter((item) => item.alignment === "left"));
const rightItems = computed(() => items.value.filter((item) => item.alignment === "right"));

function severityLabel(item: StatusBarItem) {
  if (item.severity === "warning") return t("pluginContrib.statusBar.severity.warning");
  if (item.severity === "error") return t("pluginContrib.statusBar.severity.error");
  return "";
}

function tooltipText(item: StatusBarItem) {
  const parts = [t("pluginContrib.statusBar.source", { name: item.pluginName || item.pluginId })];
  const severity = severityLabel(item);
  if (severity) parts.push(severity);
  if (item.busy) parts.push(t("pluginContrib.statusBar.busy"));
  if (item.tooltip) parts.push(item.tooltip);
  return parts.join("\n");
}

function ariaLabel(item: StatusBarItem) {
  return [item.text, severityLabel(item), item.busy ? t("pluginContrib.statusBar.busy") : "", item.pluginName]
    .filter(Boolean)
    .join(" · ");
}

async function run(item: StatusBarItem) {
  if (!item.command) return;
  try {
    await runStatusBarCommand(item.pluginId, item.command);
  } catch (error) {
    ElMessage.error(t("pluginContrib.statusBar.commandFailed", {
      error: error instanceof Error ? error.message : String(error),
    }));
  }
}

let stopContextSync: (() => void) | undefined;
onMounted(() => {
  stopContextSync = startStatusBarContextSync();
});
onBeforeUnmount(() => {
  stopContextSync?.();
  stopContextSync = undefined;
});
</script>

<template>
  <div
    v-if="items.length > 0"
    class="app-status-bar"
    role="toolbar"
    :aria-label="t('pluginContrib.statusBar.ariaLabel')"
  >
    <div
      v-for="group in [{ key: 'left', entries: leftItems }, { key: 'right', entries: rightItems }]"
      :key="group.key"
      class="app-status-bar__group"
      :class="`app-status-bar__group--${group.key}`"
    >
      <el-tooltip
        v-for="item in group.entries"
        :key="item.key"
        :content="tooltipText(item)"
        placement="top"
        :show-after="400"
        popper-class="app-status-bar__tooltip"
      >
        <el-button
          text
          size="small"
          class="app-status-bar__item"
          :class="{
            'is-warning': item.severity === 'warning',
            'is-error': item.severity === 'error',
            'is-static': !item.command,
          }"
          :aria-label="ariaLabel(item)"
          :aria-busy="item.busy"
          :tabindex="item.command ? 0 : -1"
          @click="run(item)"
        >
          <el-icon v-if="item.busy" class="app-status-bar__icon is-loading" aria-hidden="true"><Loading /></el-icon>
          <el-icon v-else-if="item.icon" class="app-status-bar__icon" aria-hidden="true">
            <component :is="pluginViewIcon(item.icon)" />
          </el-icon>
          <span class="app-status-bar__text">{{ item.text }}</span>
        </el-button>
      </el-tooltip>
    </div>
  </div>
</template>
