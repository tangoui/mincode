<script setup lang="ts">
/**
 * Live results of a streaming test run in the Tests tab (lazy chunk of
 * ProjectPluginTestsPanel): summary bar (phase, counts, total time,
 * truncation), a windowed tree of the run's nodes (or of the discovered
 * tests before any run) with status icons and durations, a context menu to
 * run or debug a node, and the selected test's details. A large run updates
 * the tree at most every RENDER_INTERVAL_MS; the run's end shows at once.
 *
 * The tree renders only the rows in view (an el-scrollbar over the
 * flattened expanded rows, ARIA tree roles and arrow-key navigation) instead
 * of el-tree-v2: a run can hold 10000 nodes (too many for el-tree), and
 * el-tree-v2's virtual list reshuffles the shared chunks so the shell's
 * dialog/popover modules land in the entry script, past its 192 KiB budget.
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch, type Component } from "vue";
import type { ScrollbarInstance } from "element-plus";
import { CaretRight, CircleCheck, CircleClose, Close, Loading, Remove, RemoveFilled, WarningFilled } from "@element-plus/icons-vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import ProjectTestRunDetails from "@/components/projects/ProjectTestRunDetails.vue";
import type { TestItem } from "@/domain/plugins/test-providers";
import { findTestItem } from "@/domain/testing/test-discovery";
import { summarizeTestRun, testRunTreeRows, type TestNodeStatus, type TestRunState, type TestRunTreeRow } from "@/domain/testing/test-runs";
import { useVueI18n } from "@/i18n/messages";
import { formatTestDuration } from "@/domain/testing/test-run-format";

const props = defineProps<{
  /** The run shown (a snapshot), or undefined for the discovered tests. */
  run?: Readonly<TestRunState>;
  tests: readonly TestItem[];
  projectRoot: string;
  /** A run is active or launching: run actions are disabled. */
  busy: boolean;
  /** A discovery notice (dropped or truncated items). */
  notice?: string;
}>();
const emit = defineEmits<{
  run: [ids: string[], debug: boolean];
  "open-file": [path: string, line: number | undefined];
  "open-address": [address: string];
  dismiss: [];
}>();

const RENDER_INTERVAL_MS = 100;
const ROW_HEIGHT = 24;
/** Rows rendered above and below the viewport. */
const OVERSCAN = 8;
const FALLBACK_TREE_HEIGHT = 480;
const STATUS_ICONS: Record<Exclude<TestNodeStatus, "pending">, Component> = {
  running: Loading,
  passed: CircleCheck,
  failed: CircleClose,
  errored: WarningFilled,
  skipped: Remove,
  terminated: RemoveFilled,
};

const { t } = useVueI18n();

/** The run as rendered (throttled). */
const shown = shallowRef(props.run);
let renderTimer: ReturnType<typeof setTimeout> | undefined;

watch(
  () => props.run,
  (run, previous) => {
    if (!run || !previous || run.runId !== previous.runId || run.phase === "ended") {
      if (renderTimer !== undefined) clearTimeout(renderTimer);
      renderTimer = undefined;
      shown.value = run;
      return;
    }
    renderTimer ??= setTimeout(() => {
      renderTimer = undefined;
      shown.value = props.run;
    }, RENDER_INTERVAL_MS);
  },
);

interface Row {
  id: string;
  label: string;
  status?: TestNodeStatus;
  durationMs?: number;
  children?: Row[];
}

function discoveredRows(items: readonly TestItem[], parents: string[], depth = 0): Row[] {
  return items.map((item) => {
    const row: Row = { id: item.id, label: item.label };
    if (item.children?.length && depth < 256) {
      parents.push(item.id);
      row.children = discoveredRows(item.children, parents, depth + 1);
    }
    return row;
  });
}

const tree = computed<{ rows: readonly (Row | TestRunTreeRow)[]; parentIds: string[] }>(() => {
  const run = shown.value;
  if (run) return testRunTreeRows(run);
  const parentIds: string[] = [];
  return { rows: discoveredRows(props.tests, parentIds), parentIds };
});

const treeHost = ref<HTMLElement>();
const treeHeight = ref(FALLBACK_TREE_HEIGHT);

/** Rows the user collapsed (everything else is expanded, also nodes added later). */
const collapsed = ref(new Set<string>());

