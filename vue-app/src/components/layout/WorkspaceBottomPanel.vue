<script setup lang="ts">
/**
 * A resizable tool panel under a workspace's main area (debug sessions, …).
 * The main area is the default slot and stays mounted whatever the panel
 * does; each tab's body (`#tab`) stays mounted while its tab exists, so
 * switching or collapsing keeps terminals and trees. Without tabs only the
 * main area is rendered.
 */
import type { TabPaneName } from "element-plus";
import AppChevron from "@/components/ui/AppChevron.vue";
import AppSplitPane from "@/components/ui/AppSplitPane.vue";
import AppWorkspaceTabs from "@/components/ui/AppWorkspaceTabs.vue";
import { useVueI18n } from "@/i18n/messages";

export interface WorkspaceBottomPanelTab {
  id: string;
  title: string;
  closable: boolean;
}

/** The tab strip's height: what a collapsed panel keeps. */
const HEADER_HEIGHT = 32;
const MIN_PANEL_HEIGHT = 120;
const MIN_MAIN_HEIGHT = 160;

const props = withDefaults(
  defineProps<{
    tabs: WorkspaceBottomPanelTab[];
    activeId: string | null;
    height: number;
    collapsed: boolean;
    ariaLabel?: string;
  }>(),
  { ariaLabel: "" }
);

const emit = defineEmits<{
  "update:activeId": [id: string];
  "update:height": [height: number];
  "update:collapsed": [collapsed: boolean];
  close: [id: string];
}>();

defineSlots<{
  default(): unknown;
  tab(props: { tab: WorkspaceBottomPanelTab; active: boolean }): unknown;
  "tab-label"(props: { tab: WorkspaceBottomPanelTab }): unknown;
}>();

const { t } = useVueI18n();

function maxPanelHeight(containerSize: number): number {
  return Math.max(MIN_PANEL_HEIGHT, containerSize - MIN_MAIN_HEIGHT);
}

function selectTab(name: TabPaneName) {
  const id = String(name);
  emit("update:activeId", id);
  if (props.collapsed) emit("update:collapsed", false);
}

function resizeEnd(value: number) {
  if (props.collapsed || props.tabs.length === 0) return;
  emit("update:height", value);
}
</script>

<template>
  <AppSplitPane
    class="app-bottom-panel-host"
    layout="vertical"
    side="second"
    :model-value="collapsed ? HEADER_HEIGHT : height"
    :collapsed="tabs.length === 0"
    :resizable="tabs.length > 0 && !collapsed"
    :min="collapsed ? HEADER_HEIGHT : MIN_PANEL_HEIGHT"
    :max="collapsed ? HEADER_HEIGHT : maxPanelHeight"
    :second-min="MIN_MAIN_HEIGHT"
    :aria-label="ariaLabel || t('debug.panel.resize')"
    @resize-end="resizeEnd"
  >
    <template #first>
      <div class="app-bottom-panel-host__main">
        <slot />
      </div>
    </template>
    <template #second>
      <section v-if="tabs.length" class="app-bottom-panel" :class="{ 'is-collapsed': collapsed }">
        <header class="app-bottom-panel__header">
          <AppWorkspaceTabs
            class="app-bottom-panel__tabs"
            :model-value="activeId ?? undefined"
            @update:model-value="selectTab"
            @tab-remove="emit('close', String($event))"
          >
            <el-tab-pane v-for="tab in tabs" :key="tab.id" :name="tab.id" :closable="tab.closable">
              <template #label>
                <span class="app-bottom-panel__tab-label" :title="tab.title">
                  <slot name="tab-label" :tab="tab">{{ tab.title }}</slot>
                </span>
              </template>
            </el-tab-pane>
          </AppWorkspaceTabs>
          <el-tooltip :content="t(collapsed ? 'debug.panel.expand' : 'debug.panel.collapse')" placement="top">
            <el-button
              text
              native-type="button"
              class="app-bottom-panel__collapse"
              :aria-label="t(collapsed ? 'debug.panel.expand' : 'debug.panel.collapse')"
              :aria-expanded="!collapsed"
              @click="emit('update:collapsed', !collapsed)"
            >
              <AppChevron :direction="collapsed ? 'up' : 'down'" />
            </el-button>
          </el-tooltip>
        </header>
        <div v-show="!collapsed" class="app-bottom-panel__body">
          <div
            v-for="tab in tabs"
            v-show="tab.id === activeId"
            :key="tab.id"
            class="app-bottom-panel__tab-body"
          >
            <slot name="tab" :tab="tab" :active="tab.id === activeId" />
          </div>
        </div>
      </section>
    </template>
  </AppSplitPane>
</template>
