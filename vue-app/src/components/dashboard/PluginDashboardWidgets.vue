<script setup lang="ts">
/**
 * Plugin widgets on the dashboard: at most 12 cards (ordered, not hidden) and
 * at most six live view frames, kept for the most recently visible view
 * widgets. Frames are released when the dashboard route is inactive.
 */
import { onMounted, ref, watch } from "vue";
import { Setting } from "@element-plus/icons-vue";
import PluginWidgetCard from "@/components/dashboard/PluginWidgetCard.vue";
import DashboardWidgetManagerDialog from "@/components/dashboard/DashboardWidgetManagerDialog.vue";
import { createLiveFrameTracker } from "@/domain/plugins/dashboard-widgets";
import { useVueI18n } from "@/i18n/messages";
import { useDashboardWidgetsStore } from "@/stores/dashboard-widgets";

const props = defineProps<{ active: boolean }>();
const { t } = useVueI18n();
const store = useDashboardWidgetsStore();
const managerOpen = ref(false);
const tracker = createLiveFrameTracker();
const liveKeys = ref<ReadonlySet<string>>(new Set());

function syncLive() {
  liveKeys.value = new Set(tracker.keys());
}

function handleVisibility(key: string, visible: boolean) {
  const widget = store.widgets.find((item) => item.key === key);
  if (!visible || widget?.kind !== "view" || !props.active) return;
  tracker.touch(key);
  syncLive();
}

function releaseAll() {
  for (const key of tracker.keys()) tracker.release(key);
  syncLive();
}

watch(
  () => props.active,
  (active) => {
    if (!active) releaseAll();
  },
);
watch(
  () => store.arranged.shown,
  (shown) => {
    const keys = new Set(shown.filter((widget) => widget.kind === "view").map((widget) => widget.key));
    for (const key of tracker.keys()) if (!keys.has(key)) tracker.release(key);
    syncLive();
  },
);

onMounted(() => void store.hydrate());
</script>

<template>
  <section v-if="store.widgets.length" class="plugin-widgets" :aria-label="t('pluginContrib.dashboardWidgets.section')">
    <header class="plugin-widgets__header">
      <div>
        <h2>{{ t("pluginContrib.dashboardWidgets.section") }}</h2>
        <p>{{ t("pluginContrib.dashboardWidgets.sectionDescription") }}</p>
      </div>
      <el-button text size="small" native-type="button" :icon="Setting" @click="managerOpen = true">
        {{ t("pluginContrib.dashboardWidgets.customize") }}
      </el-button>
    </header>
    <div v-if="store.arranged.shown.length" class="plugin-widgets__grid">
      <PluginWidgetCard
        v-for="widget in store.arranged.shown"
        :key="widget.key"
        :widget="widget"
        :live="liveKeys.has(widget.key)"
        :active="active"
        @visibility="handleVisibility"
        @hide="store.setHidden($event, true)"
      />
    </div>
    <p v-if="store.arranged.overflow" class="plugin-widgets__note">
      {{ t("pluginContrib.dashboardWidgets.moreHidden", { count: store.arranged.overflow }) }}
    </p>
    <DashboardWidgetManagerDialog v-model="managerOpen" />
  </section>
</template>

<style scoped>
.plugin-widgets {
  display: grid;
  gap: 12px;
  margin-top: 16px;
}
.plugin-widgets__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.plugin-widgets__header h2 {
  margin: 0 0 4px;
  color: var(--app-text);
  font-size: 14px;
  font-weight: 600;
}
.plugin-widgets__header p,
.plugin-widgets__note {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 11px;
}
.plugin-widgets__grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  align-items: start;
  gap: 16px;
}
</style>
