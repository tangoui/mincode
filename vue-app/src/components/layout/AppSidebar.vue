<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useTerminalAttentionStore } from "@/stores/terminal-attention";
import { Expand, Fold, MagicStick, Setting } from "@element-plus/icons-vue";
import {
  sidebarLabelsVisible,
  toggleSidebarLabels,
} from "@/services/sidebar-label-preferences";
import { useVueI18n } from "@/i18n/messages";
import { scrollEdgeFades } from "@/domain/layout/scroll-edge-fades";
import { useWorkbenchStore } from "@/stores/workbench";
import { useAiAssistantShellStore } from "@/stores/ai-assistant-shell";
import { useKeybindingsStore } from "@/stores/keybindings";
import { useAppUpdateStore } from "@/stores/app-update";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import {
  sidebarItemIsActive,
  sidebarItemRoute,
  sidebarItemView,
  sidebarMainItems,
  type SidebarItemId,
  type SidebarMainItem,
} from "@/services/sidebar-items";
import { preloadWorkspaceView } from "@/router/workspace-views";
import {
  createDefaultSidebarVisibility,
  loadSidebarVisibility,
  SIDEBAR_VISIBILITY_EVENT,
  type SidebarVisibility,
} from "@/services/sidebar-preferences";
import type { WorkspaceView } from "@/types/workspace";

let disposed = false;
let visibilityChangedByEvent = false;
const props = defineProps<{ settingsOpen?: boolean }>();
const emit = defineEmits<{ "open-settings": [] }>();

// Versioned: a saved order wins over the defaults for every id it already lists,
// so reordering the default list alone would be invisible to anyone who has ever
// dragged an icon. Bump this whenever the shipped order changes on purpose; it
// costs those users one re-drag and is the only way the new default takes effect.
const ORDER_KEY = "app_sidebar_order_v2";
// Keep the manager registered for command-palette navigation, but pin its
// sidebar entry above the footer instead of including it in the draggable list.
const defaultItems = sidebarMainItems;

function mergeOrder(value: unknown) {
  const known = new Set<SidebarItemId>(defaultItems.map((item) => item.id));
  const saved = Array.isArray(value) ? value : [];
  const ordered: SidebarItemId[] = [];

  for (const item of saved) {
    if (
      typeof item === "string" &&
      known.has(item as SidebarItemId) &&
      !ordered.includes(item as SidebarItemId)
    ) {
      ordered.push(item as SidebarItemId);
    }
  }

  // Add newly introduced tools beside their closest preceding feature instead
  // of silently tacking them onto the end of an existing custom order.
  defaultItems.forEach((item, defaultIndex) => {
    if (ordered.includes(item.id)) return;
    const preceding = [...defaultItems.slice(0, defaultIndex)]
      .reverse()
      .find((candidate) => ordered.includes(candidate.id));
    const insertionIndex = preceding ? ordered.indexOf(preceding.id) + 1 : ordered.length;
    ordered.splice(insertionIndex, 0, item.id);
  });

  return ordered;
}

function loadOrder() {
  try {
    return mergeOrder(JSON.parse(localStorage.getItem(ORDER_KEY) || "[]") as unknown);
  } catch {
    return defaultItems.map((item) => item.id);
  }
}

const route = useRoute();
const router = useRouter();
const workbench = useWorkbenchStore();
const assistant = useAiAssistantShellStore();
const { t } = useVueI18n();
const keybindings = useKeybindingsStore();
const aiTooltip = computed(() => {
  const key = keybindings.label("builtin:ai");
  return key ? `${t("ai.chat")} (${key})` : t("ai.chat");
});
const appUpdate = useAppUpdateStore();
const settingsLabel = computed(() => appUpdate.pendingVersion
  ? t("nav.settingsUpdate", { version: appUpdate.pendingVersion })
  : t("nav.settings"));
const order = ref(loadOrder());
const labelsVisible = sidebarLabelsVisible();

