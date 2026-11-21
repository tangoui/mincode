<script setup lang="ts">
/**
 * Icon-only sidebar tab bar. Users can hide tabs (right-click or the "⋯"
 * menu, persisted); tabs that do not fit the sidebar width move into "⋯".
 * The active tab always stays on the bar, even when hidden or overflowing.
 */
import { computed, onBeforeUnmount, onMounted, ref, type Component } from "vue";
import { useVueI18n } from "@/i18n/messages";
import { Check, MoreFilled } from "@element-plus/icons-vue";
import type { DropdownInstance } from "element-plus";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import {
  loadPersistedValue,
  registerDesktopBrowserSnapshotKeys,
  savePersistedValue,
} from "@/services/persistent-store";

export interface ProjectSidebarTabItem {
  name: string;
  label: string;
  icon: Component;
  badge?: number;
  /** Pinned tabs cannot be hidden. */
  pinned?: boolean;
}

const TAB_WIDTH = 30;
const TAB_GAP = 2;
const MORE_WIDTH = 28;
const BAR_PADDING = 12;

const props = defineProps<{
  items: ProjectSidebarTabItem[];
  modelValue: string;
  storageKey: string;
  fallback: string;
}>();

const emit = defineEmits<{
  "update:modelValue": [name: string];
}>();

const { t } = useVueI18n();

registerDesktopBrowserSnapshotKeys(props.storageKey);
const hidden = ref<string[]>(readStoredHidden());
const root = ref<HTMLElement | null>(null);
const width = ref(Number.POSITIVE_INFINITY);
const moreDropdown = ref<DropdownInstance>();
const contextMenu = ref({ open: false, x: 0, y: 0, name: "" });
let resizeObserver: ResizeObserver | null = null;

function normalizeHidden(value: unknown) {
  return Array.isArray(value) ? value.filter((name): name is string => typeof name === "string") : [];
}

function readStoredHidden() {
  try {
    return normalizeHidden(JSON.parse(localStorage.getItem(props.storageKey) ?? "[]"));
  } catch {
    return [];
  }
}

const hiddenSet = computed(() => new Set(hidden.value));
const hideable = computed(() => props.items.filter((item) => !item.pinned));
const shown = computed(() =>
  props.items.filter((item) => item.pinned || !hiddenSet.value.has(item.name) || item.name === props.modelValue)
);
const capacity = computed(() =>
  Math.max(1, Math.floor((width.value - BAR_PADDING - MORE_WIDTH + TAB_GAP) / (TAB_WIDTH + TAB_GAP)))
);
const inline = computed(() => {
  const list = shown.value;
  if (list.length <= capacity.value) return list;
  const head = list.slice(0, capacity.value);
  const active = list.find((item) => item.name === props.modelValue);
  if (active && !head.includes(active)) head[head.length - 1] = active;
  return head;
});
const overflow = computed(() => shown.value.filter((item) => !inline.value.includes(item)));
const overflowBadge = computed(() => overflow.value.reduce((sum, item) => sum + (item.badge ?? 0), 0));

function isVisible(item: ProjectSidebarTabItem) {
  return item.pinned || !hiddenSet.value.has(item.name);
}

function setHidden(next: string[]) {
  hidden.value = next;
  void savePersistedValue(props.storageKey, next);
  if (next.includes(props.modelValue)) emit("update:modelValue", props.fallback);
}

function toggleVisible(item: ProjectSidebarTabItem) {
  if (item.pinned) return;
  setHidden(
    isVisible(item) ? [...hidden.value, item.name] : hidden.value.filter((name) => name !== item.name)
  );
}

function showAll() {
  setHidden([]);
}

function select(name: string) {
  if (name !== props.modelValue) emit("update:modelValue", name);
}

function selectFromMenu(name: string) {
  select(name);
  moreDropdown.value?.handleClose();
}

function openContextMenu(event: MouseEvent, name = "") {
  contextMenu.value = { open: true, x: event.clientX, y: event.clientY, name };
}

function closeContextMenu() {
  contextMenu.value.open = false;
}

const contextItem = computed(() => props.items.find((item) => item.name === contextMenu.value.name));

function hideContextItem() {
  const item = contextItem.value;
  closeContextMenu();
  if (item && isVisible(item)) toggleVisible(item);
}

function formatBadge(value: number) {
  return value > 99 ? "99+" : String(value);
}

