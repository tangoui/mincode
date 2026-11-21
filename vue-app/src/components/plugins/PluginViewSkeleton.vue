<script setup lang="ts">
import { computed } from "vue";
import { useVueI18n } from "@/i18n/messages";
import { pluginViewMetadata } from "@/services/plugin-titles";
import { pluginViewIcon } from "@/services/plugin-view-icon";
import "@/styles/workspaces/plugins/view-skeleton.css";

defineOptions({ name: "PluginViewSkeleton" });

// Host-rendered placeholder shown from the moment a plugin view opens until its
// sandboxed page completes the handshake. It only uses cached catalog metadata
// (no IPC) and never a screenshot of the plugin page.
const props = defineProps<{
  pluginId: string;
  viewId: string;
  /** "compact" drops the header and toolbar (dashboard widget cards). */
  layout?: "page" | "compact";
}>();

const { t } = useVueI18n();
const metadata = computed(() => pluginViewMetadata(props.pluginId.trim(), props.viewId.trim()));
const title = computed(() => metadata.value?.title || props.viewId);
const icon = computed(() => pluginViewIcon(metadata.value?.icon));
</script>

<template>
  <div
    class="plugin-view-skeleton"
    :class="{ 'plugin-view-skeleton--compact': layout === 'compact' }"
    role="status"
    aria-busy="true"
    :aria-label="t('plugins.viewLoadingNamed', { name: title })"
  >
    <header v-if="layout !== 'compact'" class="plugin-view-skeleton__header">
      <el-icon class="plugin-view-skeleton__icon" aria-hidden="true"><component :is="icon" /></el-icon>
      <div class="plugin-view-skeleton__identity">
        <strong class="plugin-view-skeleton__title">{{ title }}</strong>
        <span v-if="metadata?.pluginName && metadata.pluginName !== title" class="plugin-view-skeleton__plugin">{{ metadata.pluginName }}</span>
      </div>
    </header>
    <el-skeleton class="plugin-view-skeleton__body" animated :throttle="0">
      <template #template>
        <div v-if="layout !== 'compact'" class="plugin-view-skeleton__toolbar" aria-hidden="true">
          <el-skeleton-item variant="button" class="plugin-view-skeleton__control" />
          <el-skeleton-item variant="button" class="plugin-view-skeleton__control" />
          <el-skeleton-item variant="text" class="plugin-view-skeleton__search" />
        </div>
        <div class="plugin-view-skeleton__content" aria-hidden="true">
          <el-skeleton-item variant="rect" class="plugin-view-skeleton__block" />
          <el-skeleton-item variant="text" class="plugin-view-skeleton__line" />
          <el-skeleton-item variant="text" class="plugin-view-skeleton__line plugin-view-skeleton__line--short" />
          <el-skeleton-item v-if="layout !== 'compact'" variant="rect" class="plugin-view-skeleton__block plugin-view-skeleton__block--tall" />
          <el-skeleton-item v-if="layout !== 'compact'" variant="text" class="plugin-view-skeleton__line" />
        </div>
      </template>
    </el-skeleton>
  </div>
</template>
