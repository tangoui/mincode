<script setup lang="ts">
/**
 * One plugin dashboard widget. A view widget renders the plugin's own view in
 * a sandboxed PluginViewFrame (variant "widget") only while the parent keeps
 * it live (visible, at most six frames). A data widget calls its declared
 * `read` action only while the card is visible, the dashboard route is active
 * and the document is visible, at most every 60 s; the result renders as
 * plain text only (never HTML).
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { Hide, Refresh, TopRight } from "@element-plus/icons-vue";
import PluginViewFrame from "@/components/plugins/PluginViewFrame.vue";
import {
  dashboardWidgetTitle,
  shouldRefreshDashboardWidget,
  type DashboardWidget,
  type DashboardWidgetData,
} from "@/domain/plugins/dashboard-widgets";
import { useVueI18n } from "@/i18n/messages";
import { DashboardWidgetDataError, fetchDashboardWidgetData, type DashboardWidgetDataRequest } from "@/services/dashboard-widget-service";
import { executePluginCommand } from "@/services/plugin-commands";
import { isDesktopRuntime } from "@/services/desktop-bridge";

const props = defineProps<{ widget: DashboardWidget; live: boolean; active: boolean }>();
const emit = defineEmits<{ visibility: [key: string, visible: boolean]; hide: [key: string] }>();

const REFRESH_TICK_MS = 15_000;
const { t, localeChain } = useVueI18n();
const root = ref<HTMLElement | null>(null);
const inView = ref(false);
const documentVisible = ref(typeof document === "undefined" ? true : document.visibilityState !== "hidden");
const data = ref<DashboardWidgetData | undefined>();
const loading = ref(false);
const errorText = ref("");
let lastFetchedAt: number | undefined;
let request: DashboardWidgetDataRequest | undefined;
let observer: IntersectionObserver | undefined;
let tick: ReturnType<typeof setInterval> | undefined;

const title = computed(() => dashboardWidgetTitle(props.widget, localeChain.value));
const visible = computed(() => inView.value && props.active && documentVisible.value);
const updatedLabel = computed(() => {
  if (!data.value?.updatedAt) return "";
  return t("pluginContrib.dashboardWidgets.updated", { time: new Date(data.value.updatedAt).toLocaleTimeString() });
});

function cancelRequest() {
  if (!request) return;
  request.cancel();
  request = undefined;
  // The cancelled fetch never settles as current, so reset here and refetch once visible again.
  loading.value = false;
  lastFetchedAt = undefined;
}

async function load(force = false) {
  if (props.widget.kind !== "data") return;
  if (!force && !shouldRefreshDashboardWidget({ refreshSeconds: props.widget.refreshSeconds, lastFetchedAt, now: Date.now(), visible: visible.value })) return;
  if (!isDesktopRuntime()) {
    errorText.value = t("pluginContrib.dashboardWidgets.unavailable");
    return;
  }
  cancelRequest();
  lastFetchedAt = Date.now();
  const current = fetchDashboardWidgetData(props.widget.pluginId, props.widget.id);
  request = current;
  loading.value = true;
  try {
    const result = await current.promise;
    if (request !== current) return;
    data.value = result;
    errorText.value = "";
  } catch (error) {
    if (request !== current) return;
    const detail = error instanceof DashboardWidgetDataError ? error.error.detail || error.error.code : String(error);
    errorText.value = t("pluginContrib.dashboardWidgets.failed", { detail });
  } finally {
    if (request === current) {
      request = undefined;
      loading.value = false;
    }
  }
}

function refresh() {
  if (props.widget.kind === "data") void load(true);
}

async function openCommand() {
  if (!props.widget.command) return;
  try {
    await executePluginCommand(props.widget.pluginId, props.widget.command);
  } catch {
    ElMessage.warning(t("pluginContrib.dashboardWidgets.commandUnavailable"));
  }
}

function handleDocumentVisibility() {
  documentVisible.value = document.visibilityState !== "hidden";
}

watch(visible, (isVisible) => {
  if (isVisible) void load();
  else cancelRequest();
});
watch(
  () => inView.value && props.active,
  (shown) => emit("visibility", props.widget.key, shown),
);
// A new snapshot (plugin update) may change the action: fetch again when visible.
watch(
  () => props.widget,
  (next, previous) => {
    if (next.action === previous.action && next.kind === previous.kind) return;
    lastFetchedAt = undefined;
    data.value = undefined;
    void load();
  },
);

onMounted(() => {
  document.addEventListener("visibilitychange", handleDocumentVisibility);
  if (typeof IntersectionObserver === "undefined" || !root.value) {
    inView.value = true;
  } else {
    observer = new IntersectionObserver((entries) => {
      const entry = entries[entries.length - 1];
      if (entry) inView.value = entry.isIntersecting;
    }, { rootMargin: "100px" });
    observer.observe(root.value);
  }
  if (props.widget.kind === "data") tick = setInterval(() => void load(), REFRESH_TICK_MS);
});

onBeforeUnmount(() => {
  document.removeEventListener("visibilitychange", handleDocumentVisibility);
  observer?.disconnect();
  if (tick) clearInterval(tick);
  cancelRequest();
  emit("visibility", props.widget.key, false);
});
</script>

<template>
  <article ref="root" class="plugin-widget-card" :class="`is-${widget.size}`" :aria-label="title">
    <header class="plugin-widget-card__header">
      <div class="plugin-widget-card__title">
        <strong :title="title">{{ title }}</strong>
        <small :title="widget.pluginName">{{ widget.pluginName }}</small>
      </div>
      <div class="plugin-widget-card__actions">
        <el-button
          v-if="widget.kind === 'data'"
          text
          size="small"
          native-type="button"
          :icon="Refresh"
          :loading="loading"
          :disabled="!visible"
          :aria-label="t('pluginContrib.dashboardWidgets.refresh')"
          :title="t('pluginContrib.dashboardWidgets.refresh')"
          @click="refresh"
        />
        <el-button
          v-if="widget.command"
          text
          size="small"
          native-type="button"
          :icon="TopRight"
          :aria-label="t('pluginContrib.dashboardWidgets.open')"
          :title="t('pluginContrib.dashboardWidgets.open')"
          @click="openCommand"
        />
        <el-button
          text
          size="small"
          native-type="button"
          :icon="Hide"
          :aria-label="t('pluginContrib.dashboardWidgets.hide')"
          :title="t('pluginContrib.dashboardWidgets.hide')"
          @click="emit('hide', widget.key)"
        />
      </div>
    </header>
    <div class="plugin-widget-card__body">
      <template v-if="widget.kind === 'view'">
        <PluginViewFrame
          v-if="live && active && isDesktopRuntime()"
          :plugin-id="widget.pluginId"
          :view-id="widget.view ?? ''"
          :active="visible"
          :drop-target-id="`dashboard-widget-${widget.key}`"
          variant="widget"
          :size="widget.size"
        />
        <div v-else class="plugin-widget-card__placeholder" role="status">
          {{ isDesktopRuntime() ? t("pluginContrib.dashboardWidgets.paused") : t("pluginContrib.dashboardWidgets.unavailable") }}
        </div>
      </template>
      <template v-else>
        <div v-if="errorText" class="plugin-widget-card__placeholder is-error" role="alert">{{ errorText }}</div>
        <div v-else-if="!data" class="plugin-widget-card__placeholder" role="status">
          {{ visible ? t("pluginContrib.dashboardWidgets.loading") : t("pluginContrib.dashboardWidgets.paused") }}
        </div>
        <div v-else-if="!data.items.length" class="plugin-widget-card__placeholder">{{ t("pluginContrib.dashboardWidgets.empty") }}</div>
        <dl v-else class="plugin-widget-card__items">
          <div v-for="(item, index) in data.items" :key="index" :class="`is-${item.severity}`" :title="item.hint">
            <dt>{{ item.label }}</dt>
            <dd>{{ item.value }}</dd>
          </div>
        </dl>
        <footer v-if="data && (data.footnote || updatedLabel)" class="plugin-widget-card__footnote">
          <span>{{ data.footnote }}</span>
          <span>{{ updatedLabel }}</span>
        </footer>
      </template>
    </div>
  </article>
</template>

<style scoped>
.plugin-widget-card {
  display: flex;
  min-width: 0;
  flex-direction: column;
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-surface);
}
.plugin-widget-card__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 9px 8px 7px 12px;
  border-bottom: 1px solid var(--app-border);
}
.plugin-widget-card__title {
  display: grid;
  min-width: 0;
}
.plugin-widget-card__title strong,
.plugin-widget-card__title small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.plugin-widget-card__title strong {
  color: var(--app-text);
  font-size: 13px;
  font-weight: 600;
}
.plugin-widget-card__title small {
  color: var(--app-text-muted);
  font-size: 11px;
}
.plugin-widget-card__actions {
  display: flex;
  flex: none;
  align-items: center;
}
.plugin-widget-card__actions .el-button + .el-button {
  margin-left: 0;
}
.plugin-widget-card__body {
  display: flex;
  min-height: 180px;
  flex-direction: column;
}
.plugin-widget-card.is-medium .plugin-widget-card__body {
  min-height: 280px;
}
.plugin-widget-card__placeholder {
  display: flex;
  flex: 1;
  align-items: center;
  justify-content: center;
  padding: 12px;
  color: var(--app-text-muted);
  font-size: 12px;
  text-align: center;
  overflow-wrap: anywhere;
}
.plugin-widget-card__placeholder.is-error {
  color: var(--el-color-danger);
}
.plugin-widget-card__items {
  display: grid;
  align-content: start;
  gap: 2px;
  margin: 0;
  padding: 8px 12px;
}
.plugin-widget-card__items > div {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  padding: 3px 0;
}
.plugin-widget-card__items dt {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.plugin-widget-card__items dd {
  margin: 0;
  overflow: hidden;
  color: var(--app-text);
  font-size: 13px;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.plugin-widget-card__items .is-success dd {
  color: var(--el-color-success);
}
.plugin-widget-card__items .is-warning dd {
  color: var(--el-color-warning);
}
.plugin-widget-card__items .is-danger dd {
  color: var(--el-color-danger);
}
.plugin-widget-card__footnote {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  margin-top: auto;
  padding: 6px 12px 8px;
  color: var(--app-text-muted);
  font-size: 11px;
}
</style>
