<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";

const props = withDefaults(
  defineProps<{
    sizes: number[];
    panelCount: number;
    layout?: "horizontal" | "vertical";
    min?: number;
    lazy?: boolean;
    ariaLabel?: string;
    keyboardStep?: number;
  }>(),
  {
    layout: "horizontal",
    min: 0,
    lazy: false,
    ariaLabel: "",
    keyboardStep: 0.05,
  }
);

const emit = defineEmits<{
  resizeStart: [sizes: number[]];
  resize: [sizes: number[]];
  resizeEnd: [sizes: number[]];
  "update:sizes": [sizes: number[]];
}>();

type SplitGroupDragger = HTMLElement & {
  __appSplitGroupKeydown?: (event: KeyboardEvent) => void;
};

const root = ref<HTMLElement>();
const containerSize = ref(0);
let resizeObserver: ResizeObserver | undefined;
let resizeTimer: number | undefined;
let draggers: SplitGroupDragger[] = [];

const normalizedSizes = computed(() => normalizeSizes(props.sizes));
const panelSizes = computed(() => normalizedSizes.value.map((size) => `${size * 100}%`));
const panelMin = computed(() => {
  if (containerSize.value <= 0) return 0;
  return Math.min(props.min, (containerSize.value / Math.max(1, props.panelCount)) * 0.7);
});
const orientation = computed(() => (props.layout === "horizontal" ? "vertical" : "horizontal"));

function normalizeSizes(sizes: number[]) {
  if (
    sizes.length !== props.panelCount ||
    sizes.some((size) => !Number.isFinite(size) || size <= 0)
  ) {
    return Array.from({ length: props.panelCount }, () => 1 / Math.max(1, props.panelCount));
  }
  const total = sizes.reduce((sum, size) => sum + size, 0);
  if (total <= 0) return Array.from({ length: props.panelCount }, () => 1 / Math.max(1, props.panelCount));
  return sizes.map((size) => size / total);
}

function sizesFromPixels(sizes: number[]) {
  return normalizeSizes(sizes);
}

function handleResizeStart(_index: number, sizes: number[]) {
  emit("resizeStart", sizesFromPixels(sizes));
}

function handleResize(_index: number, sizes: number[]) {
  const normalized = sizesFromPixels(sizes);
  emit("resize", normalized);
  syncAccessibility(normalized);
}

function handleResizeEnd(_index: number, sizes: number[]) {
  const normalized = sizesFromPixels(sizes);
  emit("update:sizes", normalized);
  emit("resizeEnd", normalized);
  void nextTick(() => syncAccessibility(normalized));
}

function minimumFraction(pairSize: number) {
  if (containerSize.value <= 0) return pairSize * 0.1;
  return Math.min(props.min / containerSize.value, pairSize * 0.35);
}

function resizeWithKeyboard(event: KeyboardEvent, dividerIndex: number) {
  const horizontal = props.layout === "horizontal";
  const decreaseKey = horizontal ? "ArrowLeft" : "ArrowUp";
  const increaseKey = horizontal ? "ArrowRight" : "ArrowDown";
  if (![decreaseKey, increaseKey, "Home", "End"].includes(event.key)) return;

  const sizes = [...normalizedSizes.value];
  const pairSize = (sizes[dividerIndex] ?? 0) + (sizes[dividerIndex + 1] ?? 0);
  if (pairSize <= 0) return;
  event.preventDefault();

  const minimum = minimumFraction(pairSize);
  let nextFirst = sizes[dividerIndex] ?? pairSize / 2;
  if (event.key === "Home") nextFirst = minimum;
  else if (event.key === "End") nextFirst = pairSize - minimum;
  else {
    const direction = event.key === increaseKey ? 1 : -1;
    const multiplier = event.shiftKey ? 4 : 1;
    nextFirst += pairSize * props.keyboardStep * direction * multiplier;
  }
  nextFirst = Math.max(minimum, Math.min(pairSize - minimum, nextFirst));
  sizes[dividerIndex] = nextFirst;
  sizes[dividerIndex + 1] = pairSize - nextFirst;
  emit("resize", sizes);
  emit("update:sizes", sizes);
  emit("resizeEnd", sizes);
  void nextTick(() => syncAccessibility(sizes));
}

function updateContainerSize() {
  const rect = root.value?.getBoundingClientRect();
  const nextSize = props.layout === "horizontal" ? (rect?.width ?? 0) : (rect?.height ?? 0);
  if (Math.abs(nextSize - containerSize.value) >= 0.5) containerSize.value = nextSize;
}

function scheduleContainerSync() {
  if (resizeTimer !== undefined) return;
  resizeTimer = window.setTimeout(() => {
    resizeTimer = undefined;
    updateContainerSize();
    void nextTick(syncAccessibility);
  }, 0);
}

function detachDraggerListeners() {
  cleanupDraggerListeners();
}

function syncAccessibility(sizes = normalizedSizes.value) {
  const nextDraggers = Array.from(
    root.value?.querySelectorAll<SplitGroupDragger>(".el-splitter-bar__dragger") ?? []
  );
  if (
    nextDraggers.length !== draggers.length ||
    nextDraggers.some((dragger, index) => dragger !== draggers[index])
  ) {
    detachDraggerListeners();
    draggers = nextDraggers;
    draggers.forEach((dragger, index) => {
      const listener = (event: KeyboardEvent) => resizeWithKeyboard(event, index);
      dragger.__appSplitGroupKeydown = listener;
      dragger.addEventListener("keydown", listener);
    });
  }
  draggers.forEach((dragger, index) => {
    const first = sizes[index] ?? 0;
    const second = sizes[index + 1] ?? 0;
    const pair = first + second;
    const value = pair > 0 ? Math.round((first / pair) * 100) : 50;
    dragger.tabIndex = 0;
    dragger.setAttribute("role", "separator");
    dragger.setAttribute("aria-orientation", orientation.value);
    dragger.setAttribute("aria-valuemin", "0");
    dragger.setAttribute("aria-valuemax", "100");
    dragger.setAttribute("aria-valuenow", String(value));
    if (props.ariaLabel) dragger.setAttribute("aria-label", props.ariaLabel);
    else dragger.removeAttribute("aria-label");
  });
}

function cleanupDraggerListeners() {
  draggers.forEach((dragger) => {
    const listener = dragger.__appSplitGroupKeydown;
    if (listener) dragger.removeEventListener("keydown", listener);
  });
  draggers = [];
}

watch(
  () => [props.layout, props.panelCount, props.sizes, props.ariaLabel],
  () => void nextTick(() => {
    updateContainerSize();
    syncAccessibility();
  }),
  { deep: true }
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
  cleanupDraggerListeners();
});
</script>

<template>
  <div
    ref="root"
    class="app-split-pane app-split-group"
    :class="`app-split-pane--${props.layout}`"
  >
    <el-splitter
      :layout="props.layout"
      :lazy="props.lazy"
      @resize-start="handleResizeStart"
      @resize="handleResize"
      @resize-end="handleResizeEnd"
    >
      <el-splitter-panel
        v-for="(_, index) in props.panelCount"
        :key="index"
        :size="panelSizes[index]"
        :min="panelMin"
      >
        <slot :index="index" />
      </el-splitter-panel>
    </el-splitter>
  </div>
</template>
