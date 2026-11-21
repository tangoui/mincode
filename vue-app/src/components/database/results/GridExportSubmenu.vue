<script setup lang="ts">
import { onBeforeUnmount, ref } from "vue";
import { ArrowRight } from "@element-plus/icons-vue";
import type { TableDataFormat } from "@/domain/database/object-naming";
import { useVueI18n } from "@/i18n/messages";

const props = withDefaults(defineProps<{ divided?: boolean }>(), { divided: false });
const emit = defineEmits<{ select: [format: TableDataFormat] }>();
const { t } = useVueI18n();

/** 与工具栏"导出"下拉保持一致的格式清单与标签。 */
const exportOptions: { format: TableDataFormat; label: string }[] = [
  { format: "csv", label: "CSV" },
  { format: "json", label: "JSON" },
  { format: "xlsx", label: "Excel (XLSX)" },
  { format: "markdown", label: "Markdown" },
  { format: "sql", label: "SQL (INSERT)" },
  { format: "txt", label: "TXT" },
];

const FLYOUT_WIDTH = 150;
/** 6 个 30px 菜单项 + 上下 4px padding，用于垂直方向钳制。 */
const FLYOUT_HEIGHT = exportOptions.length * 30 + 8;
/** 与父菜单之间的水平间隙。 */
const FLYOUT_GAP = 6;
const VIEWPORT_MARGIN = 8;
const OPEN_DELAY = 60;
const CLOSE_DELAY = 180;

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
    const overflowRight =
      rect.right + FLYOUT_GAP + FLYOUT_WIDTH > window.innerWidth - VIEWPORT_MARGIN;
    const left = overflowRight
      ? rect.left - FLYOUT_GAP - FLYOUT_WIDTH
      : rect.right + FLYOUT_GAP;
    const top = Math.min(
      rect.top - 4,
      Math.max(VIEWPORT_MARGIN, window.innerHeight - FLYOUT_HEIGHT - VIEWPORT_MARGIN)
    );
    flyoutStyle.value = { left: `${left}px`, top: `${top}px` };
    open.value = true;
  }, OPEN_DELAY);
}

function scheduleClose() {
  clearTimers();
  closeTimer = setTimeout(() => {
    closeTimer = undefined;
    open.value = false;
  }, CLOSE_DELAY);
}

function choose(format: TableDataFormat) {
  clearTimers();
  open.value = false;
  emit("select", format);
}

onBeforeUnmount(clearTimers);
</script>

<template>
  <div
    ref="trigger"
    class="el-dropdown-menu__item db-grid-export-submenu__trigger"
    :class="{ 'is-divided': props.divided }"
    role="menuitem"
    aria-haspopup="menu"
    :aria-expanded="open"
    @mouseenter="openFlyout"
    @mouseleave="scheduleClose"
    @click="openFlyout"
  >
    <span>{{ t("database.export") }}</span>
    <el-icon aria-hidden="true"><ArrowRight /></el-icon>
  </div>
  <Teleport to="body">
    <!-- el-popper 类用于被 AppContextMenu 的外部点击关闭判定识别为弹层内部 -->
    <div
      v-if="open"
      class="el-popper app-context-menu__popper db-grid-export-submenu__flyout"
      :style="flyoutStyle"
      role="menu"
      @mouseenter="clearTimers"
      @mouseleave="scheduleClose"
    >
      <div
        v-for="option in exportOptions"
        :key="option.format"
        class="el-dropdown-menu__item db-grid-export-submenu__option"
        role="menuitem"
        @click="choose(option.format)"
      >
        {{ option.label }}
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.db-grid-export-submenu__trigger {
  justify-content: space-between;
  width: 100%;
  cursor: pointer;
}

.db-grid-export-submenu__flyout {
  position: fixed;
  /* 高于 el-dropdown 动态分配的 popper 层级（2000+），保证浮在菜单之上 */
  z-index: 2100;
  min-width: 0;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  padding: 4px;
  background: var(--app-surface);
  box-shadow: var(--el-box-shadow-light);
}

.db-grid-export-submenu__option {
  cursor: pointer;
  white-space: nowrap;
}
</style>
