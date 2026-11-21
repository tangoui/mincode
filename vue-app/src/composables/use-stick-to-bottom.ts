import { onScopeDispose, watch, type Ref } from "vue";

export interface StickToBottomOptions<T extends HTMLElement> {
  /** The scrolling element; its first child is the content whose height changes. */
  host: Readonly<Ref<T | undefined>>;
  /** True while the reader is at the bottom and new content should keep the view there. */
  following: () => boolean;
  /** Called after the view was moved, so callers can refresh position-dependent state. */
  onAligned?: (host: T) => void;
}

/**
 * Keeps a scroll view pinned to its end while content grows, as long as the reader has not
 * scrolled away. Both the content and the viewport are observed: streamed text, expanding cards
 * and late markdown layout grow the content, and an entering drawer changes the viewport height.
 * Alignment is instant because a smooth scroll restarted on every frame never reaches the end in
 * WebKit.
 */
export function useStickToBottom<T extends HTMLElement>(options: StickToBottomOptions<T>) {
  let observer: ResizeObserver | undefined;

  function align() {
    const host = options.host.value;
    if (!host || !options.following()) return;
    const bottom = host.scrollHeight - host.clientHeight;
    if (Math.abs(host.scrollTop - bottom) > 1) host.scrollTop = bottom;
    options.onAligned?.(host);
  }

  function disconnect() {
    observer?.disconnect();
    observer = undefined;
  }

  watch(
    options.host,
    (host) => {
      disconnect();
      if (!host || typeof ResizeObserver === "undefined") return;
      observer = new ResizeObserver(align);
      observer.observe(host);
      if (host.firstElementChild) observer.observe(host.firstElementChild);
    },
    { immediate: true, flush: "post" },
  );
  onScopeDispose(disconnect);

  return { align };
}
