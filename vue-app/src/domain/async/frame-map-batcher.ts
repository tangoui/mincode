export interface FrameMapBatcher<V> {
  /** Queues a value; values set before the next frame are committed together. */
  set(key: string, value: V): void;
  /** The queued value not committed yet. */
  get(key: string): V | undefined;
  delete(key: string): void;
  /** Drops queued values without committing them. */
  clear(): void;
  /** Commits queued values now. */
  flush(): void;
  dispose(): void;
}

export interface FrameMapSchedulers {
  scheduleFrame?: (callback: () => void) => number;
  cancelFrame?: (handle: number) => void;
  scheduleTimer?: (callback: () => void) => number;
  cancelTimer?: (handle: number) => void;
}

// Frames pause while the window is hidden; the timer still commits within this bound.
const FALLBACK_MS = 100;

/**
 * 把同一渲染帧内陆续到达的多项结果合并成一次提交，避免每个结果各触发一次响应式更新和整页渲染
 * （例如连续展开多个项目时，几个后端响应落在同一帧）。渲染帧暂停时由兜底计时器提交。
 */
export function createFrameMapBatcher<V>(
  commit: (entries: [string, V][]) => void,
  schedulers: FrameMapSchedulers = {},
): FrameMapBatcher<V> {
  const scheduleFrame = schedulers.scheduleFrame ?? ((callback: () => void) => globalThis.requestAnimationFrame(callback));
  const cancelFrame = schedulers.cancelFrame ?? ((handle: number) => globalThis.cancelAnimationFrame(handle));
  const scheduleTimer = schedulers.scheduleTimer ?? ((callback: () => void) => globalThis.setTimeout(callback, FALLBACK_MS) as unknown as number);
  const cancelTimer = schedulers.cancelTimer ?? ((handle: number) => globalThis.clearTimeout(handle));
  const pending = new Map<string, V>();
  let frame = 0;
  let timer = 0;
  let disposed = false;

  const cancel = () => {
    if (frame) cancelFrame(frame);
    if (timer) cancelTimer(timer);
    frame = 0;
    timer = 0;
  };

  const flush = () => {
    cancel();
    if (!pending.size) return;
    const entries = [...pending];
    pending.clear();
    commit(entries);
  };

  return {
    set(key, value) {
      if (disposed) return;
      pending.set(key, value);
      if (!frame) frame = scheduleFrame(flush);
      if (!timer) timer = scheduleTimer(flush);
    },
    get: (key) => pending.get(key),
    delete(key) {
      pending.delete(key);
      if (!pending.size) cancel();
    },
    clear() {
      pending.clear();
      cancel();
    },
    flush,
    dispose() {
      disposed = true;
      pending.clear();
      cancel();
    },
  };
}
