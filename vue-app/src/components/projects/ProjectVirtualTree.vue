<script setup lang="ts" generic="T extends object">
// Virtualized, lazily loaded tree for the project explorer. It keeps el-tree's lazy-load
// contract (load(node, resolve), level-0 synthetic root, node.expand/collapse/insertChild/remove)
// but renders only the rows in view, so expanding folders with thousands of entries stays smooth.
// Expanded ancestors pin to the top while their subtree scrolls (sticky folders).
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import type { ScrollbarInstance } from "element-plus";
import { CaretRight, Loading } from "@element-plus/icons-vue";
import {
  LazyTree,
  stickyRows,
  visibleRows,
  visibleWindow,
  type LazyTreeNode,
  type LoadFunction,
} from "@/domain/projects/lazy-tree";

const props = withDefaults(
  defineProps<{
    load: LoadFunction<T>;
    nodeKey?: string;
    leafKey?: string;
    defaultExpandedKeys?: string[];
    expandOnClickNode?: boolean;
    rowHeight?: number;
    indent?: number;
    stickyLevels?: number;
    overscan?: number;
  }>(),
  {
    nodeKey: "id",
    leafKey: "leaf",
    defaultExpandedKeys: () => [],
    expandOnClickNode: true,
    rowHeight: 28,
    indent: 18,
    stickyLevels: 5,
    overscan: 8,
  },
);

const emit = defineEmits<{
  "node-click": [data: T, node: LazyTreeNode<T>, event: MouseEvent | KeyboardEvent];
  "node-expand": [data: T, node: LazyTreeNode<T>];
  "node-collapse": [data: T, node: LazyTreeNode<T>];
  "node-contextmenu": [event: MouseEvent, data: T, node: LazyTreeNode<T>];
}>();

defineSlots<{ default(props: { node: LazyTreeNode<T>; data: T }): unknown }>();

/** Top padding of the row list inside the scroller; row offsets are measured from it. */
const LIST_TOP = 6;

// reactive() so in-place edits (refresh, insert, remove) re-render; the model holds no refs,
// so its unwrapped type is the class itself.
const field = (data: T, name: string) => (data as Record<string, unknown>)[name];
const tree = reactive(
  new LazyTree<T>({
    key: (data) => String(field(data, props.nodeKey)),
    isLeaf: (data) => Boolean(field(data, props.leafKey)),
    load: (node, resolve) => props.load(node, resolve),
    defaultExpandedKeys: props.defaultExpandedKeys,
  }),
) as unknown as LazyTree<T>;

// The tree scrolls inside an el-scrollbar (thin app scrollbar); its wrap is the scroll layer
// and also the focus target for arrow keys.
const scrollbar = ref<ScrollbarInstance>();
const scroller = computed<HTMLElement | undefined>(() => scrollbar.value?.wrapRef);
const scrollTop = ref(0);
const viewportHeight = ref(0);
const currentKey = ref<string | null>(null);

const rows = computed(() => visibleRows(tree.root));
const listTop = computed(() => scrollTop.value - LIST_TOP);
const range = computed(() =>
  visibleWindow({
    count: rows.value.length,
    rowHeight: props.rowHeight,
    scrollTop: Math.max(0, listTop.value),
    viewportHeight: viewportHeight.value,
    overscan: props.overscan,
  }),
);
const windowRows = computed(() =>
  rows.value.slice(range.value.start, range.value.end).map((node, offset) => ({ node, index: range.value.start + offset })),
);
const pinned = computed(() =>
  stickyRows(rows.value, { rowHeight: props.rowHeight, scrollTop: listTop.value, maxLevels: props.stickyLevels }),
);

function toggle(node: LazyTreeNode<T>) {
  if (node.isLeaf) return;
  if (node.expanded) {
    emit("node-collapse", node.data, node);
    node.collapse();
  } else {
    node.expand();
    emit("node-expand", node.data, node);
  }
}

function onRowClick(node: LazyTreeNode<T>, event: MouseEvent | KeyboardEvent) {
  currentKey.value = node.key;
  if (props.expandOnClickNode) toggle(node);
  emit("node-click", node.data, node, event);
}

function onScroll() {
  const element = scroller.value;
  if (!element) return;
  scrollTop.value = element.scrollTop;
  viewportHeight.value = element.clientHeight;
}

function scrollToNode(key: string, align: "center" | "nearest" = "center") {
  const element = scroller.value;
  const index = rows.value.findIndex((node) => node.key === key);
  if (!element || index < 0) return;
  const rowTop = LIST_TOP + index * props.rowHeight;
  const height = element.clientHeight;
  let next: number | undefined;
  if (align === "center") {
    next = Math.max(0, rowTop - (height - props.rowHeight) / 2);
  } else {
    // Rows under the pinned folders are hidden, so "nearest" keeps clear of them.
    const covered = pinned.value.length * props.rowHeight;
    if (rowTop - covered < element.scrollTop) next = Math.max(0, rowTop - covered);
    else if (rowTop + props.rowHeight > element.scrollTop + height) next = rowTop + props.rowHeight - height;
  }
  // setScrollTop also moves el-scrollbar's thumb.
  if (next !== undefined) scrollbar.value?.setScrollTop(next);
  onScroll();
}

