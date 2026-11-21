/**
 * E2E-only seam: lets the desktop smoke harness read what a local terminal shows
 * (the xterm buffer; with the WebGL renderer the DOM has no text). Off by default:
 * only the smoke runner, which loads only in `-tags e2e` builds, turns it on, so in
 * the product nothing ever registers (a terminal makes one no-op call on mount).
 */
let enabled = false;
const readers = new Map<string, () => string>();

export function enableTerminalBufferProbe() {
  enabled = true;
}

/** Registers a reader for one terminal session; returns the unregister function. */
export function registerTerminalBufferReader(sessionId: string, read: () => string): () => void {
  if (!enabled) return () => undefined;
  readers.set(sessionId, read);
  return () => {
    if (readers.get(sessionId) === read) readers.delete(sessionId);
  };
}

/** The session's buffer text (scrollback included), or undefined if no terminal is mounted for it. */
export function readTerminalBuffer(sessionId: string): string | undefined {
  return readers.get(sessionId)?.();
}

export interface TerminalSizeReport {
  /** The xterm grid. */
  cols: number;
  rows: number;
  /** What the FitAddon would fit into the container now (null when it cannot measure). */
  proposed: { cols: number; rows: number } | null;
}

const sizeReaders = new Map<string, () => TerminalSizeReport>();

/** Registers a grid-size reader for one terminal session; returns the unregister function. */
export function registerTerminalSizeReader(sessionId: string, read: () => TerminalSizeReport): () => void {
  if (!enabled) return () => undefined;
  sizeReaders.set(sessionId, read);
  return () => {
    if (sizeReaders.get(sessionId) === read) sizeReaders.delete(sessionId);
  };
}

/** The session's xterm grid and fit proposal, or undefined if no terminal is mounted for it. */
export function readTerminalSize(sessionId: string): TerminalSizeReport | undefined {
  return sizeReaders.get(sessionId)?.();
}
