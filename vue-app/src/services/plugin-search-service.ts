/**
 * Invokes a plugin search provider through the host (`plugin_host_search`).
 * The host checks the provider still exists, builds the fixed payload
 * {provider, query, locale} (plus project:{name, root} for a plugin that
 * negotiated `searchFileTargets` while a workspace scope is ready), runs the
 * plugin's `read`-risk action with a 5 s timeout and bounded payload/result,
 * and returns validated plain-text items, which are re-validated here. File
 * items survive only with the response's host-filled fileScope and
 * providerRuntime; they open through services/plugin-search-file-targets.ts.
 * An abort cancels the host request (the late result is discarded); the host
 * also cancels a file-enabled query when the workspace scope changes. A
 * `busy` answer is retried once after 150 ms.
 */
import { parseSearchResultResponse, type PluginSearchProvider, type SearchResultResponse } from "@/domain/plugins/search-providers";
import {
  cancelContributionRequest,
  newContributionRequestId,
  parseContributionError,
  type ContributionError,
} from "@/services/plugin-contribution-requests";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export type PluginSearchResult =
  | { ok: true; response: SearchResultResponse }
  | { ok: false; error: ContributionError };

const BUSY_RETRY_MS = 150;

function wait(ms: number, signal?: AbortSignal) {
  return new Promise<void>((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => {
      clearTimeout(timer);
      resolve();
    }, { once: true });
  });
}

async function searchOnce(
  provider: Pick<PluginSearchProvider, "pluginId" | "id">,
  query: string,
  locale: string,
  signal?: AbortSignal,
): Promise<PluginSearchResult> {
  const requestId = newContributionRequestId();
  const abort = () => void cancelContributionRequest(requestId);
  signal?.addEventListener("abort", abort, { once: true });
  try {
    const response = await invokeBackend<unknown>("plugin_host_search", {
      pluginId: provider.pluginId,
      providerId: provider.id,
      requestId,
      query,
      locale,
    });
    if (signal?.aborted) return { ok: false, error: { code: "cancelled", detail: "" } };
    return { ok: true, response: parseSearchResultResponse(response) };
  } catch (error) {
    return { ok: false, error: parseContributionError(error) };
  } finally {
    signal?.removeEventListener("abort", abort);
  }
}

/** Runs one provider query. Never throws; `signal` aborts the request. */
export async function searchWithPlugin(
  provider: Pick<PluginSearchProvider, "pluginId" | "id">,
  query: string,
  locale: string,
  signal?: AbortSignal,
): Promise<PluginSearchResult> {
  if (!isDesktopRuntime()) return { ok: false, error: { code: "unavailable", detail: "desktop runtime required" } };
  if (signal?.aborted) return { ok: false, error: { code: "cancelled", detail: "" } };
  const first = await searchOnce(provider, query, locale, signal);
  if (first.ok || first.error.code !== "busy" || signal?.aborted) return first;
  await wait(BUSY_RETRY_MS, signal);
  if (signal?.aborted) return { ok: false, error: { code: "cancelled", detail: "" } };
  return searchOnce(provider, query, locale, signal);
}
