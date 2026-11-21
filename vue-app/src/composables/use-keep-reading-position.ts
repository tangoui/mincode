import { onScopeDispose, watch, type Ref } from "vue";

/**
 * When an element above the visible part of its scroll view changes height (a step line folding
 * after its run, a card expanding), shift the scroll position by the same amount so the text the
 * reader is looking at stays put. WebKit, which the desktop host uses on macOS, has no CSS scroll anchoring.
 */
export function useKeepReadingPosition(target: Readonly<Ref<HTMLElement | undefined>>) {
  let observer: ResizeObserver | undefined;

  function disconnect() {
    observer?.disconnect();
    observer = undefined;
  }

  watch(
    target,
    (element) => {
      disconnect();
      if (!element || typeof ResizeObserver === "undefined") return;
      const host = element.closest<HTMLElement>(".el-scrollbar__wrap");
      if (!host) return;
      let lastHeight = element.offsetHeight;
      observer = new ResizeObserver(() => {
        const height = element.offsetHeight;
        const delta = height - lastHeight;
        lastHeight = height;
        if (!delta) return;
        // A reader following the end is kept there by the stick-to-bottom observer instead.
        if (host.scrollHeight - host.scrollTop - host.clientHeight <= 32) return;
        // Where the element ended before the change: only content entirely above the view is moved.
        const previousBottom = element.getBoundingClientRect().bottom - delta;
        if (previousBottom <= host.getBoundingClientRect().top) host.scrollTop += delta;
      });
      observer.observe(element);
    },
    { immediate: true, flush: "post" },
  );
  onScopeDispose(disconnect);
}
