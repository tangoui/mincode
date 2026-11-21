/**
 * Debugger sources that are not files on disk: classes of the JDK or of a
 * dependency (java-debug reports `jdt://contents/...` with a
 * `sourceReference`), or code a Python adapter only holds in memory. Their
 * text comes from the DAP `source` request and is shown read-only.
 */
import { isValidDebuggerAddress } from "./address-breakpoints.ts";
import type { DapSource } from "@/domain/debug/types";

/** Scheme of the editor documents that show such a source. */
export const VIRTUAL_SOURCE_SCHEME = "debug-source://";

function isLocalAbsolutePath(path: string): boolean {
  return path.startsWith("/") || /^[A-Za-z]:[\\/]/.test(path);
}

/** The source has no local file to open: its text must come from the adapter. */
export function isVirtualDebugSource(source: DapSource | undefined): boolean {
  if (!source) return false;
  const path = typeof source.path === "string" ? source.path : "";
  if (path && isLocalAbsolutePath(path)) return false;
  return (typeof source.sourceReference === "number" && source.sourceReference > 0) || path !== "";
}

/** The document path is a virtual source document. */
export function isVirtualSourceDocumentPath(path: string): boolean {
  return path.startsWith(VIRTUAL_SOURCE_SCHEME);
}

/**
 * Stable editor document path of a virtual source within a session:
 * `debug-source://<session>/<key>/<name>`. The key is the source reference
 * (or the adapter's path) encoded as one segment; the last segment is the
 * source name, which picks the tab title and the editor language.
 */
export function virtualSourceDocumentPath(sessionId: string, source: DapSource, fallbackName: string): string {
  const reference = typeof source.sourceReference === "number" && source.sourceReference > 0 ? String(source.sourceReference) : "";
  const path = typeof source.path === "string" ? source.path : "";
  const key = encodeURIComponent(path || `ref:${reference}`);
  const fromPath = path.replace(/[?#].*$/, "").split(/[\\/]/).filter(Boolean).pop() ?? "";
  const name = (source.name?.trim() || fromPath || fallbackName.trim() || "source").replace(/[\\/]/g, "_");
  return `${VIRTUAL_SOURCE_SCHEME}${encodeURIComponent(sessionId)}/${key}/${name}`;
}

/**
 * What a virtual source document path (virtualSourceDocumentPath) names: the
 * session that opened it, and the debugger address of its source when the
 * adapter gave one the host accepts (a source known only by its
 * sourceReference, or with an address the host refuses, has none). Null for
 * any other path.
 */
export function virtualSourceDocumentInfo(path: string): { sessionId: string; address?: string } | null {
  if (!isVirtualSourceDocumentPath(path)) return null;
  const [session, key] = path.slice(VIRTUAL_SOURCE_SCHEME.length).split("/");
  if (session === undefined || key === undefined) return null;
  try {
    const sessionId = decodeURIComponent(session);
    const address = decodeURIComponent(key);
    return isValidDebuggerAddress(address) ? { sessionId, address } : { sessionId };
  } catch {
    return null;
  }
}

/**
 * A saved editor tab that can never be reopened: a debugger source document
 * (its source reference only lived as long as its session), or a `jdt://`
 * class address that older versions joined onto the project root and saved
 * as if it were a file (reopening it reported "cannot open this file").
 */
export function isStaleDebugSourceTabPath(path: string): boolean {
  if (isVirtualSourceDocumentPath(path)) return true;
  return /(^|\/)jdt:\/\//.test(path.replace(/\\/g, "/"));
}

/**
 * The path is a debugger address (a `debug-source://` document or a `jdt://`
 * class, alone or wrongly joined onto a folder), not a file on disk: it must
 * never reach the local file reader.
 */
export function isDebuggerAddressPath(path: string): boolean {
  return isStaleDebugSourceTabPath(path);
}
