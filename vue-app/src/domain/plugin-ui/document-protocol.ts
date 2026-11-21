/**
 * Document bridge between a custom editor iframe and the host
 * (contributes.customEditors, `features.document: 1`). Everything here is
 * pure parsing of untrusted messages and host responses; no IPC or Vue.
 *
 * iframe → host (requests, answered with `mincode.host.response`):
 * - `mincode.plugin.document.read {id, offset, length}` (length ≤ chunkBytes)
 * - `mincode.plugin.document.save {id, uploadId?, data, last}` (base64, ≤ chunkBytes raw)
 * - `mincode.plugin.document.dirty {id, dirty}`
 * iframe → host (notification): `mincode.plugin.document.save-result {requestId, ok, error?}`
 * host → iframe: `mincode.host.document {document}`,
 * `mincode.host.document.save-request {requestId}`, `mincode.host.document.changed {document}`.
 *
 * The iframe never sends paths and never sends `force`: the host resolves
 * the bound file and asks the user before overwriting a changed file.
 */
import {
  CUSTOM_EDITOR_CHUNK_BYTES,
  CUSTOM_EDITOR_READ_MAX_BYTES,
  CUSTOM_EDITOR_WRITE_MAX_BYTES,
  type CustomEditorAccess,
} from "@/domain/plugins/custom-editors";
import { PLUGIN_UI_MAX_MESSAGE_BYTES, pluginUIMessageByteLength } from "@/domain/plugin-ui/protocol";

/** Document facts shared with the iframe (connect message and `mincode.host.document`). */
export interface CustomEditorDocumentInfo {
  name: string;
  relativePath: string;
  /** Absolute path; present only when the plugin holds `workspace.read`. */
  path?: string;
  size: number;
  /** Last modification time, Unix epoch milliseconds. */
  modifiedAt?: number;
  access: CustomEditorAccess;
  maxReadBytes: number;
  maxWriteBytes: number;
  chunkBytes: number;
}

export interface CustomEditorReadChunk {
  data: string;
  eof: boolean;
  size: number;
  modifiedAt?: number;
}

export type PluginUIDocumentRequest =
  | { type: "mincode.plugin.document.read"; id: string; offset: number; length: number }
  | { type: "mincode.plugin.document.save"; id: string; uploadId?: string; data: string; last: boolean }
  | { type: "mincode.plugin.document.dirty"; id: string; dirty: boolean };

export interface PluginUIDocumentSaveResult {
  type: "mincode.plugin.document.save-result";
  requestId: string;
  ok: boolean;
  error?: string;
}

const BASE64_PATTERN = /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/;
const ID_MAX_LENGTH = 128;
const UPLOAD_ID_PATTERN = /^[A-Za-z0-9_-]{1,64}$/;

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function boundedText(value: unknown, maxLength: number) {
  return typeof value === "string" && value.length > 0 && value.length <= maxLength ? value : undefined;
}

function nonNegativeInteger(value: unknown) {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : undefined;
}

function modifiedAtValue(value: unknown): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : undefined;
}

export function isBase64(value: string) {
  return value.length % 4 === 0 && BASE64_PATTERN.test(value);
}

/** Decoded byte length of a valid base64 string. */
export function base64ByteLength(value: string) {
  if (!value) return 0;
  const padding = value.endsWith("==") ? 2 : value.endsWith("=") ? 1 : 0;
  return (value.length / 4) * 3 - padding;
}

export function parsePluginUIDocumentRequest(value: unknown, chunkBytes = CUSTOM_EDITOR_CHUNK_BYTES): PluginUIDocumentRequest | undefined {
  const raw = recordValue(value);
  const id = boundedText(raw?.id, ID_MAX_LENGTH);
  if (!raw || !id || pluginUIMessageByteLength(raw) > PLUGIN_UI_MAX_MESSAGE_BYTES) return undefined;
  if (raw.type === "mincode.plugin.document.read") {
    const offset = nonNegativeInteger(raw.offset);
    const length = nonNegativeInteger(raw.length);
    if (offset === undefined || length === undefined || length < 1 || length > chunkBytes
      || offset > CUSTOM_EDITOR_READ_MAX_BYTES) return undefined;
    return { type: raw.type, id, offset, length };
  }
  if (raw.type === "mincode.plugin.document.save") {
    if (typeof raw.data !== "string" || !isBase64(raw.data) || base64ByteLength(raw.data) > chunkBytes) return undefined;
    if (typeof raw.last !== "boolean") return undefined;
    if (raw.uploadId !== undefined && (typeof raw.uploadId !== "string" || !UPLOAD_ID_PATTERN.test(raw.uploadId))) return undefined;
    // Paths and force are host-only; a message carrying them is rejected outright.
    if ("force" in raw || "path" in raw || "relativePath" in raw) return undefined;
    return { type: raw.type, id, uploadId: raw.uploadId as string | undefined, data: raw.data, last: raw.last };
  }
  if (raw.type === "mincode.plugin.document.dirty") {
    if (typeof raw.dirty !== "boolean") return undefined;
    return { type: raw.type, id, dirty: raw.dirty };
  }
  return undefined;
}

