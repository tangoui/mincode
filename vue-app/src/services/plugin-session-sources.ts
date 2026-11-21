/**
 * Reads a plugin session source through the host
 * (`plugin_host_session_list` / `plugin_host_session_messages`,
 * backend/plugin/session_source_invoke.go). The sessions page calls
 * the list only when the user expands the source and the messages only when
 * the user opens a session. The host builds the fixed payloads, bounds and
 * validates the results; they are re-validated here
 * (domain/plugins/session-sources.ts). Results live in memory only.
 */
import {
  parseSessionListResponse,
  parseSessionMessagesResponse,
  type PluginSessionMessage,
  type PluginSessionSource,
  type PluginSessionSummary,
} from "@/domain/plugins/session-sources";
import { useVueI18n } from "@/i18n/messages";
import { invokeProviderCommand, type ProviderInvokeResult } from "@/services/plugin-provider-invoke";

export type PluginSessionScope = "workspace" | "all";

function currentLocale(): string {
  return useVueI18n().locale.value;
}

/**
 * Lists the sessions of a source. `projectRoot` is sent only for the
 * workspace scope (the host rejects a workspace request without one and
 * forwards the root only to plugins with workspace.read).
 */
export function loadPluginSessions(
  source: PluginSessionSource,
  scope: PluginSessionScope,
  projectRoot: string | undefined,
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<{ sessions: PluginSessionSummary[]; truncated: boolean }>> {
  return invokeProviderCommand(
    "plugin_host_session_list",
    {
      pluginId: source.pluginId,
      sourceId: source.id,
      scope,
      locale: currentLocale(),
      ...(scope === "workspace" && projectRoot ? { projectRoot } : {}),
    },
    (value) => {
      const { sessions, truncated } = parseSessionListResponse(value);
      return { sessions, truncated };
    },
    signal,
  );
}

/** Reads the transcript of one session; the request carries only the session id. */
export function loadPluginSessionMessages(
  source: PluginSessionSource,
  sessionId: string,
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<{ messages: PluginSessionMessage[]; truncated: boolean }>> {
  return invokeProviderCommand(
    "plugin_host_session_messages",
    { pluginId: source.pluginId, sourceId: source.id, sessionId, locale: currentLocale() },
    (value) => {
      const { messages, truncated } = parseSessionMessagesResponse(value);
      return { messages, truncated };
    },
    signal,
  );
}

/** The sources a page-wide refresh reloads: only the expanded ones. */
export function pluginSourcesToRefresh(
  sources: readonly PluginSessionSource[],
  open: Readonly<Record<string, boolean>>,
): PluginSessionSource[] {
  return sources.filter((source) => open[source.key] === true);
}
