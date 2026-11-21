export const UI_SESSION_CAPTURE_EVENT = "app:ui-session-capture";

type UiSessionFlusher = () => void | Promise<unknown>;
const flushers = new Set<UiSessionFlusher>();

/**
 * Ask mounted feature views to synchronously copy transient UI positions into
 * their persistent stores before the debounced store writers are flushed.
 */
export function requestUiSessionCapture(): void {
  if (typeof window === "undefined") return;
  window.dispatchEvent(new Event(UI_SESSION_CAPTURE_EVENT));
}

export function registerUiSessionFlusher(flusher: UiSessionFlusher): () => void {
  flushers.add(flusher);
  return () => flushers.delete(flusher);
}

export async function flushUiSessionPersistence(): Promise<void> {
  await Promise.allSettled([...flushers].map((flusher) => Promise.resolve().then(flusher)));
}
