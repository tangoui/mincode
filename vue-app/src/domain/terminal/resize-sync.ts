export interface TerminalDimensions {
  cols: number;
  rows: number;
}

export interface TerminalResizeSyncOptions {
  /** Fits the terminal and returns the size to report, or null when there is no usable layout yet. */
  measure: () => TerminalDimensions | null;
  /** Whether the backend session can currently accept a resize. */
  canSend: () => boolean;
  send: (dimensions: TerminalDimensions) => Promise<void> | void;
  requestFrame?: (callback: () => void) => number;
  cancelFrame?: (handle: number) => void;
}

export interface TerminalResizeSync {
  /** Coalesces continuous resize notifications (e.g. split dragging) into one fit per frame. */
  schedule(): void;
  /** Fits immediately and reports the size only if it differs from the last one sent. */
  flush(): void;
  /** Forgets the last sent size so the next flush always reports it (connect / reconnect). */
  reset(): void;
  /** Fits and sends the current size even if it was sent before (self-heal of a stale PTY size). */
  resync(): void;
  dispose(): void;
}

export function createTerminalResizeSync(options: TerminalResizeSyncOptions): TerminalResizeSync {
  const requestFrame =
    options.requestFrame ?? ((callback: () => void) => window.requestAnimationFrame(callback));
  const cancelFrame = options.cancelFrame ?? ((handle: number) => window.cancelAnimationFrame(handle));
  let frame: number | undefined;
  let lastSent: TerminalDimensions | null = null;
  let disposed = false;

  function cancelScheduled() {
    if (frame === undefined) return;
    cancelFrame(frame);
    frame = undefined;
  }

  function flush() {
    cancelScheduled();
    if (disposed) return;
    const dimensions = options.measure();
    if (!dimensions || !options.canSend()) return;
    if (lastSent && lastSent.cols === dimensions.cols && lastSent.rows === dimensions.rows) return;
    const sent = { cols: dimensions.cols, rows: dimensions.rows };
    lastSent = sent;
    void Promise.resolve()
      .then(() => options.send(sent))
      .catch(() => {
        // 发送失败时忘记该尺寸，下一次 flush 会重试。
        if (lastSent === sent) lastSent = null;
      });
  }

  return {
    schedule() {
      if (disposed || frame !== undefined) return;
      frame = requestFrame(() => {
        frame = undefined;
        flush();
      });
    },
    flush,
    reset() {
      lastSent = null;
    },
    resync() {
      lastSent = null;
      flush();
    },
    dispose() {
      disposed = true;
      cancelScheduled();
    },
  };
}