onMounted(async () => {
  if (root.value) {
    resizeObserver = new ResizeObserver(([entry]) => {
      width.value = entry.contentRect.width + BAR_PADDING;
    });
    resizeObserver.observe(root.value);
  }
  const stored = await loadPersistedValue<unknown>(props.storageKey);
  if (stored !== null) hidden.value = normalizeHidden(stored);
});

onBeforeUnmount(() => {
  resizeObserver?.disconnect();
});
</script>

<template>
  <div
    ref="root"
    class="project-sidebar-tabs"
    role="tablist"
    @contextmenu.prevent="openContextMenu($event)"
  >
    <el-tooltip
      v-for="item in inline"
      :key="item.name"
      :content="item.badge ? `${item.label} (${item.badge})` : item.label"
      placement="bottom"
      :show-after="300"
    >
      <button
        type="button"
        role="tab"
        class="project-sidebar-tabs__tab"
        :class="{ 'is-active': item.name === modelValue }"
        :aria-selected="item.name === modelValue"
        :aria-label="item.label"
        @click="select(item.name)"
        @contextmenu.prevent.stop="openContextMenu($event, item.name)"
      >
        <el-icon><component :is="item.icon" /></el-icon>
        <span v-if="item.badge" class="project-sidebar-tabs__badge">{{ formatBadge(item.badge) }}</span>
      </button>
    </el-tooltip>

    <el-dropdown
      ref="moreDropdown"
      class="project-sidebar-tabs__more"
      trigger="click"
      placement="bottom-end"
      :hide-on-click="false"
      popper-class="project-sidebar-tabs__menu"
    >
      <button
        type="button"
        class="project-sidebar-tabs__tab"
        :aria-label="t('project.sidebarTabs.more')"
        :title="t('project.sidebarTabs.more')"
      >
        <el-icon><MoreFilled /></el-icon>
        <span v-if="overflowBadge" class="project-sidebar-tabs__badge is-dot" />
      </button>
      <template #dropdown>
        <el-dropdown-menu>
          <el-dropdown-item
            v-for="item in overflow"
            :key="`overflow-${item.name}`"
            @click="selectFromMenu(item.name)"
          >
            <el-icon><component :is="item.icon" /></el-icon>
            <span class="project-sidebar-tabs__menu-label">{{ item.label }}</span>
            <span v-if="item.badge" class="project-sidebar-tabs__menu-count">{{ formatBadge(item.badge) }}</span>
          </el-dropdown-item>
          <el-dropdown-item disabled :divided="overflow.length > 0" class="project-sidebar-tabs__menu-heading">
            {{ t("project.sidebarTabs.showTabs") }}
          </el-dropdown-item>
          <el-dropdown-item
            v-for="item in hideable"
            :key="`toggle-${item.name}`"
            @click="toggleVisible(item)"
          >
            <el-icon :class="{ 'is-unchecked': !isVisible(item) }"><Check /></el-icon>
            <span class="project-sidebar-tabs__menu-label">{{ item.label }}</span>
          </el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>

    <AppContextMenu
      :open="contextMenu.open"
      :x="contextMenu.x"
      :y="contextMenu.y"
      :width="200"
      popper-class="project-sidebar-tabs__menu"
      @close="closeContextMenu"
    >
      <el-dropdown-item
        v-if="contextItem"
        :disabled="contextItem.pinned || !isVisible(contextItem)"
        @click="hideContextItem"
      >
        {{ t("project.sidebarTabs.hide", { name: contextItem.label }) }}
      </el-dropdown-item>
      <el-dropdown-item disabled :divided="Boolean(contextItem)" class="project-sidebar-tabs__menu-heading">
        {{ t("project.sidebarTabs.showTabs") }}
      </el-dropdown-item>
      <el-dropdown-item v-for="item in hideable" :key="`context-${item.name}`" @click="toggleVisible(item)">
        <el-icon :class="{ 'is-unchecked': !isVisible(item) }"><Check /></el-icon>
        <span class="project-sidebar-tabs__menu-label">{{ item.label }}</span>
      </el-dropdown-item>
      <el-dropdown-item divided :disabled="!hidden.length" @click="showAll">
        {{ t("project.sidebarTabs.showAll") }}
      </el-dropdown-item>
    </AppContextMenu>
  </div>
</template>
