<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from "vue";
import type { DropdownInstance } from "element-plus";

const MENU_MARGIN = 8;

interface PopperClampState {
  rects: { popper: { width: number; height: number } };
  modifiersData: { popperOffsets?: { x: number; y: number } };
}

const props = withDefaults(
  defineProps<{
    open: boolean;
    x: number;
    y: number;
    width?: number;
    /** 显式指定时跳过自动空间计算。 */
    maxHeight?: string;
    popperClass?: string;
    teleported?: boolean;
    /** Accessible name of the menu. */
    ariaLabel?: string;
  }>(),
  {
    width: 218,
    maxHeight: "calc(100vh - 16px)",
    popperClass: "",
    teleported: true,
    ariaLabel: undefined,
  }
);

const emit = defineEmits<{
  close: [];
}>();

const dropdown = ref<DropdownInstance>();
const virtualTrigger = ref<HTMLElement>();
const menuList = ref<{ $el?: unknown }>();
let syncingVisibility = false;

/**
 * el-dropdown-menu 不透传 aria-label，且 aria-labelledby 指向虚拟触发时并不渲染的触发元素；
 * 有 ariaLabel 时直接给菜单列表命名。
 */
function labelMenu() {
  const list = menuList.value?.$el;
  if (!props.ariaLabel || !(list instanceof HTMLElement)) return;
  list.setAttribute("aria-label", props.ariaLabel);
  list.removeAttribute("aria-labelledby");
}

/**
 * 虚拟触发场景不依赖 flip/preventOverflow，而是在 Popper 得到真实菜单尺寸后
 * 直接修正最终坐标。菜单超出哪一侧就整体移回视口；仅当菜单自身高于视口时滚动。
 */
const popperOptions = {
  strategy: "fixed" as const,
  modifiers: [
    { name: "flip", enabled: false },
    { name: "preventOverflow", enabled: false },
    {
      name: "appContextMenuViewportClamp",
      enabled: true,
      phase: "main" as const,
      requires: ["popperOffsets"],
      requiresIfExists: ["offset"],
      fn({ state }: { state: PopperClampState }) {
        const offsets = state.modifiersData.popperOffsets;
        if (!offsets) return;
        const maxX = Math.max(
          MENU_MARGIN,
          window.innerWidth - state.rects.popper.width - MENU_MARGIN
        );
        const maxY = Math.max(
          MENU_MARGIN,
          window.innerHeight - state.rects.popper.height - MENU_MARGIN
        );
        offsets.x = Math.min(Math.max(offsets.x, MENU_MARGIN), maxX);
        offsets.y = Math.min(Math.max(offsets.y, MENU_MARGIN), maxY);
      },
    },
  ],
};

/**
 * 虚拟触发的 el-dropdown 不注册外部点击关闭，这里自行兜底：
 * 点击落在任何 EP 弹出层之外时关闭菜单，同时避免多个菜单并存。
 */
function handleGlobalMouseDown(event: MouseEvent) {
  if (!props.open) return;
  const target = event.target as HTMLElement | null;
  if (target?.closest?.(".el-popper")) return;
  emit("close");
}

/**
 * Escape 关闭打开的菜单（焦点在菜单、菜单项或仍在触发元素上时都适用），关闭后焦点按下方规则归还。
 * 只在菜单弹层确实在文档中且可见时处理，并跳过已被别处处理的事件：KeepAlive 隐藏的视图里
 * 遗留的菜单不能吞掉其他视图的 Esc。
 */
function menuVisible() {
  const list = menuList.value?.$el;
  if (!(list instanceof HTMLElement) || !list.isConnected) return false;
  const popper = list.closest<HTMLElement>(".el-popper") ?? list;
  return popper.getAttribute("aria-hidden") !== "true" && getComputedStyle(popper).display !== "none";
}

function handleGlobalKeydown(event: KeyboardEvent) {
  if (event.key !== "Escape" || event.defaultPrevented || !props.open || !menuVisible()) return;
  event.preventDefault();
  emit("close");
}

watch(
  () => props.open,
  (open) => {
    if (open) {
      window.addEventListener("mousedown", handleGlobalMouseDown, true);
      window.addEventListener("keydown", handleGlobalKeydown);
    } else {
      window.removeEventListener("mousedown", handleGlobalMouseDown, true);
      window.removeEventListener("keydown", handleGlobalKeydown);
    }
  },
  { immediate: true, flush: "post" }
);

onBeforeUnmount(() => {
  window.removeEventListener("mousedown", handleGlobalMouseDown, true);
  window.removeEventListener("keydown", handleGlobalKeydown);
  if (props.open) restoreFocus();
});

/**
 * 关闭时把焦点还给打开菜单时的元素（键盘打开的行、右键时聚焦的控件），
 * 只在焦点仍在菜单里或已丢失（body）时归还：菜单项把焦点移到别处（如重命名输入框）时不抢回。
 */
let returnFocusTo: HTMLElement | null = null;

function restoreFocus() {
  const target = returnFocusTo;
  returnFocusTo = null;
  if (!target?.isConnected) return;
  const active = document.activeElement;
  if (active && active !== document.body && active !== target && !active.closest(".app-context-menu__popper")) return;
  target.focus({ preventScroll: true });
}

watch(
  () => props.open,
  (open, wasOpen) => {
    if (open && !wasOpen) {
      const active = document.activeElement;
      returnFocusTo = active instanceof HTMLElement && active !== document.body ? active : null;
    } else if (!open && wasOpen) {
      restoreFocus();
    }
  },
  { immediate: true }
);

watch(
  () => [props.open, props.x, props.y] as const,
  async ([open]) => {
    await nextTick();
    syncingVisibility = true;
    if (open) dropdown.value?.handleOpen();
    else dropdown.value?.handleClose();
    await nextTick();
    if (open) labelMenu();
    syncingVisibility = false;
  },
  { immediate: true, flush: "post" }
);

function handleVisibleChange(visible: boolean) {
  if (!visible && props.open && !syncingVisibility) emit("close");
}
</script>

<template>
  <span
    ref="virtualTrigger"
    class="app-context-menu__anchor"
    :style="{ left: `${props.x}px`, top: `${props.y}px` }"
    aria-hidden="true"
  />
  <el-dropdown
    ref="dropdown"
    virtual-triggering
    :virtual-ref="virtualTrigger"
    trigger="click"
    placement="bottom-start"
    :hide-on-click="true"
    :show-arrow="false"
    :teleported="props.teleported"
    :max-height="props.maxHeight"
    :popper-options="popperOptions"
    :popper-class="['app-context-menu__popper', props.popperClass].filter(Boolean).join(' ')"
    @visible-change="handleVisibleChange"
  >
    <span class="app-context-menu__virtual-child" aria-hidden="true" />
    <template #dropdown>
      <el-dropdown-menu ref="menuList" :style="{ width: `${props.width}px` }">
        <slot />
      </el-dropdown-menu>
    </template>
  </el-dropdown>
</template>
