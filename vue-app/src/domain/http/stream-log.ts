/** WebSocket/SSE 面板保留的最大日志条数。 */
export const STREAM_LOG_LIMIT = 1000;
/** 单条日志默认只渲染的前缀长度；更长的负载需手动展开。 */
export const STREAM_LOG_PREVIEW_CHARS = 4096;

type FrameScheduler = (callback: () => void) => number;
type FrameCanceller = (handle: number) => void;

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

export interface FrameListBuffer<T> {
  push(item: T): void;
  flush(): void;
  /** 丢弃尚未应用的条目（清空日志、切换会话时使用）。 */
  clear(): void;
}

/** 高频到达的条目按渲染帧合并为一次 apply，避免每条消息都触发响应式更新与布局。 */
export function createFrameListBuffer<T>(
  apply: (items: T[]) => void,
  scheduler: FrameScheduler = defaultFrameScheduler,
  canceller: FrameCanceller = defaultFrameCanceller
): FrameListBuffer<T> {
  let pending: T[] = [];
  let frame = 0;

  const cancelFrame = () => {
    if (!frame) return;
    canceller(frame);
    frame = 0;
  };

  const flush = () => {
    cancelFrame();
    if (!pending.length) return;
    const items = pending;
    pending = [];
    apply(items);
  };

  return {
    push(item) {
      pending.push(item);
      if (!frame) {
        frame = scheduler(() => {
          frame = 0;
          flush();
        });
      }
    },
    flush,
    clear() {
      cancelFrame();
      pending = [];
    },
  };
}

/** 追加一批日志并只保留最新的 limit 条；返回新数组以配合 shallowRef。 */
export function appendStreamLogs<T>(current: readonly T[], incoming: readonly T[], limit = STREAM_LOG_LIMIT) {
  const next = current.concat(incoming);
  return next.length > limit ? next.slice(next.length - limit) : next;
}

export function isStreamLogTruncated(data: string, limit = STREAM_LOG_PREVIEW_CHARS) {
  return data.length > limit;
}

export function streamLogPreview(data: string, limit = STREAM_LOG_PREVIEW_CHARS) {
  return data.length > limit ? `${data.slice(0, limit)}…` : data;
}
