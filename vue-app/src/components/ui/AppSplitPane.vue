<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, watchEffect } from "vue";
import type { ComponentPublicInstance } from "vue";
import { splitterRootContextKey, type SplitterRootContext } from "element-plus/es/components/splitter/src/type.mjs";

export type AppSplitPaneLayout = "horizontal" | "vertical";
export type AppSplitPaneUnit = "px" | "percent";
export type AppSplitPaneSide = "first" | "second";
export type AppSplitPaneLimit = number | string | ((containerSize: number) => number);

const props = withDefaults(
  defineProps<{
    modelValue: number;
    layout?: AppSplitPaneLayout;
    side?: AppSplitPaneSide;
    /** Reorder the two semantic slots without remounting their contents. */
    reverse?: boolean;
    collapsed?: boolean;
    collapsedSize?: number;
    collapsible?: boolean;
    resizable?: boolean;
    collapseLabel?: string;
    expandLabel?: string;
    unit?: AppSplitPaneUnit;
    min?: AppSplitPaneLimit;
    max?: AppSplitPaneLimit;
    secondMin?: AppSplitPaneLimit;
    secondMax?: AppSplitPaneLimit;
    lazy?: boolean;
    ariaLabel?: string;
    keyboardStep?: number;
    refreshPanelOnCollapse?: boolean;
  }>(),
  {
    layout: "horizontal",
    side: "first",
    reverse: false,
    collapsed: false,
    collapsedSize: 0,
    collapsible: false,
    resizable: true,
    collapseLabel: "Collapse panel",
    expandLabel: "Expand panel",
    unit: "px",
    min: 0,
    max: Number.POSITIVE_INFINITY,
    secondMin: 0,
    secondMax: Number.POSITIVE_INFINITY,
    lazy: false,
    ariaLabel: "",
    keyboardStep: 0,
    refreshPanelOnCollapse: false,
  }
);

const emit = defineEmits<{
  "update:modelValue": [value: number];
  resizeStart: [value: number];
  resize: [value: number];
  resizeEnd: [value: number];
  "update:collapsed": [value: boolean];
}>();

const root = ref<HTMLElement>();
// Element Plus copies a panel's min/max into the splitter once, when the panel registers,
// and clamps every drag against that copy. A pane that registers while collapsed (0/0)
// would keep those limits after it expands, so the first drag snaps it shut. The live
// limits are written into the registered panel state whenever they change, without
// remounting the sidebar (which would reset editors, trees and subscriptions).
// Compatibility adapter for Element Plus 2.14.3: remove when panel limits become
// reactive upstream. The real-component tests exercise this internal context seam.
const splitter = ref<ComponentPublicInstance>();
function splitterContext(): SplitterRootContext | undefined {
  const provides = (splitter.value?.$ as { provides?: Record<symbol, unknown> } | undefined)?.provides;
  return provides?.[splitterRootContextKey] as SplitterRootContext | undefined;
}
function registeredPanels(): SplitterRootContext["panels"] {
  return splitterContext()?.panels ?? [];
}
const measuredContainerSize = ref(0);
const liveValue = ref(props.modelValue);
let dragger: HTMLElement | undefined;
let collapseControl: HTMLElement | undefined;
let resizeObserver: ResizeObserver | undefined;
let resizeTimer: number | undefined;

