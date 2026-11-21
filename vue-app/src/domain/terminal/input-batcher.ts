export interface TerminalInputBatcher {
  push(input: string, immediate?: boolean): Promise<void>;
  flush(): Promise<void>;
  dispose(): void;
}

export interface TerminalInputBatcherOptions {
  delayMs?: number;
  maxChars?: number;
  onError?: (error: unknown) => void;
}

/** Control keys (Ctrl-C/D, backspace, newline, Enter, Esc) must reach the PTY without batching delay. */
export function isImmediateTerminalInput(input: string) {
  return Array.from(input).some((character) => {
    const code = character.charCodeAt(0);
    return code === 3 || code === 4 || code === 8 || code === 10 || code === 13 || code === 27;
  });
}

/**
 * Coalesces keystrokes without adding latency: input typed while no write is in
 * flight goes out at once; input typed while a confirmed write is still pending
 * joins one batch that is sent as soon as that write settles (control keys and
 * `maxChars` flush right away, still in order behind the pending write).
 * `delayMs` is kept for callers but no longer delays an idle keystroke.
 */
export function createTerminalInputBatcher(
  write: (input: string) => Promise<void>,
  options: TerminalInputBatcherOptions = {},
): TerminalInputBatcher {
  const maxChars = options.maxChars ?? 256;
  let pending = "";
  let inFlight = 0;
  /** A batch is queued to go out when the current write settles. */
  let deferred: Promise<void> | undefined;
  let chain: Promise<void> = Promise.resolve();
  let disposed = false;

  function send(input: string) {
    inFlight += 1;
    const next = chain.then(() => write(input));
    const settled = next.finally(() => { inFlight -= 1; });
    chain = settled.catch(() => undefined);
    return next;
  }

  function flush() {
    const input = pending;
    pending = "";
    if (!input || disposed) return chain;
    return send(input);
  }

  function push(input: string, immediate = false) {
    if (!input || disposed) return chain;
    pending += input;
    if (immediate || inFlight === 0 || pending.length >= maxChars) return flush();
    // A write is in flight: send everything typed meanwhile as one batch right after it.
    deferred ??= chain.then(() => {
      deferred = undefined;
      if (!pending || disposed) return;
      return flush().catch((error) => options.onError?.(error));
    });
    return deferred;
  }

  return {
    push,
    flush,
    dispose() {
      disposed = true;
      pending = "";
    },
  };
}
