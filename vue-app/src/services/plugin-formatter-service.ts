/**
 * Invokes a plugin formatter through the host (`plugin_host_format`). The
 * host checks the formatter still exists, covers the language, runs the
 * plugin's `read`-risk action with a bounded timeout (10 s explicit, 3 s on
 * save) and payload/result limits, and returns the formatted document text
 * (`unchanged` when the plugin made no change). Cancellation is best effort;
 * a late result is discarded by the host.
 */
import { FORMATTER_LIMITS, utf8ByteLength, type PluginFormatter } from "@/domain/plugins/formatters";
import {
  cancelContributionRequest,
  newContributionRequestId,
  parseContributionError,
  type ContributionError,
} from "@/services/plugin-contribution-requests";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export interface FormatRange {
  startLineNumber: number;
  startColumn: number;
  endLineNumber: number;
  endColumn: number;
}

export interface FormatDocumentRequest {
  languageId: string;
  /** Absolute path or name; the host keeps only the base name. */
  fileName: string;
  /** Clean project-relative path, when the document is inside a project. */
  relativePath?: string;
  text: string;
  range?: FormatRange;
  tabSize: number;
  insertSpaces: boolean;
  eol: "\n" | "\r\n";
  mode: "explicit" | "save";
}

export type FormatDocumentResult =
  | { ok: true; text?: string }
  | { ok: false; error: ContributionError };

interface FormatResponse {
  text?: string;
  unchanged?: boolean;
}

/** Sends the document to the formatter. Never throws; `signal` aborts the request. */
export async function formatWithPlugin(
  formatter: Pick<PluginFormatter, "pluginId" | "id">,
  request: FormatDocumentRequest,
  signal?: AbortSignal,
): Promise<FormatDocumentResult> {
  if (!isDesktopRuntime()) return { ok: false, error: { code: "unavailable", detail: "desktop runtime required" } };
  if (utf8ByteLength(request.text) > FORMATTER_LIMITS.payloadMaxBytes - 4096) {
    return { ok: false, error: { code: "too_large", detail: "document is too large to format" } };
  }
  if (signal?.aborted) return { ok: false, error: { code: "cancelled", detail: "" } };
  const requestId = newContributionRequestId();
  const abort = () => void cancelContributionRequest(requestId);
  signal?.addEventListener("abort", abort, { once: true });
  try {
    const response = await invokeBackend<FormatResponse>("plugin_host_format", {
      pluginId: formatter.pluginId,
      formatterId: formatter.id,
      requestId,
      languageId: request.languageId,
      fileName: request.fileName,
      ...(request.relativePath ? { relativePath: request.relativePath } : {}),
      text: request.text,
      ...(request.range ? { range: request.range } : {}),
      options: {
        tabSize: Math.min(32, Math.max(1, Math.trunc(request.tabSize) || 4)),
        insertSpaces: request.insertSpaces,
        eol: request.eol,
      },
      mode: request.mode,
    });
    if (signal?.aborted) return { ok: false, error: { code: "cancelled", detail: "" } };
    if (!response || response.unchanged || typeof response.text !== "string") return { ok: true };
    return { ok: true, text: response.text };
  } catch (error) {
    return { ok: false, error: parseContributionError(error) };
  } finally {
    signal?.removeEventListener("abort", abort);
  }
}