const collapsedSize = computed(() => Math.max(0, props.collapsedSize));
const controlledValue = computed(() => (props.collapsed ? collapsedSize.value : props.modelValue));
const panelSize = computed(() => formatSize(controlledValue.value));
// Element Plus clamps every size update against the panel's current min/max.
// Use the collapsed size as both limits so an empty collapsed panel cannot keep
// occupying its expanded minimum width; the original limits return on expand.
const panelMin = computed(() =>
  props.collapsed ? formatSize(collapsedSize.value) : formatLimit(props.min)
);
const panelMax = computed(() =>
  props.collapsed ? formatSize(collapsedSize.value) : formatLimit(props.max)
);
const trailingMin = computed(() => formatTrailingLimit(props.secondMin));
const trailingMax = computed(() => formatTrailingLimit(props.secondMax));
watchEffect(() => {
  const panels = registeredPanels();
  if (panels.length !== 2) return;
  const sized = (props.side === "first") !== props.reverse ? 0 : 1;
  panels[sized].min = panelMin.value;
  panels[sized].max = panelMax.value;
  panels[1 - sized].min = trailingMin.value;
  panels[1 - sized].max = trailingMax.value;
}, { flush: "post" });
// A drag (or the native collapse button) also writes a pixel size into the unsized
// pane's registered state. Element Plus then scales every stored size to fill the
// container, so a later controlled size, such as a bottom panel collapsing to its
// 32px tab strip, is inflated by that stale remainder (32px rendered as ~48px).
// Once no drag is in progress the unsized pane gets its size back to "the rest",
// which equals the remainder it holds, so nothing moves; during a drag Element
// Plus owns both sizes and is left alone.
watchEffect(() => {
  const context = splitterContext();
  if (!context || context.movingIndex || context.panels.length !== 2) return;
  const rest = context.panels[(props.side === "first") !== props.reverse ? 1 : 0];
  if (rest.size !== undefined) rest.size = undefined;
}, { flush: "post" });
const orientation = computed(() => (props.layout === "horizontal" ? "vertical" : "horizontal"));
const effectiveKeyboardStep = computed(() =>
  props.keyboardStep > 0 ? props.keyboardStep : props.unit === "percent" ? 2 : 10
);

function formatSize(value: number) {
  return props.unit === "percent" ? `${value}%` : value;
}

function resolvedLimitValue(value: AppSplitPaneLimit) {
  return typeof value === "function" ? value(measuredContainerSize.value) : value;
}

function formatLimit(value: AppSplitPaneLimit) {
  if (typeof value === "function" && measuredContainerSize.value <= 0) return undefined;
  value = resolvedLimitValue(value);
  if (typeof value === "string") return value;
  if (!Number.isFinite(value)) return undefined;
  return props.unit === "percent" ? `${value}%` : value;
}

function formatTrailingLimit(value: AppSplitPaneLimit) {
  if (typeof value === "function" && measuredContainerSize.value <= 0) return undefined;
  value = resolvedLimitValue(value);
  if (typeof value === "string") return value;
  if (!Number.isFinite(value)) return undefined;
  return value;
}

function containerSize() {
  return measuredContainerSize.value || undefined;
}

function resolveLimit(value: AppSplitPaneLimit, fallback: number) {
  value = resolvedLimitValue(value);
  if (typeof value === "number") return Number.isFinite(value) ? value : fallback;
  const parsed = Number.parseFloat(value);
  if (!Number.isFinite(parsed)) return fallback;
  if (value.endsWith("%")) {
    if (props.unit === "percent") return parsed;
    const size = containerSize();
    return size ? (size * parsed) / 100 : fallback;
  }
  if (value.endsWith("px")) {
    if (props.unit === "px") return parsed;
    const size = containerSize();
    return size ? (parsed / size) * 100 : fallback;
  }
  return parsed;
}

function clamp(value: number) {
  const minimum = resolveLimit(props.min, 0);
  const maximum = resolveLimit(props.max, value);
  return Math.max(minimum, Math.min(maximum, value));
}

function valueFromSizes(sizes: number[]) {
  const firstSize = sizes[(props.side === "first") !== props.reverse ? 0 : 1] ?? 0;
  if (props.unit === "px") return clamp(Math.round(firstSize));
  const size = containerSize();
  if (!size) return clamp(props.modelValue);
  return clamp((firstSize / size) * 100);
}

function roundedValue(value: number) {
  return props.unit === "percent" ? Math.round(value * 100) / 100 : Math.round(value);
}

function handleResizeStart(_index: number, sizes: number[]) {
  if (!props.resizable || props.collapsed) return;
  const value = roundedValue(valueFromSizes(sizes));
  liveValue.value = value;
  emit("resizeStart", value);
  syncAccessibility();
}