interface FlatRow {
  row: Row;
  depth: number;
  parentId?: string;
  expanded: boolean;
  /** Position among its siblings (1-based) and their count (aria-posinset / aria-setsize). */
  posinset: number;
  setsize: number;
}

/** The expanded rows in display order. */
const flatRows = computed<FlatRow[]>(() => {
  const out: FlatRow[] = [];
  const closed = collapsed.value;
  const visit = (rows: readonly Row[], depth: number, parentId: string | undefined) => {
    for (const [index, row] of rows.entries()) {
      const expanded = Boolean(row.children?.length) && !closed.has(row.id);
      out.push({ row, depth, parentId, expanded, posinset: index + 1, setsize: rows.length });
      if (expanded) visit(row.children!, depth + 1, row.id);
    }
  };
  visit(tree.value.rows, 0, undefined);
  return out;
});

function setCollapsed(id: string, value: boolean) {
  const next = new Set(collapsed.value);
  if (value) next.add(id);
  else next.delete(id);
  collapsed.value = next;
}

const scrollTop = ref(0);
const visibleRows = computed(() => {
  const rows = flatRows.value;
  const first = Math.max(0, Math.floor(scrollTop.value / ROW_HEIGHT) - OVERSCAN);
  const last = Math.min(rows.length, Math.ceil((scrollTop.value + treeHeight.value) / ROW_HEIGHT) + OVERSCAN);
  return rows.slice(first, last).map((item, offset) => ({ ...item, index: first + offset }));
});

/**
 * The one row in the tab order (roving tabindex): the selected row while it
 * is rendered, else the first rendered row, so the tree stays reachable
 * after the selection scrolled out of view.
 */
const tabStopId = computed(() => {
  const rows = visibleRows.value;
  return rows.find((item) => item.row.id === selectedId.value)?.row.id ?? rows[0]?.row.id;
});

const summary = computed(() => (shown.value ? summarizeTestRun(shown.value) : undefined));

const phase = computed<"preparing" | "running" | "stopping" | "finished" | "stopped" | "terminated" | undefined>(() => {
  const run = shown.value;
  if (!run) return undefined;
  if (run.phase === "preparing") return "preparing";
  if (run.phase === "running") return run.stopping ? "stopping" : "running";
  if (run.finished) return "finished";
  return run.endReason === "stopped" || run.endReason === "cancelled" || run.endReason === "superseded" ? "stopped" : "terminated";
});

const totalTime = computed(() => {
  const run = shown.value;
  if (!run || run.phase !== "ended") return "";
  const ms = run.durationMs ?? (run.endedAt !== undefined ? run.endedAt - run.startedAt : undefined);
  return ms === undefined ? "" : formatTestDuration(t, ms);
});

const selectedId = ref<string>();
const menu = ref<{ x: number; y: number; id: string; label: string } | null>(null);

function nodeSource(id: string): { path: string; line?: number } | undefined {
  const node = shown.value?.nodes[id];
  if (node?.path) return { path: node.path, line: node.line };
  const item = findTestItem(props.tests, id);
  return item?.path ? { path: item.path, line: item.line } : undefined;
}

function openSource(id: string) {
  const source = nodeSource(id);
  if (source) emit("open-file", source.path, source.line);
}

function rowTitle(row: Row) {
  const parts = [row.label];
  if (row.status) parts.push(t(`pluginContrib.testProviders.status.${row.status}`));
  if (row.durationMs !== undefined) parts.push(formatTestDuration(t, row.durationMs));
  return parts.join(" · ");
}

function statusLabel(status: TestNodeStatus) {
  return t(`pluginContrib.testProviders.status.${status}`);
}

function onNodeClick(row: Row) {
  selectedId.value = row.id;
}

const scrollbar = ref<ScrollbarInstance>();

/** Selects the row at index (clamped), scrolls it into view and focuses it once it is rendered. */
async function selectIndex(index: number) {
  const rows = flatRows.value;
  if (!rows.length) return;
  const target = Math.min(rows.length - 1, Math.max(0, index));
  selectedId.value = rows[target]!.row.id;
  const top = target * ROW_HEIGHT;
  let nextTop = scrollTop.value;
  if (top < nextTop) nextTop = top;
  else if (top + ROW_HEIGHT > nextTop + treeHeight.value) nextTop = top + ROW_HEIGHT - treeHeight.value;
  if (nextTop !== scrollTop.value) {
    // Apply the position here (the scroll event comes later), so the row renders before it is focused.
    scrollTop.value = nextTop;
    scrollbar.value?.setScrollTop(nextTop);
  }
  await nextTick();
  treeHost.value?.querySelector<HTMLElement>(`[data-row-index="${target}"]`)?.focus();
}

