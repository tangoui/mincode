/**
 * Runs the plugin command behind a clicked plugin CodeLens and reports a
 * click that did not run. Loaded on demand by the Monaco stage
 * (services/monaco-language-features.ts) on the first click, so neither the
 * entry chunk nor that stage carries Element Plus, the confirmation dialog or
 * the command runtime.
 *
 * The rules live in domain/plugins/code-lens-commands.ts: the command is
 * looked up again and re-checked; read commands may take the CodeLens args;
 * write / dangerous commands run without args and only after a host
 * confirmation that names the plugin and the command's manifest title.
 * executePluginCommand* then re-check registration and `when`, and open the
 * parameter form when the command has an inputSchema.
 *
 * Full trust: a fully trusted plugin is let through by the host
 * (plugin_full_trust_authorize) and still re-checked afterwards.
 */
import { ElMessage, ElMessageBox } from "element-plus";
import { createCodeLensCommandRunner } from "@/domain/plugins/code-lens-commands";
import { useVueI18n } from "@/i18n/messages";
import { authorizeWithoutPrompt } from "@/services/plugin-full-trust";
import { executePluginCommand, executePluginCommandWithArgs, pluginCommandState } from "@/services/plugin-commands";
import { providerErrorText } from "@/services/plugin-provider-invoke";

const runner = createCodeLensCommandRunner({
  commands: () => pluginCommandState.value.commands,
  locale: () => useVueI18n().localeChain.value,
  t: (key, params) => (params ? useVueI18n().t(key, params) : useVueI18n().t(key)),
  async confirm({ title, message, dangerous }) {
    const { t } = useVueI18n();
    try {
      await ElMessageBox.confirm(message, title, {
        type: "warning",
        confirmButtonText: t("common.confirm"),
        cancelButtonText: t("common.cancel"),
        distinguishCancelAndClose: true,
        // A high-risk command keeps the initial focus off the confirm button.
        autofocus: !dangerous,
      });
      return true;
    } catch {
      return false;
    }
  },
  trusted: (pluginId, command) => authorizeWithoutPrompt(pluginId, "codeLens", command.effectiveRisk, command.id),
  execute: (pluginId, command) => executePluginCommand(pluginId, command),
  executeWithArgs: (pluginId, command, args) => executePluginCommandWithArgs(pluginId, command, { args }),
});

/** A read command with args that still satisfy its current inputSchema. */
export const runCodeLensCommandWithArgs = runner.withArgs;

/** Any action command (not a view target), without args; write / dangerous ones confirm first. */
export const runCodeLensCommand = runner.plain;

/** A gentle warning toast (grouped, so repeated clicks do not stack). */
export function showCodeLensFailure(message: string): void {
  const error = providerErrorText({ code: "unknown", detail: message });
  ElMessage({ type: "warning", grouping: true, message: useVueI18n().t("pluginContrib.languageFeatures.codeLensFailed", { error }) });
}
