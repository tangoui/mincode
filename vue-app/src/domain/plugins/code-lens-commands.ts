/**
 * The click path of a plugin CodeLens command, with its effects injected
 * (services/plugin-code-lens-commands.ts wires the plugin command runtime,
 * i18n and Element Plus; tests pass stubs).
 *
 * The command is looked up again in the current plugin command table (the
 * plugin may have been reloaded, updated or disabled since the CodeLens was
 * shown) and re-checked with codeLensCommandAccepts. Risk means the
 * effective risk (the higher of the command's and its action's risk). A read
 * command may take the CodeLens args (re-validated against its current
 * inputSchema). Any other command runs without args and only after the user
 * confirmed it in a host dialog naming the plugin and the command's own
 * manifest title — never the CodeLens title, which the plugin controls and
 * which renders inline.
 *
 * Full trust: before the dialog the host is asked (deps.trusted); a fully
 * trusted plugin's command skips only the dialog — it still runs without
 * args, is looked up again and must be unchanged. A trust check that fails
 * or answers anything but true shows the dialog.
 */
import { codeLensCommandAccepts, type CodeLensCommandShape } from "@/domain/plugins/language-feature-results";
import { resolvePluginTitle, type PluginTitle } from "@/domain/plugins/title";

type CommandArgs = Record<string, string | number | boolean>;

/** A registered plugin command as the click path needs it (plugin-service PluginCommand). */
export interface CodeLensRunnableCommand extends CodeLensCommandShape, PluginTitle {
  pluginId: string;
  pluginName: string;
  id: string;
}

export interface CodeLensConfirmRequest {
  title: string;
  message: string;
  dangerous: boolean;
}

/** The host messages of the confirmation (vue-app/src/i18n/locales). */
export type CodeLensMessageKey =
  | "pluginContrib.languageFeatures.codeLensConfirmTitle"
  | "pluginContrib.languageFeatures.codeLensConfirmWrite"
  | "pluginContrib.languageFeatures.codeLensConfirmDangerous";

export interface CodeLensCommandDeps {
  commands(): readonly CodeLensRunnableCommand[];
  locale(): string | readonly string[];
  t(key: CodeLensMessageKey, params?: Record<string, string>): string;
  /** Resolves true only when the user chose to run. */
  confirm(request: CodeLensConfirmRequest): Promise<boolean>;
  /** True only when the host lets this plugin skip the dialog (full trust). */
  trusted(pluginId: string, command: CodeLensRunnableCommand): Promise<boolean>;
  execute(pluginId: string, command: string): Promise<void>;
  executeWithArgs(pluginId: string, command: string, args: CommandArgs): Promise<void>;
}

/**
 * The confirmation of a command whose effective risk is not "read":
 * plugin name and manifest command title; dangerous commands get the
 * stronger wording.
 */
export function codeLensConfirmRequest(command: CodeLensRunnableCommand, deps: Pick<CodeLensCommandDeps, "locale" | "t">): CodeLensConfirmRequest {
  const dangerous = command.effectiveRisk === "dangerous";
  const params = { plugin: command.pluginName || command.pluginId, command: resolvePluginTitle(command, deps.locale()) };
  return {
    title: deps.t("pluginContrib.languageFeatures.codeLensConfirmTitle"),
    message: deps.t(dangerous ? "pluginContrib.languageFeatures.codeLensConfirmDangerous" : "pluginContrib.languageFeatures.codeLensConfirmWrite", params),
    dangerous,
  };
}

/** The fields of a plugin command that a confirmation vouched for. */
export interface ConfirmedCommandShape extends PluginTitle {
  action?: string;
  target?: unknown;
  effectiveRisk?: string;
}

/**
 * Whether the command the user confirmed is still the same: action, target,
 * title and effective risk. Shared by the CodeLens click path and the shell
 * commands of notifications and the tray (domain/plugins/shell-commands.ts).
 */
export function sameConfirmedCommand(before: ConfirmedCommandShape, after: ConfirmedCommandShape, locale: string | readonly string[]): boolean {
  return before.action === after.action
    && before.target === after.target
    && before.effectiveRisk === after.effectiveRisk
    && before.title === after.title
    && resolvePluginTitle(before, locale) === resolvePluginTitle(after, locale);
}

/**
 * Fails closed: only a resolved `true` skips the dialog. Shared with
 * domain/plugins/shell-commands.ts.
 */
export async function trustedWithoutPrompt(check: () => Promise<boolean>): Promise<boolean> {
  try {
    return (await check()) === true;
  } catch {
    return false;
  }
}

export function createCodeLensCommandRunner(deps: CodeLensCommandDeps) {
  const current = (pluginId: string, command: string) =>
    deps.commands().find((candidate) => candidate.pluginId === pluginId && candidate.id === command);
  return {
    /** A read command with args that still satisfy its current inputSchema. */
    async withArgs(pluginId: string, command: string, args: CommandArgs): Promise<void> {
      if (!codeLensCommandAccepts(current(pluginId, command), args)) {
        throw new Error(`Plugin command ${pluginId}:${command} does not accept these arguments`);
      }
      await deps.executeWithArgs(pluginId, command, args);
    },
    /**
     * Any action command without args; one whose effective risk is not
     * "read" only after the user confirmed (or the host allowed it under
     * full trust), and only if the command is still the one the user
     * confirmed (the plugin may update meanwhile).
     */
    async plain(pluginId: string, command: string): Promise<void> {
      const target = current(pluginId, command);
      if (!target || !codeLensCommandAccepts(target)) throw new Error(`Plugin command ${pluginId}:${command} is not available`);
      if (target.effectiveRisk !== "read") {
        const ok = (await trustedWithoutPrompt(() => deps.trusted(pluginId, target)))
          || (await deps.confirm(codeLensConfirmRequest(target, deps)));
        if (!ok) return;
        const after = current(pluginId, command);
        if (!after || !codeLensCommandAccepts(after) || !sameConfirmedCommand(target, after, deps.locale())) {
          throw new Error(`Plugin command ${pluginId}:${command} changed while waiting for confirmation; the CodeLens is out of date`);
        }
      }
      await deps.execute(pluginId, command);
    },
  };
}