const pageRows = () => Math.max(1, Math.floor(treeHeight.value / ROW_HEIGHT) - 1);

function onTreeKeydown(event: KeyboardEvent, item: FlatRow & { index: number }) {
  const { row } = item;
  switch (event.key) {
    case "ArrowDown":
      void selectIndex(item.index + 1);
      break;
    case "ArrowUp":
      void selectIndex(item.index - 1);
      break;
    case "ArrowRight":
      if (row.children?.length && !item.expanded) setCollapsed(row.id, false);
      else if (item.expanded) void selectIndex(item.index + 1);
      break;
    case "ArrowLeft":
      if (item.expanded) setCollapsed(row.id, true);
      else if (item.parentId) void selectIndex(flatRows.value.findIndex((candidate) => candidate.row.id === item.parentId));
      break;
    case "Home":
      void selectIndex(0);
      break;
    case "End":
      void selectIndex(flatRows.value.length - 1);
      break;
    case "PageDown":
      void selectIndex(item.index + pageRows());
      break;
    case "PageUp":
      void selectIndex(item.index - pageRows());
      break;
    case "Enter":
      openSource(row.id);
      break;
    case "ContextMenu": {
      const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
      selectedId.value = row.id;
      menu.value = { x: rect.left + 24, y: rect.bottom, id: row.id, label: row.label };
      break;
    }
    default:
      return;
  }
  event.preventDefault();
}

// ---- context menu ----

function onNodeContextMenu(event: MouseEvent, row: Row) {
  event.preventDefault();
  const mouse = event;
  selectedId.value = row.id;
  menu.value = { x: mouse.clientX, y: mouse.clientY, id: row.id, label: row.label };
}

function menuRun(debug: boolean) {
  const current = menu.value;
  menu.value = null;
  if (current && !props.busy) emit("run", [current.id], debug);
}

function menuOpen() {
  const current = menu.value;
  menu.value = null;
  if (current) openSource(current.id);
}

// ---- tree height ----

let resizeObserver: ResizeObserver | undefined;

onMounted(() => {
  if (!treeHost.value || typeof ResizeObserver === "undefined") return;
  resizeObserver = new ResizeObserver(([entry]) => {
    const height = Math.floor(entry?.contentRect.height ?? 0);
    if (height > 0) treeHeight.value = height;
  });
  resizeObserver.observe(treeHost.value);
});

onBeforeUnmount(() => {
  if (renderTimer !== undefined) clearTimeout(renderTimer);
  resizeObserver?.disconnect();
});
</script>

