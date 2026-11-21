/**
 * Search-box keyboard handling shared by the SSH and database connection overviews:
 * the highlighted result, ↑/↓/Enter in the search box, scrolling the highlighted row
 * into view, and focusing the search box on mount/activation unless a dialog is open.
 */
import { nextTick, onActivated, onMounted, ref, watch, type Ref } from "vue";
import { moveHighlight } from "@/domain/connections/overview";

export interface OverviewSearchKeyboardOptions {
  /** Search box text; nothing is highlighted while it is blank. */
  query: Readonly<Ref<string>>;
  /** Number of highlightable results, in display order. */
  count: Readonly<Ref<number>>;
  /** Acts on the result at `index` (Enter in the search box). */
  activate: (index: number) => void;
  searchField: Readonly<Ref<{ focus: () => void } | undefined>>;
  /** Element holding the rows; its `.connection-overview__row.is-highlighted` is scrolled into view. */
  scrollContainer: Readonly<Ref<HTMLElement | undefined>>;
  /**
   * Brings result `index` into view when the list renders only a window of its rows, so the
   * highlighted row may not exist in the DOM yet. Runs before the DOM lookup above.
   */
  revealIndex?: (index: number) => void;
}

/** True while a modal dialog (ours or the workspace's) is open or holds focus. */
function dialogIsOpen() {
  const active = document.activeElement;
  if (active instanceof HTMLElement && active.closest('[role="dialog"], .el-overlay')) return true;
  return [...document.querySelectorAll<HTMLElement>('[aria-modal="true"]')].some(
    (dialog) => dialog.getClientRects().length > 0
  );
}

/**
 * A double-click fires click twice before dblclick; only the first click of a burst
 * may act, so one double-click on a row's action button acts once.
 */
export function isRepeatClick(event: MouseEvent) {
  return event.detail > 1;
}

export function useOverviewSearchKeyboard(options: OverviewSearchKeyboardOptions) {
  const { query, count, activate, searchField, scrollContainer, revealIndex } = options;
  const highlightIndex = ref(-1);

  /**
   * Highlights the first result only once the user has typed something; an idle page
   * highlights nothing, so Enter in the auto-focused search box cannot act.
   * Arrow keys still start from the first row.
   */
  function resetHighlight() {
    highlightIndex.value = query.value.trim() && count.value ? 0 : -1;
  }

  function scrollHighlightIntoView() {
    if (revealIndex && highlightIndex.value >= 0) revealIndex(highlightIndex.value);
    void nextTick(() => {
      scrollContainer.value
        ?.querySelector<HTMLElement>(".connection-overview__row.is-highlighted")
        ?.scrollIntoView?.({ block: "nearest" });
    });
  }

  function onSearchKeydown(event: KeyboardEvent) {
    if (event.isComposing) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      highlightIndex.value = moveHighlight(highlightIndex.value, event.key === "ArrowDown" ? 1 : -1, count.value);
      scrollHighlightIntoView();
    } else if (event.key === "Enter") {
      event.preventDefault();
      if (highlightIndex.value >= 0 && highlightIndex.value < count.value) activate(highlightIndex.value);
    }
  }

  function focusSearch() {
    void nextTick(() => {
      if (!dialogIsOpen()) searchField.value?.focus();
    });
  }

  watch(count, (next) => {
    if (highlightIndex.value >= next || (highlightIndex.value < 0 && next)) resetHighlight();
  }, { immediate: true });

  onMounted(focusSearch);
  onActivated(focusSearch);

  return { highlightIndex, resetHighlight, onSearchKeydown, focusSearch };
}
