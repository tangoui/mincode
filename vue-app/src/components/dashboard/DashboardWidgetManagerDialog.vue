<script setup lang="ts">
/** Hide/show and reorder plugin dashboard widgets (workspace-scoped layout). */
import { ArrowDown, ArrowUp } from "@element-plus/icons-vue";
import { dashboardWidgetTitle } from "@/domain/plugins/dashboard-widgets";
import { useVueI18n } from "@/i18n/messages";
import { useDashboardWidgetsStore } from "@/stores/dashboard-widgets";

const open = defineModel<boolean>({ required: true });
const { t, localeChain } = useVueI18n();
const store = useDashboardWidgetsStore();
</script>

<template>
  <el-dialog v-model="open" :title="t('pluginContrib.dashboardWidgets.customizeTitle')" width="min(520px, calc(100vw - 32px))" append-to-body>
    <p class="widget-manager__description">{{ t("pluginContrib.dashboardWidgets.customizeDescription") }}</p>
    <p v-if="!store.ordered.length" class="widget-manager__empty">{{ t("pluginContrib.dashboardWidgets.managerEmpty") }}</p>
    <ol v-else class="widget-manager__list">
      <li v-for="(widget, index) in store.ordered" :key="widget.key" :class="{ 'is-hidden': store.isHidden(widget.key) }">
        <el-switch
          :model-value="!store.isHidden(widget.key)"
          :aria-label="store.isHidden(widget.key) ? t('pluginContrib.dashboardWidgets.show') : t('pluginContrib.dashboardWidgets.hide')"
          @update:model-value="(value: string | number | boolean) => store.setHidden(widget.key, !value)"
        />
        <span class="widget-manager__main">
          <strong>{{ dashboardWidgetTitle(widget, localeChain) }}</strong>
          <small>{{ widget.pluginName }}<template v-if="store.isHidden(widget.key)"> · {{ t("pluginContrib.dashboardWidgets.hidden") }}</template></small>
        </span>
        <el-button
          text
          size="small"
          native-type="button"
          :icon="ArrowUp"
          :disabled="index === 0"
          :aria-label="t('pluginContrib.dashboardWidgets.moveUp')"
          :title="t('pluginContrib.dashboardWidgets.moveUp')"
          @click="store.move(widget.key, -1)"
        />
        <el-button
          text
          size="small"
          native-type="button"
          :icon="ArrowDown"
          :disabled="index === store.ordered.length - 1"
          :aria-label="t('pluginContrib.dashboardWidgets.moveDown')"
          :title="t('pluginContrib.dashboardWidgets.moveDown')"
          @click="store.move(widget.key, 1)"
        />
      </li>
    </ol>
  </el-dialog>
</template>

<style scoped>
.widget-manager__description,
.widget-manager__empty {
  margin: 0 0 12px;
  color: var(--app-text-muted);
  font-size: 12px;
}
.widget-manager__list {
  display: grid;
  gap: 6px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.widget-manager__list li {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 8px;
  border: 1px solid var(--app-border);
  border-radius: 6px;
}
.widget-manager__list li.is-hidden .widget-manager__main {
  opacity: 0.6;
}
.widget-manager__list .el-button + .el-button {
  margin-left: 0;
}
.widget-manager__main {
  display: grid;
  flex: 1;
  min-width: 0;
}
.widget-manager__main strong,
.widget-manager__main small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.widget-manager__main strong {
  color: var(--app-text);
  font-size: 13px;
  font-weight: 500;
}
.widget-manager__main small {
  color: var(--app-text-muted);
  font-size: 11px;
}
</style>