function onKeydown(event: KeyboardEvent) {
  // Keys typed into an inline editor (rename) belong to it.
  if (event.target !== scroller.value) return;
  const list = rows.value;
  if (!list.length) return;
  const at = list.findIndex((node) => node.key === currentKey.value);
  const current = at >= 0 ? list[at] : undefined;
  let next: LazyTreeNode<T> | undefined;
  switch (event.key) {
    case "ArrowDown":
      next = list[Math.min(list.length - 1, at + 1)];
      break;
    case "ArrowUp":
      next = list[Math.max(0, at - 1)];
      break;
    case "ArrowRight":
      if (!current || current.isLeaf) return;
      if (!current.expanded) toggle(current);
      else next = current.childNodes[0];
      break;
    case "ArrowLeft":
      if (!current) return;
      if (current.expanded && !current.isLeaf) toggle(current);
      else if (current.parent && current.parent.level > 0) next = current.parent;
      break;
    case "Enter":
    case " ":
      if (!current) return;
      onRowClick(current, event);
      break;
    default:
      return;
  }
  event.preventDefault();
  if (next) {
    currentKey.value = next.key;
    scrollToNode(next.key, "nearest");
  }
}

let resizeObserver: ResizeObserver | undefined;
onMounted(() => {
  const element = scroller.value;
  if (element) {
    onScroll();
    // A sidebar that mounts hidden measures 0 until it is shown.
    if (typeof ResizeObserver !== "undefined") {
      resizeObserver = new ResizeObserver(onScroll);
      resizeObserver.observe(element);
    }
  }
  tree.start();
});
onBeforeUnmount(() => resizeObserver?.disconnect());

watch(
  () => props.defaultExpandedKeys,
  (keys) => tree.setDefaultExpandedKeys(keys),
);

defineExpose({
  /** The synthetic level-0 node; its childNodes are the top-level entries (as in el-tree). */
  root: tree.root,
  getNode: (key: string) => tree.getNode(key),
  remove: (target: LazyTreeNode<T> | T) => tree.remove(target),
  setCurrentKey: (key: string | null) => {
    currentKey.value = key;
  },
  getCurrentKey: () => currentKey.value,
  scrollToNode,
});
</script>

<template>
  <el-scrollbar
    ref="scrollbar"
    class="project-virtual-tree"
    view-class="project-virtual-tree__body"
    role="tree"
    :tabindex="0"
    @scroll="onScroll"
    @keydown="onKeydown"
  >
    <div class="project-virtual-tree__sticky">
      <div
        v-for="row in pinned"
        :key="row.node.key"
        class="project-virtual-tree__row is-pinned is-expanded"
        :class="{ 'is-current': row.node.key === currentKey }"
        :style="{ top: `${row.top}px`, height: `${rowHeight}px`, zIndex: stickyLevels + 1 - row.node.level }"
        :data-key="row.node.key"
        @click="onRowClick(row.node, $event)"
        @contextmenu="emit('node-contextmenu', $event, row.node.data, row.node)"
      >
        <span class="project-virtual-tree__content" :style="{ paddingLeft: `${(row.node.level - 1) * indent}px` }">
          <el-icon class="project-virtual-tree__caret is-expanded" @click.stop="toggle(row.node)"><CaretRight /></el-icon>
          <slot :node="row.node" :data="row.node.data" />
        </span>
      </div>
    </div>
    <div class="project-virtual-tree__list" :style="{ height: `${rows.length * rowHeight}px`, marginTop: `${LIST_TOP}px` }">
      <div
        v-for="row in windowRows"
        :key="row.node.key"
        class="project-virtual-tree__row"
        :class="{ 'is-current': row.node.key === currentKey, 'is-expanded': row.node.expanded }"
        role="treeitem"
        :aria-level="row.node.level"
        :aria-expanded="row.node.isLeaf ? undefined : row.node.expanded"
        :aria-selected="row.node.key === currentKey"
        :data-key="row.node.key"
        :style="{ top: `${row.index * rowHeight}px`, height: `${rowHeight - 1}px` }"
        @click="onRowClick(row.node, $event)"
        @contextmenu="emit('node-contextmenu', $event, row.node.data, row.node)"
      >
        <span class="project-virtual-tree__content" :style="{ paddingLeft: `${(row.node.level - 1) * indent}px` }">
          <el-icon v-if="row.node.loading" class="project-virtual-tree__caret is-loading"><Loading /></el-icon>
          <el-icon
            v-else
            class="project-virtual-tree__caret"
            :class="{ 'is-leaf': row.node.isLeaf, 'is-expanded': row.node.expanded }"
            @click.stop="toggle(row.node)"
          ><CaretRight /></el-icon>
          <slot :node="row.node" :data="row.node.data" />
        </span>
      </div>
    </div>
  </el-scrollbar>
</template>
