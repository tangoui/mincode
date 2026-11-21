/**
 * Asks a plugin commit message provider for a draft through the host
 * (`plugin_host_scm_commit_message`,
 * backend/plugin/scm_commit_message_invoke.go). Only the user's
 * click in the Git commit box calls it. The request names the provider, the
 * open project and the repository; the host reads the staged changes itself
 * (sensitive files excluded) and the draft never leaves the WebView. The
 * answer is re-validated here (domain/plugins/scm-commit-message.ts) and is
 * only ever used as draft text, never committed.
 */
import {
  parseScmCommitMessageResponse,
  type PluginScmProvider,
  type ScmCommitMessageResult,
} from "@/domain/plugins/scm-commit-message";
import { useVueI18n } from "@/i18n/messages";
import { invokeProviderCommand, type ProviderInvokeResult } from "@/services/plugin-provider-invoke";

export function generatePluginCommitMessage(
  provider: PluginScmProvider,
  projectRoot: string,
  repoPath: string,
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<ScmCommitMessageResult>> {
  return invokeProviderCommand(
    "plugin_host_scm_commit_message",
    { pluginId: provider.pluginId, providerId: provider.id, projectRoot, repoPath, locale: useVueI18n().locale.value },
    (value) => {
      const result = parseScmCommitMessageResponse(value);
      if (!result) throw new Error("invalid_result: empty message");
      return result;
    },
    signal,
  );
}