<template>
  <section class="project-test-run-results">
    <div v-if="shown && summary" class="project-test-run-results__summary" aria-live="polite" data-test="summary">
      <span v-if="phase" class="project-test-run-results__phase" :class="`is-${phase}`" data-test="phase">
        <el-icon v-if="phase === 'preparing' || phase === 'running' || phase === 'stopping'" class="is-loading" aria-hidden="true"><Loading /></el-icon>
        {{ t(`pluginContrib.testProviders.phase.${phase}`) }}
      </span>
      <span class="is-passed">{{ t("pluginContrib.testProviders.summary.passed", { count: summary.passed }) }}</span>
      <span class="is-failed">{{ t("pluginContrib.testProviders.summary.failed", { count: summary.failed }) }}</span>
      <span v-if="summary.errored" class="is-failed">{{ t("pluginContrib.testProviders.summary.errored", { count: summary.errored }) }}</span>
      <span class="is-muted">{{ t("pluginContrib.testProviders.summary.skipped", { count: summary.skipped }) }}</span>
      <span v-if="summary.terminated" class="is-muted">{{ t("pluginContrib.testProviders.summary.terminated", { count: summary.terminated }) }}</span>
      <span v-if="totalTime" class="is-muted" data-test="total-time">{{ t("pluginContrib.testProviders.summary.duration", { duration: totalTime }) }}</span>
      <el-tooltip v-if="shown.phase === 'ended'" :content="t('pluginContrib.testProviders.dismissResults')" placement="bottom">
        <el-button
          class="project-test-run-results__dismiss"
          text
          size="small"
          native-type="button"
          data-action="dismiss"
          :aria-label="t('pluginContrib.testProviders.dismissResults')"
          @click="emit('dismiss')"
        >
          <el-icon><Close /></el-icon>
        </el-button>
      </el-tooltip>
    </div>
    <p v-if="shown?.truncated" class="project-test-run-results__notice" role="status" data-test="truncated">
      {{ t("pluginContrib.testProviders.runTruncated") }}
    </p>
    <p v-if="!shown && notice" class="project-test-run-results__notice">{{ notice }}</p>

    <div ref="treeHost" class="project-test-run-results__tree">
      <el-scrollbar ref="scrollbar" :height="treeHeight" @scroll="({ scrollTop: top }: { scrollTop: number }) => (scrollTop = top)">
        <div
          class="project-test-run-results__list"
          role="tree"
          :aria-label="t('pluginContrib.testProviders.resultsTree')"
          :style="{ height: `${flatRows.length * ROW_HEIGHT}px` }"
        >
          <div
            v-for="item in visibleRows"
            :key="item.row.id"
            class="project-test-run-results__item"
            :class="{ 'is-current': item.row.id === selectedId }"
            role="treeitem"
            :tabindex="item.row.id === tabStopId ? 0 : -1"
            :aria-level="item.depth + 1"
            :aria-posinset="item.posinset"
            :aria-setsize="item.setsize"
            :aria-expanded="item.row.children?.length ? item.expanded : undefined"
            :aria-selected="item.row.id === selectedId"
            :data-row-index="item.index"
            :style="{ top: `${item.index * ROW_HEIGHT}px`, paddingLeft: `${4 + item.depth * 12}px` }"
            @click="onNodeClick(item.row)"
            @dblclick="openSource(item.row.id)"
            @contextmenu="onNodeContextMenu($event, item.row)"
            @keydown="onTreeKeydown($event, item)"
          >
            <el-icon
              v-if="item.row.children?.length"
              class="project-test-run-results__caret"
              :class="{ 'is-expanded': item.expanded }"
              aria-hidden="true"
              @click.stop="setCollapsed(item.row.id, item.expanded)"
            >
              <CaretRight />
            </el-icon>
            <span v-else class="project-test-run-results__caret" aria-hidden="true" />
            <span class="project-test-run-results__row" :title="rowTitle(item.row)" :data-id="item.row.id">
              <el-icon
                v-if="item.row.status && item.row.status !== 'pending'"
                class="project-test-run-results__status"
                :class="[`is-${item.row.status}`, { 'is-loading': item.row.status === 'running' }]"
                role="img"
                :aria-label="statusLabel(item.row.status)"
              >
                <component :is="STATUS_ICONS[item.row.status as Exclude<TestNodeStatus, 'pending'>]" />
              </el-icon>
              <span v-else class="project-test-run-results__status is-pending" aria-hidden="true" />
              <span class="project-test-run-results__label" dir="auto">{{ item.row.label }}</span>
              <span v-if="item.row.durationMs !== undefined" class="project-test-run-results__duration">{{ formatTestDuration(t, item.row.durationMs) }}</span>
            </span>
          </div>
        </div>
      </el-scrollbar>
    </div>

    <ProjectTestRunDetails
      v-if="shown && selectedId && shown.nodes[selectedId]"
      class="project-test-run-results__details"
      :run="shown"
      :node-id="selectedId"
      :tests="tests"
      :project-root="projectRoot"
      @open-file="(path: string, line: number | undefined) => emit('open-file', path, line)"
      @open-address="(address: string) => emit('open-address', address)"
    />
    <details v-else-if="shown?.output" class="project-test-run-results__output">
      <summary>
        {{ t("pluginContrib.testProviders.output") }}
        <small v-if="shown.outputTruncated">{{ t("pluginContrib.testProviders.outputTruncated") }}</small>
      </summary>
      <pre dir="auto">{{ shown.output }}</pre>
    </details>

    <AppContextMenu
      v-if="menu"
      :open="true"
      :x="menu.x"
      :y="menu.y"
      :width="240"
      :aria-label="t('pluginContrib.testProviders.actions')"
      @close="menu = null"
    >
      <el-dropdown-item data-action="menu-run" :disabled="busy" @click="menuRun(false)">
        {{ t("pluginContrib.testProviders.runOne", { name: menu.label }) }}
      </el-dropdown-item>
      <el-dropdown-item data-action="menu-debug" :disabled="busy" @click="menuRun(true)">
        {{ t("pluginContrib.testProviders.debugOne", { name: menu.label }) }}
      </el-dropdown-item>
      <el-dropdown-item v-if="nodeSource(menu.id)" divided data-action="menu-open" @click="menuOpen">
        {{ t("pluginContrib.testProviders.goToSource") }}
      </el-dropdown-item>
    </AppContextMenu>
  </section>
