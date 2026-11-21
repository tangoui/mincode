/**
 * Shared invoker for the on-demand provider contribution points
 * (`plugin_host_tree_view`, `plugin_host_language_feature`,
 * `plugin_host_test_discover`, `plugin_host_test_run`,
 * `plugin_host_ai_catalog_search`, `plugin_host_ai_catalog_install`,
 * `plugin_host_session_list`, `plugin_host_session_messages`,
 * `plugin_host_scm_commit_message`, `plugin_host_file_decorations`). A fresh request id is
 * attached to every call so an abort cancels the host request (the late
 * result is discarded); a `busy` answer is retried once after 150 ms. Never
 * throws.
 */
import {
  cancelContributionRequest,
  newContributionRequestId,
  parseContributionError,
  type ContributionError,
} from "@/services/plugin-contribution-requests";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export type ProviderCommand =
  | "plugin_host_tree_view"
  | "plugin_host_language_feature"
  | "plugin_host_test_discover"
  | "plugin_host_test_run"
  | "plugin_host_ai_catalog_search"
  | "plugin_host_ai_catalog_install"
  | "plugin_host_session_list"
  | "plugin_host_session_messages"
  | "plugin_host_scm_commit_message"
  | "plugin_host_file_decorations";

export type ProviderInvokeResult<T> = { ok: true; response: T } | { ok: false; error: ContributionError };

const BUSY_RETRY_MS = 150;
const CANCELLED = { ok: false, error: { code: "cancelled", detail: "" } } as const;

function wait(ms: number, signal?: AbortSignal) {
  return new Promise<void>((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => {
      clearTimeout(timer);
      resolve();
    }, { once: true });
  });
}

async function invokeOnce<T>(
  command: ProviderCommand,
  args: Record<string, unknown>,
  parse: (value: unknown) => T,
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<T>> {
  const requestId = newContributionRequestId();
  const abort = () => void cancelContributionRequest(requestId);
  signal?.addEventListener("abort", abort, { once: true });
  try {
    const response = await invokeBackend<unknown>(command, { ...args, requestId });
    if (signal?.aborted) return CANCELLED;
    return { ok: true, response: parse(response) };
  } catch (error) {
    return { ok: false, error: parseContributionError(error) };
  } finally {
    signal?.removeEventListener("abort", abort);
  }
}

/** Invokes a provider host command and re-validates the response with `parse`. */
export async function invokeProviderCommand<T>(
  command: ProviderCommand,
  args: Record<string, unknown>,
  parse: (value: unknown) => T,
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<T>> {
  if (!isDesktopRuntime()) return { ok: false, error: { code: "unavailable", detail: "desktop runtime required" } };
  if (signal?.aborted) return CANCELLED;
  const first = await invokeOnce(command, args, parse, signal);
  if (first.ok || first.error.code !== "busy" || signal?.aborted) return first;
  await wait(BUSY_RETRY_MS, signal);
  if (signal?.aborted) return CANCELLED;
  return invokeOnce(command, args, parse, signal);
}

/** A short single-line rendering of an untrusted plugin error for a toast. */
export function providerErrorText(error: ContributionError): string {
  // eslint-disable-next-line no-control-regex -- strip control characters from untrusted text
  const detail = error.detail.replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]+/g, " ").trim();
  const text = detail ? `${error.code}: ${detail}` : error.code;
  return text.length > 200 ? `${text.slice(0, 199)}…` : text;
}
