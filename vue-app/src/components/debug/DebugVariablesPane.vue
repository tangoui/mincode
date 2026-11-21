<script setup lang="ts">
/**
 * Watch expressions (first) and the selected frame's scopes as one lazy tree.
 * Watches are stored per project and evaluated again on every stop and
 * frame change; children load when a node is expanded. What the user
 * expanded is expanded again after a step (per session and method) and after
 * a value was changed, and the pane scrolls back to where the user left it
 * once those nodes are in. While the program runs the last tree stays in
 * place, greyed; the next stop replaces it.
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import type { ScrollbarInstance } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import { debugErrorText } from "@/domain/debug/errors";
import type { DebugSessionView } from "@/domain/debug/session-model";
import { siblingIndexes, type DebugVariableNode } from "@/domain/debug/variable-tree";
import { useDebugVariableExpansion } from "@/composables/use-debug-variable-expansion";
import { useDebugConfigsStore } from "@/stores/debug-configs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";
import DebugCompletionInput from "@/components/debug/DebugCompletionInput.vue";
import DebugVariableTree from "@/components/debug/DebugVariableTree.vue";

const props = defineProps<{ session: DebugSessionView }>();

const { t } = useVueI18n();
const configs = useDebugConfigsStore();
const sessions = useDebugSessionsStore();

const watches = computed(() => configs.stateFor(props.session.projectRoot).watches);
const draft = ref("");
const rootNodes = ref<DebugVariableNode[]>([]);
const restorePaths = ref<string[]>([]);
const loading = ref(false);
/** The tree shows the current stop (children can load); false while the program runs. */
const treeLive = ref(false);
/** The method the shown tree was built for (scroll positions are remembered per method). */
let treeFrame: string | null = null;
/** The watch list the shown tree was built with. */
let treeWatches = "";
let refreshToken = 0;
const scrollbar = ref<ScrollbarInstance>();
/** Where to scroll once the new tree's nodes are expanded again; null when nothing is pending. */
let pendingScroll: number | null = null;
/** A rebuild is scrolling back: scroll events (the browser clamping a shorter tree) are not the user's. */
let restoringScroll = false;
let scrollFrame = 0;
/** Expanding rows animate their height; keep scrolling back for about that long. */
const SCROLL_RESTORE_MS = 600;

const paused = computed(() => props.session.state === "paused" && props.session.selectedFrameId !== undefined);
/** The selected frame's method: expansion is remembered per method, so frames do not mix. */
const frameKey = computed(() => {
  if (!paused.value) return null;
  return props.session.frames.find((frame) => frame.id === props.session.selectedFrameId)?.name ?? null;
});
/** Values can be changed while paused when the adapter supports setVariable. */
const editable = computed(() => paused.value && props.session.capabilities.supportsSetVariable === true);
const expansion = useDebugVariableExpansion(
  () => props.session.id,
  () => frameKey.value
);

function watchPath(expression: string, index: number): string {
  return `watch:${index}:${expression}`;
}

async function evaluateWatch(expression: string, index: number): Promise<DebugVariableNode> {
  const path = watchPath(expression, index);
  const base = { key: path, path, name: expression, kind: "watch" as const, watchIndex: index, parentReference: 0 };
  try {
    const result = await sessions.evaluate(props.session.id, expression, "watch");
    return { ...base, value: result.result, type: result.type, reference: result.variablesReference, leaf: !(result.variablesReference > 0) };
  } catch (error) {
    return { ...base, value: debugErrorText(error), error: true, reference: 0, leaf: true };
  }
}

function pendingWatch(expression: string, index: number): DebugVariableNode {
  const path = watchPath(expression, index);
  return { key: path, path, name: expression, kind: "watch", watchIndex: index, value: t("debug.variables.watchNotPaused"), reference: 0, parentReference: 0, leaf: true, error: false };
}

function watchesKey(list: string[]): string {
  return list.join("\u0000");
}

function stopScrollRestore() {
  if (scrollFrame) cancelAnimationFrame(scrollFrame);
  scrollFrame = 0;
  restoringScroll = false;
}

/**
 * Replaces the tree. A tree of the current stop (`live`) scrolls back to the
 * method's remembered position once its nodes are expanded again (`restored`).
 */
function rebuild(nodes: DebugVariableNode[], live: boolean, restore: string[] = []) {
  stopScrollRestore();
  treeLive.value = live;
  treeFrame = live ? frameKey.value : null;
  treeWatches = watchesKey(watches.value);
  pendingScroll = live ? expansion.scrollTop() : null;
  restoringScroll = live;
  restorePaths.value = restore;
  rootNodes.value = nodes;
}

