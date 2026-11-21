/**
 * Request helpers shared by contribution points that invoke plugin actions through the host
 * (formatters, dashboard widgets). The Go host runs each call as a bounded, cancellable
 * request (backend/plugin/contribution_invoke.go) and reports failures as "<code>: detail".
 */
import { invokeBackend, isDesktopRuntime, nativeErrorMessage } from "@/services/desktop-bridge";

export type ContributionErrorCode =
  | "not_found"
  | "forbidden"
  | "unavailable"
  | "too_large"
  | "timeout"
  | "cancelled"
  | "busy"
  | "invalid_result"
  | "plugin_error"
  | "invalid_request";

/** "unknown" covers native errors that do not carry a contribution error code. */
export interface ContributionError {
  code: ContributionErrorCode | "unknown";
  detail: string;
}

const CONTRIBUTION_ERROR_CODES = new Set<string>([
  "not_found",
  "forbidden",
  "unavailable",
  "too_large",
  "timeout",
  "cancelled",
  "busy",
  "invalid_result",
  "plugin_error",
  "invalid_request",
]);

const REQUEST_ID_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-";

/** A fresh request ID matching the host's ^[A-Za-z0-9_-]{8,64}$. */
export function newContributionRequestId(): string {
  const bytes = new Uint8Array(24);
  globalThis.crypto.getRandomValues(bytes);
  let id = "";
  for (const byte of bytes) id += REQUEST_ID_ALPHABET[byte & 63];
  return id;
}

/**
 * Cancels an outstanding request on the host; the late plugin result is discarded. Resolves
 * whether the request was still in flight. Never throws: cancellation is best effort.
 */
export async function cancelContributionRequest(requestId: string): Promise<boolean> {
  if (!isDesktopRuntime()) return false;
  try {
    const response = await invokeBackend<{ cancelled?: boolean }>("plugin_host_contribution_cancel", { requestId });
    return response?.cancelled === true;
  } catch {
    return false;
  }
}

/** Splits a host error ("<code>: detail") into its code and detail. */
export function parseContributionError(error: unknown): ContributionError {
  const message = typeof error === "string" ? error : nativeErrorMessage(error);
  const match = /^([a-z_]+):\s?([\s\S]*)$/.exec(message.trim());
  if (match && CONTRIBUTION_ERROR_CODES.has(match[1])) {
    return { code: match[1] as ContributionErrorCode, detail: match[2] };
  }
  if (CONTRIBUTION_ERROR_CODES.has(message.trim())) {
    return { code: message.trim() as ContributionErrorCode, detail: "" };
  }
  return { code: "unknown", detail: message };
}