function handleResize(_index: number, sizes: number[]) {
  if (!props.resizable || props.collapsed) return;
  const value = roundedValue(valueFromSizes(sizes));
  liveValue.value = value;
  emit("resize", value);
  syncAccessibility();
}

function handleResizeEnd(_index: number, sizes: number[]) {
  if (!props.resizable || props.collapsed) return;
  const value = roundedValue(valueFromSizes(sizes));
  liveValue.value = value;
  emit("update:modelValue", value);
  emit("resizeEnd", value);
  syncAccessibility();
}

function updateFromKeyboard(value: number) {
  const nextValue = roundedValue(clamp(value));
  liveValue.value = nextValue;
  emit("update:modelValue", nextValue);
  emit("resizeEnd", nextValue);
  void nextTick(syncAccessibility);
}

function handleKeydown(event: KeyboardEvent) {
  const isFirst = (props.side === "first") !== props.reverse;
  const decreaseKey = props.layout === "horizontal"
    ? isFirst ? "ArrowLeft" : "ArrowRight"
    : isFirst ? "ArrowUp" : "ArrowDown";
  const increaseKey = props.layout === "horizontal"
    ? isFirst ? "ArrowRight" : "ArrowLeft"
    : isFirst ? "ArrowDown" : "ArrowUp";
  const multiplier = event.shiftKey ? 4 : 1;

  if (event.key === decreaseKey) {
    event.preventDefault();
    updateFromKeyboard(props.modelValue - effectiveKeyboardStep.value * multiplier);
  } else if (event.key === increaseKey) {
    event.preventDefault();
    updateFromKeyboard(props.modelValue + effectiveKeyboardStep.value * multiplier);
  } else if (event.key === "Home") {
    event.preventDefault();
    updateFromKeyboard(resolveLimit(props.min, props.modelValue));
  } else if (event.key === "End") {
    const maximum = resolveLimit(props.max, Number.POSITIVE_INFINITY);
    if (!Number.isFinite(maximum)) return;
    event.preventDefault();
    updateFromKeyboard(maximum);
  }
}

function handleCollapse(_index: number, _type: "start" | "end", _sizes: number[]) {
  if (!props.collapsible) return;
  // Element Plus 在触发 collapse 前会修改内部面板缓存，但事件参数仍可能是旧尺寸。
  // 这里以受控状态切换，避免面板视觉上已收起、状态却仍为展开，进而永久禁用拖拽手柄。
  const nextCollapsed = !props.collapsed;
  if (!nextCollapsed) {
    const value = roundedValue(clamp(props.modelValue));
    liveValue.value = value;
    emit("update:modelValue", value);
    emit("resizeEnd", value);
  }
  emit("update:collapsed", nextCollapsed);
  void nextTick(syncAccessibility);
}

function handleCollapseKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" && event.key !== " ") return;
  event.preventDefault();
  collapseControl?.click();
}

function updateContainerSize() {
  const rect = root.value?.getBoundingClientRect();
  const nextSize = props.layout === "horizontal" ? (rect?.width ?? 0) : (rect?.height ?? 0);
  if (Math.abs(nextSize - measuredContainerSize.value) >= 0.5) {
    measuredContainerSize.value = nextSize;
  }
}

function scheduleContainerSync() {
  if (resizeTimer !== undefined) return;
  resizeTimer = window.setTimeout(() => {
    resizeTimer = undefined;
    updateContainerSize();
    void nextTick(syncAccessibility);
  }, 0);
}