</template>

<style scoped>
.project-test-run-results {
  display: flex;
  min-height: 0;
  flex-direction: column;
}

.project-test-run-results__summary {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 10px;
  padding: 4px 8px;
  border-bottom: 1px solid var(--app-border);
  font-size: 11px;
}

.project-test-run-results__phase {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--app-text);
  font-weight: 600;
}

.project-test-run-results__phase.is-terminated {
  color: var(--el-color-danger);
}

.project-test-run-results__summary .is-passed {
  color: var(--el-color-success);
}

.project-test-run-results__summary .is-failed {
  color: var(--el-color-danger);
}

.project-test-run-results__summary .is-muted {
  color: var(--app-text-muted);
}

.project-test-run-results__dismiss {
  height: 18px;
  margin-left: auto;
  padding: 0 2px;
}

.project-test-run-results__notice {
  margin: 4px 8px;
  color: var(--app-text-muted);
  font-size: 11px;
}

.project-test-run-results__tree {
  min-height: 72px;
  flex: 1;
  overflow: hidden;
}

.project-test-run-results__list {
  position: relative;
}

.project-test-run-results__item {
  position: absolute;
  right: 0;
  left: 0;
  display: flex;
  height: 24px;
  box-sizing: border-box;
  align-items: center;
  cursor: default;
}

.project-test-run-results__item:hover {
  background: var(--el-fill-color-light);
}

.project-test-run-results__item.is-current {
  background: var(--el-color-primary-light-9);
}

.project-test-run-results__item:focus-visible {
  outline: 1px solid var(--app-accent);
  outline-offset: -1px;
}

.project-test-run-results__caret {
  display: inline-flex;
  width: 16px;
  flex: none;
  justify-content: center;
  color: var(--app-text-muted);
  font-size: 12px;
  cursor: pointer;
  transition: transform 0.15s;
}

.project-test-run-results__caret.is-expanded {
  transform: rotate(90deg);
}

.project-test-run-results__row {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  gap: 6px;
  padding-right: 8px;
  font-size: 12px;
}

.project-test-run-results__status {
  display: inline-flex;
  width: 14px;
  height: 14px;
  flex: none;
  color: var(--app-text-muted);
  font-size: 14px;
}

.project-test-run-results__status.is-pending::before {
  width: 6px;
  height: 6px;
  margin: auto;
  border-radius: 50%;
  background: var(--app-border);
  content: "";
}

.project-test-run-results__status.is-running {
  color: var(--app-accent);
}

.project-test-run-results__status.is-passed {
  color: var(--el-color-success);
}

.project-test-run-results__status.is-failed,
.project-test-run-results__status.is-errored {
  color: var(--el-color-danger);
}

.project-test-run-results__label {
  overflow: hidden;
  min-width: 0;
  flex: 1;
  color: var(--app-text);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-test-run-results__duration {
  flex: none;
  color: var(--app-text-muted);
  font-size: 11px;
}

.project-test-run-results__details,
.project-test-run-results__output {
  max-height: 45%;
  overflow: auto;
  border-top: 1px solid var(--app-border);
}

.project-test-run-results__output {
  font-size: 11px;
}

.project-test-run-results__output summary {
  padding: 4px 8px;
  color: var(--app-text);
  cursor: pointer;
}

.project-test-run-results__output small {
  margin-left: 6px;
  color: var(--app-text-muted);
}

.project-test-run-results__output pre {
  margin: 0;
  padding: 4px 8px 8px;
  color: var(--app-text);
  font-family: var(--app-font-mono, monospace);
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