export function parsePluginUIDocumentSaveResult(value: unknown): PluginUIDocumentSaveResult | undefined {
  const raw = recordValue(value);
  if (!raw || raw.type !== "mincode.plugin.document.save-result") return undefined;
  const requestId = boundedText(raw.requestId, ID_MAX_LENGTH);
  if (!requestId || typeof raw.ok !== "boolean") return undefined;
  const error = typeof raw.error === "string" ? raw.error.slice(0, 500) : undefined;
  return { type: raw.type, requestId, ok: raw.ok, error };
}

/** Validates the `document` half of `plugin_host_ui_begin_document`. */
export function parseCustomEditorDocumentInfo(value: unknown, expectedRelativePath: string): CustomEditorDocumentInfo {
  const raw = recordValue(value);
  const name = boundedText(raw?.name, 512);
  const relativePath = boundedText(raw?.relativePath, 4096);
  const size = nonNegativeInteger(raw?.size);
  const access = raw?.access === "readWrite" ? "readWrite" : raw?.access === "read" ? "read" : undefined;
  const maxReadBytes = nonNegativeInteger(raw?.maxReadBytes) ?? CUSTOM_EDITOR_READ_MAX_BYTES;
  if (!raw || !name || !relativePath || size === undefined || !access
    || relativePath.replace(/\\/g, "/") !== expectedRelativePath.replace(/\\/g, "/")
    || size > CUSTOM_EDITOR_READ_MAX_BYTES) {
    throw new Error("Invalid custom editor document response");
  }
  const path = boundedText(raw.path, 4096);
  return {
    name,
    relativePath,
    ...(path ? { path } : {}),
    size,
    modifiedAt: modifiedAtValue(raw.modifiedAt),
    access,
    maxReadBytes: Math.min(maxReadBytes, CUSTOM_EDITOR_READ_MAX_BYTES),
    maxWriteBytes: access === "readWrite"
      ? Math.min(nonNegativeInteger(raw.maxWriteBytes) ?? CUSTOM_EDITOR_WRITE_MAX_BYTES, CUSTOM_EDITOR_WRITE_MAX_BYTES)
      : 0,
    chunkBytes: Math.min(nonNegativeInteger(raw.chunkBytes) || CUSTOM_EDITOR_CHUNK_BYTES, CUSTOM_EDITOR_CHUNK_BYTES),
  };
}

export function parseCustomEditorReadChunk(value: unknown, requestedLength: number): CustomEditorReadChunk {
  const raw = recordValue(value);
  const data = raw?.data === undefined || raw?.data === null ? "" : raw.data;
  const size = nonNegativeInteger(raw?.size);
  if (!raw || typeof data !== "string" || !isBase64(data) || base64ByteLength(data) > requestedLength
    || typeof raw.eof !== "boolean" || size === undefined) {
    throw new Error("Invalid custom editor read response");
  }
  return { data, eof: raw.eof, size, modifiedAt: modifiedAtValue(raw.modifiedAt) };
}

/** Host error code: Go errors read "<code>: <detail>" (conflict, too_large, not_found, forbidden, stale_session, invalid_request). */
export function customEditorErrorCode(error: unknown): string | undefined {
  const message = error instanceof Error ? error.message : String(error ?? "");
  return /\b(conflict|too_large|not_found|forbidden|stale_session|invalid_request):/.exec(message)?.[1];
}

/** Commit conflict reported by the host (baseline sha256 changed on disk). */
export function isCustomEditorConflictError(error: unknown) {
  return customEditorErrorCode(error) === "conflict";
}
