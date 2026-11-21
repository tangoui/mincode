<script setup lang="ts">
// Read-only text that may be several MB: hundreds of thousands of lines, or one multi-MB
// minified line (HTTP response bodies). A row index (domain/ui/text-lines) replaces splitting
// the text, and only the rows near the viewport are in the DOM, so a large body costs a few
// typed arrays rather than one string or element per line. Long lines wrap at the viewer's
// width into fixed-height rows, which keeps every scroll position exact.
//
// Mouse selection works across the rendered rows and copies the matching source text;
// ⌘/Ctrl+A selects the whole text, so a following copy copies all of it, not the rows in view.
// ⌘/Ctrl+F opens a find bar that searches the whole text (the browser's find only sees the
// rendered rows) and scrolls each match into view.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import { ArrowDown, ArrowUp, Close } from "@element-plus/icons-vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import { useVueI18n } from "@/i18n/messages";
import { findMatches, indexRows, MAX_MATCHES, rowCount, rowOfOffset, scrollWindow } from "@/domain/ui/text-lines";

const props = defineProps<{
  text: string;
  /** Accessible name of the viewer. */
  label: string;
}>();

const { t } = useVueI18n();

/** Matches `.app-virtual-text__row` in styles/shared/virtual-text.css. */
const ROW_HEIGHT = 19;
const OVERSCAN = 20;

const viewport = ref<HTMLElement>();
const scrollTop = ref(0);
const viewportHeight = ref(0);
/** Characters per row: measured from the viewport and a monospace character width. */
const columns = ref(0);
const charWidth = ref(0);

const index = shallowRef(indexRows(props.text, columns.value || 1_000_000));
function rebuild() {
  index.value = indexRows(props.text, columns.value || 1_000_000);
}
watch(() => props.text, () => {
  rebuild();
  allSelected.value = false;
  if (viewport.value) viewport.value.scrollTop = 0;
  scrollTop.value = 0;
  runSearch();
});
watch(columns, rebuild);

const count = computed(() => rowCount(index.value));
const lastLine = computed(() => (count.value ? index.value.lines[count.value - 1]! + 1 : 1));
const gutterDigits = computed(() => String(lastLine.value).length);

const window = computed(() =>
  scrollWindow({
    count: count.value,
    rowHeight: ROW_HEIGHT,
    scrollTop: scrollTop.value,
    viewportHeight: viewportHeight.value,
    overscan: OVERSCAN,
  }),
);
interface Segment { text: string; match: "none" | "match" | "current" }
const rows = computed(() => {
  const { start, end } = window.value;
  const rowIndex = index.value;
  const text = props.text;
  const out: { row: number; number: number | null; segments: Segment[] }[] = [];
  for (let row = start; row < end; row++) {
    out.push({ row, number: rowIndex.continues[row] ? null : rowIndex.lines[row]! + 1, segments: rowSegments(text, rowIndex.starts[row]!, rowIndex.ends[row]!) });
  }
  return out;
});

// ---- Find ----
const searchOpen = ref(false);
const query = ref("");
const matches = shallowRef<number[]>([]);
const current = ref(-1);
const searchField = ref<InstanceType<typeof AppSearchField>>();
const matchLength = computed(() => query.value.length);
const searchStatus = computed(() => {
  if (!query.value) return "";
  if (!matches.value.length) return t("textSearch.noResults");
  const params = { current: current.value + 1, total: matches.value.length };
  return matches.value.length >= MAX_MATCHES ? t("textSearch.countCapped", params) : t("textSearch.count", params);
});

/** First match index at or after `offset`, as a binary search over the sorted matches. */
function firstMatchFrom(offset: number) {
  const list = matches.value;
  let low = 0;
  let high = list.length;
  while (low < high) {
    const middle = (low + high) >> 1;
    if (list[middle]! < offset) low = middle + 1;
    else high = middle;
  }
  return low;
}

/** A row's text split into plain and highlighted parts (matches may cross row boundaries). */
function rowSegments(text: string, start: number, end: number): Segment[] {
  const length = matchLength.value;
  if (!length || !matches.value.length) return [{ text: text.slice(start, end), match: "none" }];
  const list = matches.value;
  const segments: Segment[] = [];
  let at = start;
  for (let i = firstMatchFrom(Math.max(0, start - length + 1)); i < list.length && list[i]! < end; i++) {
    const from = Math.max(list[i]!, start);
    const to = Math.min(list[i]! + length, end);
    if (to <= from) continue;
    if (from > at) segments.push({ text: text.slice(at, from), match: "none" });
    segments.push({ text: text.slice(from, to), match: i === current.value ? "current" : "match" });
    at = to;
  }
  if (at < end || !segments.length) segments.push({ text: text.slice(at, end), match: "none" });
  return segments;
}

