<script setup lang="ts">
import { onBeforeUnmount, ref } from "vue";
import { ArrowRight } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";

const props = withDefaults(defineProps<{ divided?: boolean; multipleValues?: boolean }>(), {
  divided: false,
  multipleValues: false,
});
const emit = defineEmits<{
  "filter-value": [];
  "exclude-value": [];
  "select-operator": [operator: "contains" | "notContains" | "lessThan" | "greaterThan" | "isNull" | "isNotNull"];
  clear: [];
}>();
const { t } = useVueI18n();

const FILTER_FLYOUT_WIDTH = 190;
const FILTER_FLYOUT_HEIGHT = 9 * 30 + 8;
const VIEWPORT_MARGIN = 8;
const FLYOUT_GAP = 6;
const open = ref(false);
const trigger = ref<HTMLElement>();
const flyoutStyle = ref({ left: "0px", top: "0px" });
let openTimer: ReturnType<typeof setTimeout> | undefined;
let closeTimer: ReturnType<typeof setTimeout> | undefined;

function clearTimers() {
  if (openTimer !== undefined) clearTimeout(openTimer);
  if (closeTimer !== undefined) clearTimeout(closeTimer);
  openTimer = undefined;
  closeTimer = undefined;
}
function openFlyout() {
  clearTimers();
  openTimer = setTimeout(() => {
    openTimer = undefined;
    const rect = trigger.value?.getBoundingClientRect();
    if (!rect) return;
    const left = rect.right + FLYOUT_GAP + FILTER_FLYOUT_WIDTH > window.innerWidth - VIEWPORT_MARGIN
      ? rect.left - FLYOUT_GAP - FILTER_FLYOUT_WIDTH
      : rect.right + FLYOUT_GAP;
    const top = Math.min(rect.top - 4, Math.max(VIEWPORT_MARGIN, window.innerHeight - FILTER_FLYOUT_HEIGHT - VIEWPORT_MARGIN));
    flyoutStyle.value = { left: `${left}px`, top: `${top}px` };
    open.value = true;
  }, 60);
}
function scheduleClose() {
  clearTimers();
  closeTimer = setTimeout(() => {
    closeTimer = undefined;
    open.value = false;
  }, 180);
}
function choose(action: () => void) {
  clearTimers();
  open.value = false;
  action();
}
onBeforeUnmount(clearTimers);
</script>

<template>
  <div
    ref="trigger"
    class="el-dropdown-menu__item db-grid-filter-submenu__trigger"
    :class="{ 'is-divided': props.divided }"
    role="menuitem"
    aria-haspopup="menu"
    :aria-expanded="open"
    @mouseenter="openFlyout"
    @mouseleave="scheduleClose"
    @click="openFlyout"
  >
    <span>{{ t("database.filter") }}</span>
    <el-icon aria-hidden="true"><ArrowRight /></el-icon>
  </div>
  <Teleport to="body">
    <div
      v-if="open"
      class="el-popper app-context-menu__popper db-grid-filter-submenu__flyout"
      :style="flyoutStyle"
      role="menu"
      @mouseenter="clearTimers"
      @mouseleave="scheduleClose"
    >
      <div class="el-dropdown-menu__item" role="menuitem" @click="choose(() => emit('filter-value'))">
        {{ props.multipleValues ? t("database.filterBySelectedValues") : t("database.filterByCellValue") }}
      </div>
      <div class="el-dropdown-menu__item" role="menuitem" @click="choose(() => emit('exclude-value'))">
        {{ props.multipleValues ? t("database.excludeBySelectedValues") : t("database.excludeByCellValue") }}
      </div>
      <div class="el-dropdown-menu__item" role="menuitem" @click="choose(() => emit('select-operator', 'contains'))">
        {{ t("database.filterOperator.contains") }}
      </div>
      <div class="el-dropdown-menu__item" role="menuitem" @click="choose(() => emit('select-operator', 'notContains'))">
        {{ t("database.filterOperator.notContains") }}
      </div>
      <div class="el-dropdown-menu__item" role="menuitem" @click="choose(() => emit('select-operator', 'lessThan'))">
        {{ t("database.filterOperator.lessThan") }}
      </div>
      <div class="el-dropdown-menu__item" role="menuitem" @click="choose(() => emit('select-operator', 'greaterThan'))">
        {{ t("database.filterOperator.greaterThan") }}
      </div>
      <div class="el-dropdown-menu__item is-divided" role="menuitem" @click="choose(() => emit('select-operator', 'isNull'))">
        {{ t("database.filterOnlyNull") }}
      </div>
      <div class="el-dropdown-menu__item" role="menuitem" @click="choose(() => emit('select-operator', 'isNotNull'))">
        {{ t("database.filterOnlyNotNull") }}
      </div>
      <div class="el-dropdown-menu__item is-divided" role="menuitem" @click="choose(() => emit('clear'))">
        {{ t("database.clearFieldFilter") }}
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.db-grid-filter-submenu__trigger {
  justify-content: space-between;
  width: 100%;
  cursor: pointer;
}
.db-grid-filter-submenu__flyout {
  position: fixed;
  z-index: 2100;
  min-width: 0;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  padding: 4px;
  background: var(--app-surface);
  box-shadow: var(--el-box-shadow-light);
}
.db-grid-filter-submenu__flyout .el-dropdown-menu__item {
  cursor: pointer;
  white-space: nowrap;
}
</style>
