/**
 * Fetches plugin context through the host (`plugin_host_ai_context`). The
 * host checks the provider still exists and its argument rule, runs the
 * plugin's `read`-risk action with a 15 s timeout and a bounded result, and
 * returns text with hidden/control characters removed. Called only from the
 * AI composer's preview dialog after the user picked an @mention; never from
 * AI tools.
 */
import { AI_CONTEXT_PROVIDER_LIMITS, type PluginAiContextProvider } from "@/domain/ai/plugin-context";
import {
  cancelContributionRequest,
  newContributionRequestId,
  parseContributionError,
  type ContributionError,
} from "@/services/plugin-contribution-requests";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export interface PluginContextResult {
  text: string;
  title?: string;
  truncated: boolean;
  removedCharacters: number;
}

export type PluginContextOutcome = { ok: true; result: PluginContextResult } | { ok: false; error: ContributionError };

function parseResult(raw: unknown): PluginContextResult {
  const value = typeof raw === "object" && raw !== null ? (raw as Record<string, unknown>) : {};
  const text = typeof value.text === "string" ? [...value.text].slice(0, AI_CONTEXT_PROVIDER_LIMITS.resultMaxChars).join("") : "";
  const title = typeof value.title === "string" && value.title.trim() ? [...value.title.trim()].slice(0, AI_CONTEXT_PROVIDER_LIMITS.resultTitleMaxRunes).join("") : undefined;
  const removed = typeof value.removedCharacters === "number" && Number.isInteger(value.removedCharacters) && value.removedCharacters > 0 ? value.removedCharacters : 0;
  return { text, ...(title ? { title } : {}), truncated: value.truncated === true, removedCharacters: removed };
}

/** Fetches one provider's context. Never throws; an aborted signal cancels the host request. */
export async function fetchPluginContext(
  provider: Pick<PluginAiContextProvider, "pluginId" | "id">,
  argument: string | undefined,
  signal?: AbortSignal,
  locale?: string,
): Promise<PluginContextOutcome> {
  if (!isDesktopRuntime()) return { ok: false, error: { code: "unavailable", detail: "desktop runtime required" } };
  if (signal?.aborted) return { ok: false, error: { code: "cancelled", detail: "" } };
  const requestId = newContributionRequestId();
  const abort = () => void cancelContributionRequest(requestId);
  signal?.addEventListener("abort", abort, { once: true });
  try {
    const trimmed = argument?.trim();
    const response = await invokeBackend<unknown>("plugin_host_ai_context", {
      pluginId: provider.pluginId,
      providerId: provider.id,
      requestId,
      ...(trimmed ? { argument: trimmed } : {}),
      ...(locale && /^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8}){0,3}$/.test(locale) ? { locale } : {}),
    });
    if (signal?.aborted) return { ok: false, error: { code: "cancelled", detail: "" } };
    return { ok: true, result: parseResult(response) };
  } catch (error) {
    return { ok: false, error: parseContributionError(error) };
  } finally {
    signal?.removeEventListener("abort", abort);
  }
}