function revealMatch(i: number) {
  const element = viewport.value;
  if (i < 0 || !element) return;
  const row = rowOfOffset(index.value, matches.value[i]!);
  viewportHeight.value = element.clientHeight || viewportHeight.value;
  const { height } = window.value;
  const full = count.value * ROW_HEIGHT;
  // Read the live height: opening the find bar shrinks the viewport before the observer fires.
  const view = element.clientHeight || viewportHeight.value;
  // Center the row; with scaled content, map the unscaled position into the scroll range.
  // The viewport has no vertical padding, so its clientHeight is the visible content height.
  const target = row * ROW_HEIGHT - view / 2 + ROW_HEIGHT / 2;
  const range = Math.max(0, height - view);
  const top = full > height && full > view ? (target * range) / (full - view) : target;
  element.scrollTop = Math.max(0, Math.min(range, top));
  scrollTop.value = element.scrollTop;
}

let searchTimer: ReturnType<typeof setTimeout> | undefined;
/** A typed query not searched yet (the debounce has not fired). */
let searchPending = false;
function runSearch() {
  clearTimeout(searchTimer);
  searchPending = false;
  if (!searchOpen.value || !query.value) {
    matches.value = [];
    current.value = -1;
    return;
  }
  matches.value = findMatches(props.text, query.value);
  // Start at the first match from the top of the view, so a search does not jump far away.
  const topRow = Math.min(count.value - 1, Math.max(0, Math.floor(scrollTop.value / ROW_HEIGHT)));
  const fromView = count.value ? firstMatchFrom(index.value.starts[topRow] ?? 0) : 0;
  current.value = matches.value.length ? (fromView < matches.value.length ? fromView : 0) : -1;
  revealMatch(current.value);
}
// A multi-MB body takes tens of ms to search: wait until typing pauses.
function onQueryInput() {
  clearTimeout(searchTimer);
  searchPending = true;
  searchTimer = setTimeout(runSearch, props.text.length > 1_000_000 ? 200 : 60);
}

function step(direction: 1 | -1) {
  const total = matches.value.length;
  if (!total) return;
  current.value = (current.value + direction + total) % total;
  revealMatch(current.value);
}

async function openSearch() {
  searchOpen.value = true;
  await nextTick();
  measure();
  // A selection on one row seeds the query, like an editor's find.
  const selected = document.getSelection()?.toString() ?? "";
  if (selected && !selected.includes("\n") && selected.length <= 200) query.value = selected;
  await nextTick();
  searchField.value?.focus();
  runSearch();
}

function closeSearch() {
  searchOpen.value = false;
  runSearch();
  void nextTick(measure);
  viewport.value?.focus();
}

function onSearchKeydown(event: KeyboardEvent) {
  if (event.key === "Escape") {
    event.preventDefault();
    closeSearch();
  } else if (event.key === "Enter") {
    event.preventDefault();
    // Enter right after typing searches first and stays on the first match from the view.
    if (searchPending) {
      runSearch();
      return;
    }
    step(event.shiftKey ? -1 : 1);
  } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    step(event.key === "ArrowDown" ? 1 : -1);
  }
}

let frame = 0;
function onScroll() {
  if (frame) return;
  frame = requestAnimationFrame(() => {
    frame = 0;
    scrollTop.value = viewport.value?.scrollTop ?? 0;
  });
}

const probe = ref<HTMLElement>();
function measure() {
  const element = viewport.value;
  if (!element) return;
  viewportHeight.value = element.clientHeight;
  const glyph = probe.value?.getBoundingClientRect().width ?? 0;
  if (glyph > 0) charWidth.value = glyph / 10;
  const textArea = element.querySelector<HTMLElement>(".app-virtual-text__rows");
  const textWidth = (textArea?.clientWidth ?? element.clientWidth) - (gutterDigits.value + 2) * charWidth.value;
  if (charWidth.value > 0 && textWidth > 0) columns.value = Math.max(20, Math.floor(textWidth / charWidth.value) - 1);
}

let resizeObserver: ResizeObserver | null = null;
onMounted(() => {
  measure();
  if (typeof ResizeObserver === "undefined" || !viewport.value) return;
  resizeObserver = new ResizeObserver(measure);
  resizeObserver.observe(viewport.value);
});
onBeforeUnmount(() => {
  resizeObserver?.disconnect();
  resizeObserver = null;
  if (frame) cancelAnimationFrame(frame);
  clearTimeout(searchTimer);
});
watch(gutterDigits, measure);

/** Re-reads the viewport size and character width (after the container resized without notice). */
defineExpose({ remeasure: measure });

