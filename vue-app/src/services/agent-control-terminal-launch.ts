import { runWithAbort } from "@/domain/async/abort";

interface PendingLaunch {
  command?: string;
  signal: AbortSignal;
  started: () => void;
  failed: (error: unknown) => void;
}

const pending = new Map<string, PendingLaunch>();

export function waitForAgentControlTerminal(paneId: string, command: string | undefined, signal: AbortSignal) {
  const promise = new Promise<void>((resolve, reject) => {
    pending.set(paneId, { command, signal, started: resolve, failed: reject });
  });
  return runWithAbort(signal, () => promise).finally(() => pending.delete(paneId));
}

export function pendingAgentControlTerminal(paneId: string) {
  return pending.get(paneId);
}

/** Wait for the native start even when its caller goes away; the PTY may register late. */
export async function startAgentControlShell<T>(
  signal: AbortSignal,
  start: () => Promise<T>,
  close: () => Promise<unknown>,
  isUnmounted: () => boolean = () => false,
): Promise<T> {
  signal.throwIfAborted();
  const result = await start();
  if (signal.aborted || isUnmounted()) {
    await close().catch(() => undefined);
    signal.throwIfAborted();
    throw new DOMException("Terminal pane closed", "AbortError");
  }
  return result;
}
