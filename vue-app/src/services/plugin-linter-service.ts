/**
 * Invokes a plugin linter through the host (`plugin_host_lint`). The host
 * checks the linter still exists, covers the language and declares the
 * trigger, runs the plugin's `read`-risk action with a 10 s timeout and
 * bounded payload/result, and returns sanitised diagnostics. Requests are
 * queued per plugin (services/plugin-linter-queue.ts): one call in flight,
 * only the newest version of each model kept, and a stale in-flight call is
 * cancelled with plugin_host_contribution_cancel.
 */
import { LINTER_LIMITS, parseLintResult, type LinterTrigger, type PluginLinter } from "@/domain/plugins/linters";
import { utf8ByteLength } from "@/domain/plugins/formatters";
import {
  cancelContributionRequest,
  newContributionRequestId,
  parseContributionError,
} from "@/services/plugin-contribution-requests";
import { createLintScheduler, type LintOutcome } from "@/services/plugin-linter-queue";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export type { LintOutcome } from "@/services/plugin-linter-queue";

export interface LintDocumentRequest {
  languageId: string;
  /** Base name; the host keeps only the base name anyway. */
  fileName: string;
  /** Clean project-relative path, when the document is inside a project. */
  relativePath?: string;
  text: string;
  trigger: LinterTrigger;
}

interface QueuedLint {
  linter: Pick<PluginLinter, "pluginId" | "id">;
  request: LintDocumentRequest;
}

/** Headroom for the JSON envelope around the document text. */
const PAYLOAD_ENVELOPE_BYTES = 4096;

async function send({ linter, request }: QueuedLint, signal: AbortSignal): Promise<LintOutcome> {
  if (signal.aborted) return { ok: false, error: { code: "cancelled", detail: "" } };
  const requestId = newContributionRequestId();
  const abort = () => void cancelContributionRequest(requestId);
  signal.addEventListener("abort", abort, { once: true });
  try {
    const response = await invokeBackend<unknown>("plugin_host_lint", {
      pluginId: linter.pluginId,
      linterId: linter.id,
      requestId,
      languageId: request.languageId,
      fileName: request.fileName,
      ...(request.relativePath ? { relativePath: request.relativePath } : {}),
      text: request.text,
      trigger: request.trigger,
    });
    if (signal.aborted) return { ok: false, error: { code: "cancelled", detail: "" } };
    return { ok: true, result: parseLintResult(response) };
  } catch (error) {
    return { ok: false, error: parseContributionError(error) };
  } finally {
    signal.removeEventListener("abort", abort);
  }
}

const scheduler = createLintScheduler<QueuedLint>(send);

/** Whether a document is small enough to lint (checked before queueing). */
export function lintPayloadFits(text: string): boolean {
  return utf8ByteLength(text) <= LINTER_LIMITS.payloadMaxBytes - PAYLOAD_ENVELOPE_BYTES;
}

/** Queues a lint of `modelKey`. Never throws. */
export function lintWithPlugin(linter: Pick<PluginLinter, "pluginId" | "id">, modelKey: string, request: LintDocumentRequest): Promise<LintOutcome> {
  if (!isDesktopRuntime()) return Promise.resolve({ ok: false, error: { code: "unavailable", detail: "desktop runtime required" } });
  if (!lintPayloadFits(request.text)) return Promise.resolve({ ok: false, error: { code: "too_large", detail: "document is too large to lint" } });
  return scheduler.schedule(linter.pluginId, modelKey, { linter, request });
}

/** Cancels the queued and running lints of one model. */
export function cancelModelLints(modelKey: string): void {
  scheduler.cancelModel(modelKey);
}

/** Cancels the queued and running lints of one plugin. */
export function cancelPluginLints(pluginId: string): void {
  scheduler.cancelPlugin(pluginId);
}