/** Scrolls to `target`, again on the next frames while expanding rows still grow the tree. */
function scrollBack(target: number) {
  const deadline = performance.now() + SCROLL_RESTORE_MS;
  const apply = () => {
    scrollFrame = 0;
    const bar = scrollbar.value;
    const wrap = bar?.wrapRef;
    if (!restoringScroll || !bar || !wrap) return;
    if (Math.abs(wrap.scrollTop - target) > 1) bar.setScrollTop(target);
    if (Math.abs(wrap.scrollTop - target) > 1 && performance.now() < deadline) scrollFrame = requestAnimationFrame(apply);
    else restoringScroll = false;
  };
  void nextTick(apply);
}

function onRestored() {
  if (pendingScroll === null) return;
  const target = pendingScroll;
  pendingScroll = null;
  scrollBack(target);
}

function onScroll({ scrollTop }: { scrollTop: number }) {
  if (restoringScroll || !treeLive.value || treeFrame === null || treeFrame !== frameKey.value) return;
  expansion.rememberScroll(scrollTop);
}

onBeforeUnmount(stopScrollRestore);

async function refresh() {
  const token = (refreshToken += 1);
  const list = [...watches.value];
  const frameId = props.session.selectedFrameId;
  if (!paused.value || frameId === undefined) {
    loading.value = false;
    // Running on: the last stop's tree stays (greyed) so nothing jumps; the
    // next stop replaces it. Without one, or once the session ended or the
    // watch list changed, show the watches waiting for a pause.
    if (rootNodes.value.length && props.session.state !== "ended" && treeWatches === watchesKey(list)) {
      stopScrollRestore();
      pendingScroll = null;
      treeLive.value = false;
      return;
    }
    rebuild(list.map(pendingWatch), false);
    return;
  }
  loading.value = true;
  const [watchNodes, scopes] = await Promise.all([
    Promise.all(list.map((expression, index) => evaluateWatch(expression, index))),
    sessions.loadScopes(props.session.id, frameId),
  ]);
  if (token !== refreshToken) return;
  const indexes = siblingIndexes(scopes.map((scope) => scope.name));
  const scopeNodes = scopes.map((scope, position): DebugVariableNode => {
    const path = indexes[position]! > 0 ? `scope:${scope.name}#${indexes[position]! + 1}` : `scope:${scope.name}`;
    return { key: path, path, name: scope.name, reference: scope.variablesReference, parentReference: 0, kind: "scope", leaf: !(scope.variablesReference > 0) };
  });
  // The first time a method is shown its first cheap scope opens; then what the user left.
  const firstScope = scopes.findIndex((scope) => !scope.expensive && scope.variablesReference > 0);
  const nodes = [...watchNodes, ...scopeNodes];
  loading.value = false;
  rebuild(nodes, true, expansion.pathsToRestore(nodes.map((node) => node.path), firstScope >= 0 ? [scopeNodes[firstScope]!.path] : []));
}

watch(
  () => [props.session.state, props.session.selectedFrameId, watchesKey(watches.value)] as const,
  () => void refresh(),
  { immediate: true }
);

// A changed variable (setVariable) can show in other rows too (a watch, a
// parent's summary): read everything again, keeping what is expanded.
watch(
  () => sessions.variablesVersion(props.session.id),
  () => void refresh()
);

function addWatch() {
  const expression = draft.value.trim();
  if (!expression) return;
  configs.setWatches(props.session.projectRoot, [...watches.value, expression]);
  draft.value = "";
}

function removeWatch(index: number) {
  configs.setWatches(
    props.session.projectRoot,
    watches.value.filter((_item, position) => position !== index)
  );
}

const emptyText = computed(() => (paused.value ? t("debug.variables.empty") : t("debug.variables.notPaused")));
</script>

<template>
  <section
    class="debug-variables"
    :aria-label="t('debug.variables.title')"
    @wheel.passive="stopScrollRestore"
    @pointerdown="stopScrollRestore"
    @keydown="stopScrollRestore"
  >
    <DebugCompletionInput
      v-model="draft"
      :session="session"
      class="debug-variables__watch-input"
      size="small"
      clearable
      :placeholder="t('debug.variables.addWatch')"
      :aria-label="t('debug.variables.addWatch')"
      @submit="addWatch"
    />
    <div v-if="loading && !rootNodes.length" class="debug-pane-empty">{{ t("debug.variables.loading") }}</div>
    <div v-else-if="!rootNodes.length" class="debug-pane-empty">{{ emptyText }}</div>
    <el-scrollbar v-else ref="scrollbar" class="debug-variables__tree" @scroll="onScroll">
      <DebugVariableTree
        :session-id="session.id"
        :nodes="rootNodes"
        :restore-paths="restorePaths"
        :live="treeLive && paused"
        :editable="editable && treeLive"
        :empty-text="emptyText"
        @remove-watch="removeWatch"
        @expand="(path: string) => expansion.markExpanded(path, treeFrame)"
        @collapse="(path: string) => expansion.markCollapsed(path, treeFrame)"
        @restored="onRestored"
      />
    </el-scrollbar>
  </section>
</template>