// When the rail is too short for every item, fade the edges that hide items so a
// half-visible icon reads as "scroll for more" instead of a clipped button.
const navScroll = ref<{ wrapRef?: HTMLElement } | null>(null);
const navFades = ref({ top: false, bottom: false });
let navResizeObserver: ResizeObserver | undefined;
function updateNavFades() {
  const wrap = navScroll.value?.wrapRef;
  if (!wrap) return;
  const next = scrollEdgeFades({ scrollTop: wrap.scrollTop, clientHeight: wrap.clientHeight, scrollHeight: wrap.scrollHeight });
  if (next.top !== navFades.value.top || next.bottom !== navFades.value.bottom) navFades.value = next;
}
const visibility = ref(createDefaultSidebarVisibility());
const dragging = ref<SidebarItemId>();
// Terminal / sessions workspaces holding a terminal that wants the user back.
const terminalAttention = useTerminalAttentionStore();
const attentionViews = computed(() => {
  const views = new Set<string>();
  for (const paneId of terminalAttention.items.keys()) views.add(terminalAttention.sessionPanes.has(paneId) ? "sessions" : "terminal");
  return views;
});
const items = computed(() =>
  order.value
    .map((view) => defaultItems.find((item) => item.id === view))
    .filter(
      (item): item is (typeof defaultItems)[number] =>
        item !== undefined && visibility.value[item.id]
    )
);

const activeView = computed<WorkspaceView>(() => {
  const tabView = workbench.activeTab?.view;
  if (tabView) return tabView;
  return typeof route.name === "string" ? (route.name as WorkspaceView) : "dashboard";
});

function warmItem(item: SidebarMainItem) {
  void preloadWorkspaceView(sidebarItemView(item));
}
function openItem(item: SidebarMainItem) {
  warmItem(item);
  const view = sidebarItemView(item);
  if (!workbench.openView(view) && view !== "dashboard") return;
  void router.push(sidebarItemRoute(item));
}
function isItemActive(item: SidebarMainItem) {
  return sidebarItemIsActive(item, activeView.value, route.query);
}
function startDrag(view: SidebarItemId) {
  dragging.value = view;
}
function dropOn(view: SidebarItemId) {
  const source = dragging.value;
  dragging.value = undefined;
  if (!source || source === view) return;
  const next = [...order.value];
  const from = next.indexOf(source);
  const to = next.indexOf(view);
  if (from < 0 || to < 0) return;
  next.splice(from, 1);
  next.splice(to, 0, source);
  order.value = next;
  void savePersistedValue(ORDER_KEY, next);
}
function handleVisibilityChange(event: Event) {
  visibilityChangedByEvent = true;
  visibility.value = (event as CustomEvent<SidebarVisibility>).detail;
}


onMounted(async () => {
  const wrap = navScroll.value?.wrapRef;
  if (wrap && typeof ResizeObserver !== "undefined") {
    navResizeObserver = new ResizeObserver(updateNavFades);
    navResizeObserver.observe(wrap);
    if (wrap.firstElementChild) navResizeObserver.observe(wrap.firstElementChild);
  }
  updateNavFades();
  window.addEventListener(SIDEBAR_VISIBILITY_EVENT, handleVisibilityChange);
  const [storedOrder, storedVisibility] = await Promise.all([
    loadPersistedValue<unknown>(ORDER_KEY),
    loadSidebarVisibility(),
  ]);
  if (disposed) return;
  if (Array.isArray(storedOrder)) order.value = mergeOrder(storedOrder);
  // A visibility event during loading is newer than the stored value.
  if (!visibilityChangedByEvent) visibility.value = storedVisibility;
  if (JSON.stringify(storedOrder) !== JSON.stringify(order.value)) await savePersistedValue(ORDER_KEY, order.value);
});
onBeforeUnmount(() => {
  navResizeObserver?.disconnect();
  disposed = true;
  window.removeEventListener(SIDEBAR_VISIBILITY_EVENT, handleVisibilityChange);
});
</script>

