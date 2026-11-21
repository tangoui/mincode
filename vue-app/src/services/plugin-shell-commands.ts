/**
 * Runs the plugin command behind a notification action button or a tray
 * menu item and reports one that did not run. Loaded on demand
 * (NotificationCenterButton.vue on click, App.vue on a tray navigation), so
 * the entry chunk carries neither Element Plus dialogs nor the command
 * runtime.
 *
 * The rules live in domain/plugins/shell-commands.ts: the command is looked
 * up again; notifications confirm anything that is not read, the tray only
 * dangerous commands; the dialog names the plugin and the manifest title.
 * executePluginCommand then re-checks registration and `when`, opens view
 * targets and the parameter form when the command has an inputSchema.
 *
 * Full trust: a fully trusted plugin is let through by the host
 * (plugin_full_trust_authorize) and still re-checked afterwards.
 */
import { ElMessage, ElMessageBox } from "element-plus";
import { createShellCommandRunner, ShellCommandError, type ShellCommandOrigin } from "@/domain/plugins/shell-commands";
import { useVueI18n } from "@/i18n/messages";
import { authorizeWithoutPrompt } from "@/services/plugin-full-trust";
import { executePluginCommand, pluginCommandState } from "@/services/plugin-commands";
import { providerErrorText } from "@/services/plugin-provider-invoke";

const runner = createShellCommandRunner({
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
  trusted: (pluginId, origin, command) => authorizeWithoutPrompt(pluginId, origin, command.effectiveRisk, command.id),
  execute: (pluginId, command) => executePluginCommand(pluginId, command),
});

/** Runs the command; a failure becomes a grouped warning toast, never a rejection. */
export async function runShellCommand(origin: ShellCommandOrigin, pluginId: string, command: string): Promise<void> {
  try {
    await runner.run(origin, pluginId, command);
  } catch (error) {
    const { t } = useVueI18n();
    const message = error instanceof ShellCommandError && error.reason === "changed"
      ? t("pluginContrib.shell.changed")
      : t("pluginContrib.shell.unavailable", {
        // Plugin errors are untrusted: one sanitized, bounded line.
        error: providerErrorText({ code: "unknown", detail: error instanceof Error ? error.message : String(error) }),
      });
    ElMessage({ type: "warning", grouping: true, message });
  }
}
