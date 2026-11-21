export interface FrameTextBatcher {
  append(key: string, text: string): void;
  flush(key?: string): void;
  dispose(options?: { flush?: boolean }): void;
}

type FrameScheduler = (callback: () => void) => number;
type FrameCanceller = (handle: number) => void;
interface PendingTextSegment {
  key: string;
  text: string;
}

function defaultFrameScheduler(callback: () => void) {
  if (typeof globalThis.requestAnimationFrame === "function") {
    return globalThis.requestAnimationFrame(callback);
  }
  return globalThis.setTimeout(callback, 0) as unknown as number;
}

function defaultFrameCanceller(handle: number) {
  if (typeof globalThis.cancelAnimationFrame === "function") {
    globalThis.cancelAnimationFrame(handle);
    return;
  }
  globalThis.clearTimeout(handle);
}

/**
 * 将高频文本增量按渲染帧合并，避免每个 token 都触发响应式更新和布局计算。
 * 只合并连续同 key 的片段，确保思考、回复等交错事件仍保持原始顺序。
 */
export function createFrameTextBatcher(
  apply: (key: string, text: string) => void,
  scheduler: FrameScheduler = defaultFrameScheduler,
  canceller: FrameCanceller = defaultFrameCanceller,
): FrameTextBatcher {
  let pending: PendingTextSegment[] = [];
  let frame = 0;
  let disposed = false;

  const cancelFrame = () => {
    if (!frame) return;
    canceller(frame);
    frame = 0;
  };

  const flush = (key?: string) => {
    if (key !== undefined) {
      const matching = pending.filter((segment) => segment.key === key);
      pending = pending.filter((segment) => segment.key !== key);
      for (const segment of matching) apply(segment.key, segment.text);
      if (!pending.length) cancelFrame();
      return;
    }

    cancelFrame();
    const segments = pending;
    pending = [];
    for (const segment of segments) apply(segment.key, segment.text);
  };

  const schedule = () => {
    if (frame || disposed) return;
    frame = scheduler(() => {
      frame = 0;
      flush();
    });
  };

  return {
    append(key, text) {
      if (disposed || !text) return;
      const last = pending[pending.length - 1];
      if (last?.key === key) last.text += text;
      else pending.push({ key, text });
      schedule();
    },
    flush,
    dispose(options = {}) {
      if (disposed) return;
      disposed = true;
      if (options.flush) flush();
      else {
        cancelFrame();
        pending = [];
      }
    },
  };
}