<template>
  <aside class="app-activity-bar">
    <el-scrollbar
      ref="navScroll"
      class="app-activity-bar__nav-scroll"
      :class="{ 'is-fade-top': navFades.top, 'is-fade-bottom': navFades.bottom }"
      @scroll="updateNavFades"
    >
      <nav class="app-activity-bar__nav" :aria-label="t('app.vueWorkspace')">
      <el-tooltip
        v-for="item in items"
        :key="item.id"
        :content="t(item.labelKey)"
        placement="right"
        :disabled="labelsVisible"
      >
        <el-button
          class="app-activity-button"
          text
          native-type="button"
          :class="{ 'is-active': isItemActive(item), 'is-dragging': dragging === item.id }"
          :aria-label="t(item.labelKey)"
          :aria-current="isItemActive(item) ? 'page' : undefined"
          draggable="true"
          @pointerenter="warmItem(item)"
          @pointerdown="warmItem(item)"
          @focus="warmItem(item)"
          @click="openItem(item)"
          @dragstart="startDrag(item.id)"
          @dragend="dragging = undefined"
          @dragover.prevent
          @drop.prevent="dropOn(item.id)"
        >
          <el-badge is-dot type="warning" :hidden="!attentionViews.has(item.id)" class="app-activity-button__attention">
            <el-icon><component :is="item.icon" /></el-icon>
          </el-badge>
          <span v-if="labelsVisible" class="app-activity-button__label" :title="t(item.labelKey)">{{ t(item.labelKey) }}</span>
        </el-button>
      </el-tooltip>
      </nav>
    </el-scrollbar>
    <div class="app-activity-bar__bottom">
      <el-tooltip
        :content="labelsVisible ? t('nav.hideSidebarLabels') : t('nav.showSidebarLabels')"
        placement="right"
      >
        <el-button
          class="app-activity-button app-activity-bar__labels-toggle"
          text
          native-type="button"
          :aria-label="labelsVisible ? t('nav.hideSidebarLabels') : t('nav.showSidebarLabels')"
          :aria-pressed="labelsVisible"
          @click="toggleSidebarLabels()"
        >
          <el-icon><Fold v-if="labelsVisible" /><Expand v-else /></el-icon>
        </el-button>
      </el-tooltip>
      <el-tooltip
        v-if="visibility.ai"
        :content="aiTooltip"
        placement="right"
        :disabled="labelsVisible"
      >
        <el-button
          class="app-activity-button"
          text
          native-type="button"
          :class="{ 'is-active': assistant.open || activeView === 'ai' }"
          :aria-label="t('ai.chat')"
          :aria-pressed="assistant.open || activeView === 'ai'"
          @click="assistant.toggle()"
        >
          <el-icon><MagicStick /></el-icon>
          <span v-if="labelsVisible" class="app-activity-button__label" :title="t('ai.chat')">{{ t('ai.chat') }}</span>
        </el-button>
      </el-tooltip>
      <el-tooltip
        :content="settingsLabel"
        placement="right"
        :disabled="labelsVisible"
      >
        <el-button
          class="app-activity-button"
          text
          native-type="button"
          :class="{ 'is-active': props.settingsOpen }"
          :aria-label="settingsLabel"
          :aria-pressed="props.settingsOpen"
          @click="emit('open-settings')"
        >
          <span class="app-activity-button__icon">
            <el-icon><Setting /></el-icon>
            <span v-if="appUpdate.pendingVersion" class="app-activity-button__dot" aria-hidden="true" />
          </span>
          <span v-if="labelsVisible" class="app-activity-button__label" :title="t('nav.settings')">{{ t('nav.settings') }}</span>
        </el-button>
      </el-tooltip>
    </div>
  </aside>
</template>

<style scoped>
.app-activity-button__icon {
  position: relative;
  display: inline-flex;
}

.app-activity-button__dot {
  position: absolute;
  top: -3px;
  right: -4px;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--el-color-danger);
  box-shadow: 0 0 0 1.5px var(--app-sidebar);
}

.app-activity-button__count {
  display: inline-flex;
  line-height: 0;
}

.app-activity-button__count :deep(.el-badge__content) {
  top: 1px;
  right: 4px;
  height: 15px;
  padding: 0 4px;
  border: 0;
  background: var(--app-accent);
  font-size: 10px;
  line-height: 15px;
  box-shadow: 0 0 0 1.5px var(--app-sidebar);
}

.app-activity-bar__labels-toggle {
  flex: 0 0 auto;
}

.app-activity-bar__plugin-manager {
  display: flex;
  flex: 0 0 auto;
  width: 100%;
  justify-content: center;
  padding: 8px 0;
}
</style>
