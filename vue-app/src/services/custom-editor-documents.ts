/**
 * IPC adapter for custom editor documents (contributes.customEditors). The
 * Go host binds a UI session to one project file; the frontend never passes
 * a path to the plugin and never accepts `force` from the iframe. Browser
 * preview has no plugin host: every call rejects instead of faking success.
 */
import {
  parseCustomEditorDocumentInfo,
  parseCustomEditorReadChunk,
  type CustomEditorDocumentInfo,
  type CustomEditorReadChunk,
} from "@/domain/plugin-ui/document-protocol";
import { CUSTOM_EDITOR_CHUNK_BYTES, CUSTOM_EDITOR_WRITE_MAX_BYTES } from "@/domain/plugins/custom-editors";
import { parsePluginUISession, pluginUIParentOrigin, type PluginUISession } from "@/services/plugin-service";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export interface CustomEditorDocumentTarget {
  pluginId: string;
  editorId: string;
  viewId: string;
  projectRoot: string;
  relativePath: string;
}

export interface CustomEditorDocumentSession {
  session: PluginUISession;
  document: CustomEditorDocumentInfo;
}

function requireDesktop() {
  if (!isDesktopRuntime()) throw new Error("Custom editors require the MinCode desktop app");
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

export async function beginCustomEditorDocument(target: CustomEditorDocumentTarget): Promise<CustomEditorDocumentSession> {
  requireDesktop();
  const raw = objectValue(await invokeBackend<unknown>("plugin_host_ui_begin_document", {
    pluginId: target.pluginId,
    editorId: target.editorId,
    projectRoot: target.projectRoot,
    relativePath: target.relativePath,
    parentOrigin: pluginUIParentOrigin(),
  }));
  if (!raw) throw new Error("Invalid custom editor document response");
  return {
    session: parsePluginUISession(raw.session, target.pluginId, target.viewId),
    document: parseCustomEditorDocumentInfo(raw.document, target.relativePath),
  };
}

export async function readCustomEditorDocument(
  sessionId: string,
  nonce: string,
  offset: number,
  length: number,
): Promise<CustomEditorReadChunk> {
  requireDesktop();
  const bounded = Math.min(Math.max(1, length), CUSTOM_EDITOR_CHUNK_BYTES);
  const raw = await invokeBackend<unknown>("plugin_host_ui_document_read", { sessionId, nonce, offset, length: bounded });
  return parseCustomEditorReadChunk(raw, bounded);
}

function writeDocument(sessionId: string, nonce: string, args: Record<string, unknown>) {
  return invokeBackend<unknown>("plugin_host_ui_document_write", { sessionId, nonce, ...args });
}

/**
 * Host-side staged write of a buffered save: begin → append chunks. Returns the
 * upload id to commit. A failed step aborts the upload.
 */
export async function stageCustomEditorDocument(
  sessionId: string,
  nonce: string,
  chunks: readonly string[],
  totalBytes: number,
): Promise<string> {
  requireDesktop();
  if (totalBytes > CUSTOM_EDITOR_WRITE_MAX_BYTES) throw new Error("too_large: document exceeds the write limit");
  const begun = objectValue(await writeDocument(sessionId, nonce, { op: "begin" }));
  const uploadId = typeof begun?.uploadId === "string" ? begun.uploadId : "";
  if (!uploadId) throw new Error("Invalid custom editor upload response");
  try {
    for (const data of chunks) {
      if (data) await writeDocument(sessionId, nonce, { op: "append", uploadId, data });
    }
  } catch (error) {
    await abortCustomEditorUpload(sessionId, nonce, uploadId);
    throw error;
  }
  return uploadId;
}

/**
 * Commits a staged upload (atomic rename on the Go side). On a `conflict`
 * error the host keeps the upload so a confirmed overwrite can commit the
 * same upload with `force`; `force` is set only by the host after the user
 * confirmed overwriting a changed file, never by the iframe.
 */
export async function commitCustomEditorUpload(sessionId: string, nonce: string, uploadId: string, force = false): Promise<void> {
  requireDesktop();
  await writeDocument(sessionId, nonce, { op: "commit", uploadId, ...(force ? { force: true } : {}) });
}

export async function abortCustomEditorUpload(sessionId: string, nonce: string, uploadId: string): Promise<void> {
  if (!isDesktopRuntime()) return;
  await writeDocument(sessionId, nonce, { op: "abort", uploadId }).catch(() => undefined);
}
