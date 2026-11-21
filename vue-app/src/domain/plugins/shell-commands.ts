/**
 * Running a plugin command from outside the workbench chrome: a notification
 * action button or a tray menu item. Effects are injected
 * (services/plugin-shell-commands.ts wires the plugin command runtime, i18n
 * and Element Plus; tests pass stubs). Same split as the CodeLens click path
 * (domain/plugins/code-lens-commands.ts), whose post-confirmation comparison
 * this module reuses.
 *
 * The command is looked up again in the current plugin command table (the
 * plugin may have been updated, disabled or uninstalled since the button or
 * menu item appeared). Whether to confirm depends on the origin and the
 * command's effective risk (read < network < write < dangerous; missing or
 * unknown counts as dangerous):
 * - notification: anything that is not read confirms — the button text is
 *   the plugin's runtime choice and may disguise the command;
 * - tray: only dangerous confirms — the label is the manifest title in a
 *   submenu named after the plugin, but the click happens outside the
 *   workbench, often with the window hidden.
 * View-target commands only open a tab and never confirm. The dialog names
 * the plugin and the command's own manifest title, never the button text.
 * After the confirmation the command is looked up again and must be
 * unchanged.
 *
 * Full trust: before the dialog the host is asked (deps.trusted); a fully
 * trusted plugin's command skips only the dialog — the command is still
 * looked up again and must be unchanged. A trust check that fails or answers
 * anything but true shows the dialog.
 */
import { sameConfirmedCommand, trustedWithoutPrompt } from "@/domain/plugins/code-lens-commands";
import { resolvePluginTitle, type PluginTitle } from "@/domain/plugins/title";

export type ShellCommandOrigin = "notification" | "tray";

/** A registered plugin command as this path needs it (plugin-service PluginCommand). */
export interface ShellRunnableCommand extends PluginTitle {
  pluginId: string;
  pluginName: string;
  id: string;
  action?: string;
  target?: unknown;
  effectiveRisk?: string;
}

/** The host messages of the confirmation (vue-app/src/i18n/locales). */
export type ShellMessageKey =
  | "pluginContrib.shell.confirmTitle"
  | "pluginContrib.shell.confirmWrite"
  | "pluginContrib.shell.confirmDangerous"
  | "pluginContrib.shell.originNotification"
  | "pluginContrib.shell.originTray";

export interface ShellConfirmRequest {
  title: string;
  message: string;
  dangerous: boolean;
}

export interface ShellCommandDeps {
  commands(): readonly ShellRunnableCommand[];
  locale(): string | readonly string[];
  t(key: ShellMessageKey, params?: Record<string, string>): string;
  /** Resolves true only when the user chose to run. */
  confirm(request: ShellConfirmRequest): Promise<boolean>;
  /** True only when the host lets this plugin skip the dialog (full trust). */
  trusted(pluginId: string, origin: ShellCommandOrigin, command: ShellRunnableCommand): Promise<boolean>;
  execute(pluginId: string, command: string): Promise<void>;
}

/** Why a shell command did not run: gone / not runnable, or changed while the confirmation was open. */
export class ShellCommandError extends Error {
  readonly reason: "unavailable" | "changed";

  constructor(reason: "unavailable" | "changed", message: string) {
    super(message);
    this.name = "ShellCommandError";
    this.reason = reason;
  }
}

const RISK_RANKS: Record<string, 0 | 1 | 2 | 3> = { read: 0, network: 1, write: 2, dangerous: 3 };
const ORIGIN_THRESHOLDS: Record<ShellCommandOrigin, 1 | 3> = { notification: 1, tray: 3 };
const ORIGIN_KEYS: Record<ShellCommandOrigin, ShellMessageKey> = {
  notification: "pluginContrib.shell.originNotification",
  tray: "pluginContrib.shell.originTray",
};

/** read 0, network 1, write 2, dangerous (and missing or unknown) 3. */
export function riskRank(risk: string | undefined): 0 | 1 | 2 | 3 {
  return risk !== undefined && Object.prototype.hasOwnProperty.call(RISK_RANKS, risk) ? RISK_RANKS[risk] : 3;
}

function isViewTarget(target: unknown): boolean {
  return typeof target === "object" && target !== null && (target as { type?: unknown }).type === "view";
}

/** Whether running the command from this origin needs a host confirmation first. */
export function shellCommandNeedsConfirm(origin: ShellCommandOrigin, command: { target?: unknown; effectiveRisk?: string }): boolean {
  // Only a view target (opens a tab) skips the risk decision; null or any other target shape does not.
  if (isViewTarget(command.target)) return false;
  return riskRank(command.effectiveRisk) >= ORIGIN_THRESHOLDS[origin];
}

function confirmRequest(origin: ShellCommandOrigin, command: ShellRunnableCommand, deps: ShellCommandDeps): ShellConfirmRequest {
  const dangerous = riskRank(command.effectiveRisk) === 3;
  const params = {
    plugin: command.pluginName || command.pluginId,
    command: resolvePluginTitle(command, deps.locale()),
    origin: deps.t(ORIGIN_KEYS[origin]),
  };
  return {
    title: deps.t("pluginContrib.shell.confirmTitle"),
    message: deps.t(dangerous ? "pluginContrib.shell.confirmDangerous" : "pluginContrib.shell.confirmWrite", params),
    dangerous,
  };
}

export function createShellCommandRunner(deps: ShellCommandDeps) {
  const current = (pluginId: string, command: string) =>
    deps.commands().find((candidate) => candidate.pluginId === pluginId && candidate.id === command);
  return {
    async run(origin: ShellCommandOrigin, pluginId: string, command: string): Promise<"ran" | "cancelled"> {
      const target = current(pluginId, command);
      if (!target) throw new ShellCommandError("unavailable", `Plugin command ${pluginId}:${command} is not available`);
      if (shellCommandNeedsConfirm(origin, target)) {
        const ok = (await trustedWithoutPrompt(() => deps.trusted(pluginId, origin, target)))
          || (await deps.confirm(confirmRequest(origin, target, deps)));
        if (!ok) return "cancelled";
        const after = current(pluginId, command);
        if (!after || !sameConfirmedCommand(target, after, deps.locale())) {
          throw new ShellCommandError("changed", `Plugin command ${pluginId}:${command} changed while waiting for confirmation`);
        }
      }
      await deps.execute(pluginId, command);
      return "ran";
    },
  };
}
