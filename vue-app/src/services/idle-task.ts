/**
 * Runs non-essential startup work after first paint. Falls back to a timer where
 * requestIdleCallback is unavailable (WebKit); `timeout` bounds how long a busy
 * main thread can postpone the task. Returns a cancel function.
 */
export function scheduleIdleTask(task: () => void, timeout = 2000): () => void {
  const scope = globalThis as typeof globalThis & {
    requestIdleCallback?: (callback: () => void, options?: { timeout: number }) => number;
    cancelIdleCallback?: (handle: number) => void;
  };
  if (typeof scope.requestIdleCallback === "function") {
    const handle = scope.requestIdleCallback(task, { timeout });
    return () => scope.cancelIdleCallback?.(handle);
  }
  const handle = setTimeout(task, Math.min(timeout, 200));
  return () => clearTimeout(handle);
}
