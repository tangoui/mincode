/**
 * Where debug popups anchor at a line of code (Smart Step Into's choice
 * opens below the execution line, as in an IDE). The project workspace
 * registers a provider for its text editor while it is the active view; the
 * provider answers only for the document it shows and a line that is on
 * screen, so callers fall back to their own anchor otherwise.
 */
import type { DapStackFrame } from "@/domain/debug/types";
import { isVirtualDebugSource, virtualSourceDocumentPath } from "@/domain/debug/virtual-source";
import { pathsAreCaseInsensitive } from "@/stores/debug-configs";

export interface DebugScreenPoint {
  x: number;
  y: number;
}

/** The viewport point just below (line, column) of the document at path, or null when it is not shown. */
export type DebugLineAnchorProvider = (path: string, line: number, column: number) => DebugScreenPoint | null;

let provider: DebugLineAnchorProvider | null = null;

/** Registers the active workspace's provider; returns the disposer. */
export function setDebugLineAnchorProvider(next: DebugLineAnchorProvider): () => void {
  provider = next;
  return () => {
    if (provider === next) provider = null;
  };
}

/** Both document paths name the same file (`\` vs `/` and, where paths fold case, case ignored). */
export function sameDebugDocumentPath(a: string, b: string): boolean {
  const left = a.replace(/\\/g, "/");
  const right = b.replace(/\\/g, "/");
  return pathsAreCaseInsensitive() ? left.toLowerCase() === right.toLowerCase() : left === right;
}

/** The editor document path of a frame's source (a virtual document for adapter-held sources), or null. */
export function debugFrameDocumentPath(sessionId: string, frame: DapStackFrame): string | null {
  if (frame.source && isVirtualDebugSource(frame.source)) return virtualSourceDocumentPath(sessionId, frame.source, frame.name);
  return frame.source?.path ?? null;
}

/** The viewport point below frame's line in the editor, or null when no editor shows it. */
export function debugFrameLineAnchor(sessionId: string, frame: DapStackFrame): DebugScreenPoint | null {
  if (!provider || frame.line < 1) return null;
  const path = debugFrameDocumentPath(sessionId, frame);
  return path ? provider(path, frame.line, Math.max(1, frame.column)) : null;
}