// ⌘/Ctrl+A selects the whole text: the copy that follows takes it from the source.
const allSelected = ref(false);
function onKeydown(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && !event.altKey && event.key.toLowerCase() === "f") {
    event.preventDefault();
    void openSearch();
    return;
  }
  if (event.key === "Escape" && searchOpen.value) {
    event.preventDefault();
    closeSearch();
    return;
  }
  if ((event.metaKey || event.ctrlKey) && !event.altKey && event.key.toLowerCase() === "a") {
    event.preventDefault();
    allSelected.value = true;
    const rowsElement = viewport.value?.querySelector(".app-virtual-text__rows");
    if (rowsElement) document.getSelection()?.selectAllChildren(rowsElement);
  }
}

/** Source offset of a point in a rendered row (null when outside the rows). */
function sourceOffset(node: Node | null, offset: number): number | null {
  const element = node instanceof Element ? node : node?.parentElement;
  const rowElement = element?.closest<HTMLElement>("[data-row]");
  if (!rowElement || !node) return null;
  const row = Number(rowElement.dataset.row);
  const textElement = rowElement.querySelector(".app-virtual-text__text");
  const start = index.value.starts[row]!;
  const end = index.value.ends[row]!;
  if (!textElement || !textElement.contains(node)) {
    // On the line number (or the row itself): before or after the row's text.
    return offset === 0 && node !== textElement ? start : end;
  }
  // The row's text is split into plain text and <mark> highlights: count the characters
  // before the selection point across those parts.
  const range = document.createRange();
  range.setStart(textElement, 0);
  range.setEnd(node, offset);
  return Math.min(start + range.toString().length, end);
}

function onCopy(event: ClipboardEvent) {
  if (!event.clipboardData) return;
  if (allSelected.value) {
    event.preventDefault();
    event.clipboardData.setData("text/plain", props.text);
    return;
  }
  const selection = document.getSelection();
  if (!selection || selection.rangeCount === 0 || selection.isCollapsed) return;
  const range = selection.getRangeAt(0);
  const from = sourceOffset(range.startContainer, range.startOffset);
  const to = sourceOffset(range.endContainer, range.endOffset);
  if (from === null || to === null || to < from) return;
  // The source keeps line breaks the wrapped rows do not show, and no line numbers.
  event.preventDefault();
  event.clipboardData.setData("text/plain", props.text.slice(from, to));
}

function onPointerDown() {
  allSelected.value = false;
}
</script>

<template>
  <div class="app-virtual-text" @keydown="onKeydown">
    <span ref="probe" class="app-virtual-text__probe" aria-hidden="true">0000000000</span>
    <div v-if="searchOpen" class="app-virtual-text__search" role="search">
      <AppSearchField
        ref="searchField"
        v-model="query"
        class="app-virtual-text__search-field"
        :placeholder="t('textSearch.placeholder')"
        @input="onQueryInput"
        @keydown="onSearchKeydown"
      />
      <span class="app-virtual-text__search-status" aria-live="polite">{{ searchStatus }}</span>
      <el-button
        text
        size="small"
        :icon="ArrowUp"
        :disabled="!matches.length"
        :title="t('textSearch.previous')"
        :aria-label="t('textSearch.previous')"
        @click="step(-1)"
      />
      <el-button
        text
        size="small"
        :icon="ArrowDown"
        :disabled="!matches.length"
        :title="t('textSearch.next')"
        :aria-label="t('textSearch.next')"
        @click="step(1)"
      />
      <el-button
        text
        size="small"
        :icon="Close"
        :title="t('common.close')"
        :aria-label="t('common.close')"
        @click="closeSearch"
      />
    </div>
    <div
      ref="viewport"
      class="app-virtual-text__viewport"
      tabindex="0"
      role="document"
      :aria-label="label"
      :style="{ '--app-virtual-text-gutter': `${gutterDigits}ch` }"
      @scroll.passive="onScroll"
      @copy="onCopy"
      @pointerdown="onPointerDown"
    >
      <div class="app-virtual-text__spacer" :style="{ height: `${window.height}px` }">
        <div class="app-virtual-text__rows" :style="{ transform: `translateY(${window.offset}px)` }">
          <div v-for="item in rows" :key="item.row" class="app-virtual-text__row" :data-row="item.row">
            <span class="app-virtual-text__number" aria-hidden="true">{{ item.number ?? "" }}</span>
            <span class="app-virtual-text__text"><template v-for="(segment, i) in item.segments" :key="i"><mark
              v-if="segment.match !== 'none'"
              :class="['app-virtual-text__match', { 'is-current': segment.match === 'current' }]"
            >{{ segment.text }}</mark><template v-else>{{ segment.text }}</template></template></span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
