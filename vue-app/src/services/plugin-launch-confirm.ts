// Shared first-run confirmation for plugin process launches (tasks,
// languageServers, linters, …). The host resolves the launch
// (plugin_process_launch.go) and refuses to start it until the user confirmed
// this exact fingerprint; this dialog only shows the resolved spec. It is
// required in every approval mode. terminalProfiles keeps its own dialog.
import { h } from "vue";
import { ElMessageBox } from "element-plus";
import PluginLaunchConfirmContent from "@/components/plugins/PluginLaunchConfirmContent.vue";
import { useVueI18n } from "@/i18n/messages";

export interface ProcessLaunchFile {
  relative: string;
  sha256: string;
}

/** JSON shape of the Go ResolvedProcessLaunch. */
export interface ResolvedProcessLaunch {
  pluginId: string;
  pluginName: string;
  pluginVersion: string;
  point: string;
  entryKey: string;
  title: string;
  /** Per-language titles (Go map[string]string), resolved for display by processLaunchTitle. */
  titleLocalized?: Record<string, string>;
  argv: string[];
  env: Record<string, string>;
  cwd: string;
  pluginExecutable: boolean;
  executableSha256?: string;
  pluginFiles?: ProcessLaunchFile[];
  fingerprint: string;
  trusted: boolean;
  previouslyTrusted: boolean;
}

/**
 * Asks the user to run launch. Cancel keeps the initial focus. Resolves true
 * only when the user chose Run; the caller then sends launch.fingerprint to
 * the host's trust call. notices are listed below the command (what the
 * process receives besides its launch, e.g. an ACP agent's environment and
 * connectors).
 */
export async function confirmPluginLaunch(
  launch: ResolvedProcessLaunch,
  { changed = launch.previouslyTrusted, notices }: { changed?: boolean; notices?: string[] } = {},
): Promise<boolean> {
  const { t } = useVueI18n();
  try {
    await ElMessageBox.confirm(
      h(PluginLaunchConfirmContent, { launch, changed, notices }),
      t("pluginContrib.processLaunch.confirmTitle"),
      {
        type: "warning",
        confirmButtonText: t("pluginContrib.processLaunch.confirmRun"),
        cancelButtonText: t("common.cancel"),
        distinguishCancelAndClose: true,
        autofocus: false,
        customClass: "app-plugin-launch-confirm",
      },
    );
    return true;
  } catch {
    return false;
  }
}