function syncAccessibility() {
  const nextDragger = root.value?.querySelector<HTMLElement>(".el-splitter-bar__dragger") ?? undefined;
  if (dragger !== nextDragger) {
    dragger?.removeEventListener("keydown", handleKeydown);
    dragger = nextDragger;
    dragger?.addEventListener("keydown", handleKeydown);
  }
  if (dragger) {
    dragger.tabIndex = props.collapsed ? -1 : 0;
    dragger.setAttribute("role", "separator");
    dragger.setAttribute("aria-orientation", orientation.value);
    dragger.setAttribute("aria-valuemin", String(roundedValue(resolveLimit(props.min, 0))));
    const maximum = resolveLimit(props.max, Number.POSITIVE_INFINITY);
    if (Number.isFinite(maximum))
      dragger.setAttribute("aria-valuemax", String(roundedValue(maximum)));
    else dragger.removeAttribute("aria-valuemax");
    dragger.setAttribute(
      "aria-valuenow",
      String(roundedValue(props.collapsed ? 0 : liveValue.value))
    );
    if (props.ariaLabel) dragger.setAttribute("aria-label", props.ariaLabel);
    else dragger.removeAttribute("aria-label");
  }

  const nextCollapseControl = props.collapsible
    ? (root.value?.querySelector<HTMLElement>(".el-splitter-bar__collapse-icon") ?? undefined)
    : undefined;
  if (collapseControl !== nextCollapseControl) {
    collapseControl?.removeEventListener("keydown", handleCollapseKeydown);
    collapseControl = nextCollapseControl;
    collapseControl?.addEventListener("keydown", handleCollapseKeydown);
  }
  if (collapseControl) {
    const label = props.collapsed ? props.expandLabel : props.collapseLabel;
    collapseControl.tabIndex = 0;
    collapseControl.setAttribute("role", "button");
    collapseControl.setAttribute("aria-label", label);
    collapseControl.setAttribute("title", label);
    collapseControl.setAttribute("aria-expanded", String(!props.collapsed));
  }
}

watch(
  () => props.modelValue,
  (value) => {
    liveValue.value = value;
    void nextTick(syncAccessibility);
  }
);
watch(
  () => [
    props.layout,
    props.reverse,
    props.min,
    props.max,
    props.ariaLabel,
    props.collapsed,
    props.collapsedSize,
    props.collapsible,
    props.collapseLabel,
    props.expandLabel,
  ],
  () => void nextTick(() => {
    updateContainerSize();
    syncAccessibility();
  })
);

onMounted(() => void nextTick(() => {
  updateContainerSize();
  if (root.value && typeof ResizeObserver !== "undefined") {
    resizeObserver = new ResizeObserver(scheduleContainerSync);
    resizeObserver.observe(root.value);
  }
  syncAccessibility();
}));
onBeforeUnmount(() => {
  resizeObserver?.disconnect();
  if (resizeTimer !== undefined) window.clearTimeout(resizeTimer);
  dragger?.removeEventListener("keydown", handleKeydown);
  collapseControl?.removeEventListener("keydown", handleCollapseKeydown);
});
</script>

<template>
  <div
    ref="root"
    class="app-split-pane"
    :class="[
      `app-split-pane--${props.layout}`,
      `app-split-pane--${props.side}`,
      {
        'is-collapsed': props.collapsed,
        'is-collapsible': props.collapsible,
        'is-resize-disabled': !props.resizable,
      },
    ]"
  >
    <el-splitter
      ref="splitter"
      :layout="props.layout"
      :lazy="props.lazy"
      @resize-start="handleResizeStart"
      @resize="handleResize"
      @resize-end="handleResizeEnd"
      @collapse="handleCollapse"
    >
      <!-- Stable semantic keys move the native panels without destroying the
           message list/composer when their physical position changes. -->
      <template v-for="pane in (props.reverse ? ['second', 'first'] : ['first', 'second'])" :key="pane">
        <el-splitter-panel
          v-if="pane !== props.side || pane === 'first' || !props.collapsed"
          :key="pane === 'first' && pane === props.side
            ? props.refreshPanelOnCollapse
              ? props.collapsed ? 'first-panel-collapsed' : 'first-panel-expanded'
              : 'first-panel-stable'
            : pane"
          :size="pane === props.side ? panelSize : undefined"
          :min="pane === props.side ? panelMin : trailingMin"
          :max="pane === props.side ? panelMax : trailingMax"
          :resizable="props.resizable && !props.collapsed"
          :collapsible="pane === props.side ? props.collapsible : false"
        >
          <slot :name="pane" />
        </el-splitter-panel>
      </template>
    </el-splitter>
  </div>
</template>
