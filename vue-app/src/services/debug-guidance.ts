/**
 * Guidance when a debugger plugin or one of its tools is missing.
 *
 *  - `ensureDebuggerFor`: a language without an installed debugger offers the
 *    plugin manager on its official debugger plugin.
 *  - `checkBeforeLaunch`: the debugger's check action runs before every
 *    user-initiated launch (Run / Debug from the toolbar, the entry-point menu,
 *    keybindings and Restart all go through `launchDebugSession`, which calls
 *    `guardDebugLaunch`). Never on file open: on macOS, probing the
 *    `/usr/bin/python3` stub pops the system "install developer tools" dialog.
 *    Missing tools are listed with their install commands; a command runs in
 *    a new, visible terminal tab only after the user confirms it.
 *  - `launchConfigurationFor`: Run / Debug in a project without run
 *    configurations creates a "Current File" one from the debugger of the
 *    active file's language.
 */
import { h } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import router from "@/router";
import { configurationPayload, newConfigurationFromSnippet } from "@/domain/debug/configurations";
import type { DebuggerInfo, DebuggerMissingTool, RunConfiguration } from "@/domain/debug/types";
import { visibleTerminalText } from "@/domain/terminal/launch-display";
import { useVueI18n } from "@/i18n/messages";
import { checkDebugger, listDebuggers } from "@/services/debug-service";
import { useDebugConfigsStore } from "@/stores/debug-configs";

/** Monaco language id → id of MinCode's official debugger plugin for it. */
export const OFFICIAL_DEBUGGER_PLUGINS: Record<string, string> = {
  python: "python-debug",
  java: "java-debug",
  go: "go-debug",
  php: "php-debug",
};

/** Display names of the languages above (proper nouns, not translated). */
const LANGUAGE_NAMES: Record<string, string> = { python: "Python", java: "Java", go: "Go", php: "PHP" };

/** Opens a new terminal tab that runs command (the workspace's own terminal tabs). */
export type DebugTerminalOpener = (request: { command: string; cwd: string; title: string }) => void;

let terminalOpener: DebugTerminalOpener | null = null;

/** Registers where install commands run (the active project workspace); returns the disposer. */
export function setDebugTerminalOpener(opener: DebugTerminalOpener): () => void {
  terminalOpener = opener;
  return () => {
    if (terminalOpener === opener) terminalOpener = null;
  };
}

/** Asks to open the plugin manager on pluginId; navigates when the user agrees. */
async function offerPluginInstall(message: string, pluginId: string): Promise<void> {
  const { t } = useVueI18n();
  try {
    await ElMessageBox.confirm(message, t("debug.guidance.pluginTitle"), {
      type: "info",
      confirmButtonText: t("debug.guidance.openPlugins"),
      cancelButtonText: t("common.cancel"),
      distinguishCancelAndClose: true,
    });
  } catch {
    return;
  }
  await router.push({ name: "plugins", query: { search: pluginId } });
}

/**
 * The first installed debugger for languageId; "unsupported" when none is
 * installed and no official plugin covers the language. A language with an
 * official debugger plugin offers the plugin manager on it, and a failed
 * listing shows the error; both resolve null. Never runs a check.
 */
async function lookupDebugger(languageId: string): Promise<DebuggerInfo | "unsupported" | null> {
  const { t } = useVueI18n();
  let debuggers: DebuggerInfo[];
  try {
    debuggers = await listDebuggers();
  } catch (error) {
    const reason = error instanceof Error ? error.message : String(error);
    ElMessage.error({ message: t("debug.guidance.listFailed", { error: reason }), showClose: true, grouping: true });
    return null;
  }
  const debugger_ = debuggers.find((item) => item.languages.includes(languageId));
  if (debugger_) return debugger_;
  const pluginId = OFFICIAL_DEBUGGER_PLUGINS[languageId];
  if (!pluginId) return "unsupported";
  await offerPluginInstall(t("debug.guidance.needPlugin", { language: LANGUAGE_NAMES[languageId] ?? languageId }), pluginId);
  return null;
}

/**
 * The first installed debugger for languageId. Without one, a language with
 * an official debugger plugin offers the plugin manager on it, any other
 * language gets an "unsupported" notice, and a failed debugger listing shows
 * the error; all resolve null. Never runs the debugger's check.
 */
export async function ensureDebuggerFor(languageId: string, _root: string): Promise<DebuggerInfo | null> {
  const result = await lookupDebugger(languageId);
  if (result !== "unsupported") return result;
  const { t } = useVueI18n();
  ElMessage.info({ message: t("debug.guidance.unsupported", { language: languageId }), showClose: true, grouping: true });
  return null;
}

/** A missing tool as the dialog shows it; only `runnable` install commands may run. */
export interface GuidedMissingTool extends DebuggerMissingTool {
  /** installCommand displays exactly as it runs (no control, bidi or zero-width characters). */
  runnable: boolean;
}

/** Validated missing tools: a command that would display differently than it runs is shown (escaped) but not runnable. */
function missingTools(raw: unknown): GuidedMissingTool[] {
  if (!Array.isArray(raw)) return [];
  const tools: GuidedMissingTool[] = [];
  for (const item of raw as unknown[]) {
    if (typeof item !== "object" || item === null) continue;
    const { name, hint, installCommand } = item as Record<string, unknown>;
    if (typeof name !== "string" || !name.trim()) continue;
    const tool: GuidedMissingTool = { name, hint: typeof hint === "string" ? hint : "", runnable: false };
    const command = typeof installCommand === "string" ? installCommand.trim() : "";
    if (command) {
      tool.installCommand = command;
      tool.runnable = visibleTerminalText(command) === command;
    }
    tools.push(tool);
  }
  return tools;
}

/** Shows the missing tools; resolves true when the user chose to run the install commands. */
async function confirmMissingTools(debugger_: DebuggerInfo, missing: GuidedMissingTool[]): Promise<boolean> {
  const { t } = useVueI18n();
  const { default: DebugMissingToolsDialog } = await import("@/components/debug/DebugMissingToolsDialog.vue");
  const runnable = missing.some((tool) => tool.runnable);
  try {
    await ElMessageBox.confirm(
      h(DebugMissingToolsDialog, { debuggerName: debugger_.pluginName || debugger_.title, missing }),
      t("debug.guidance.missingTitle"),
      {
        type: "warning",
        showConfirmButton: runnable,
        confirmButtonText: t("debug.guidance.runInTerminal"),
        cancelButtonText: runnable ? t("common.cancel") : t("common.close"),
        distinguishCancelAndClose: true,
        autofocus: false,
        customClass: "app-debug-missing-tools",
      },
    );
    return runnable;
  } catch {
    return false;
  }
}

function runInTerminal(tool: DebuggerMissingTool & { installCommand: string }, cwd: string) {
  if (terminalOpener) {
    terminalOpener({ command: tool.installCommand, cwd, title: visibleTerminalText(tool.name) });
    return;
  }
  const { t } = useVueI18n();
  ElMessage.warning({ message: t("debug.guidance.noTerminal", { command: tool.installCommand }), showClose: true });
}

/**
 * Runs the debugger's check for cfg (only call this for an explicit Run /
 * Debug). Resolves true when the launch may go on: the debugger has no
 * check, the tools are ready, or the check itself failed (prepare and start
 * report real failures). Missing tools show the dialog and resolve false;
 * their install commands run, each in a new terminal tab, only after the
 * user confirms. Throws Error("debug.config.noActiveFile") like the launch.
 */
export async function checkBeforeLaunch(
  debugger_: DebuggerInfo,
  root: string,
  cfg: RunConfiguration,
  activeFile?: string,
): Promise<boolean> {
  if (!debugger_.hasCheck) return true;
  const configuration = configurationPayload(cfg, { projectRoot: root, file: activeFile });
  let result;
  try {
    result = await checkDebugger({ pluginId: debugger_.pluginId, type: debugger_.type, projectRoot: root, configuration });
  } catch {
    return true;
  }
  if (result.ready) return true;
  const missing = missingTools(result.missing);
  if (await confirmMissingTools(debugger_, missing)) {
    for (const tool of missing) {
      if (tool.runnable && tool.installCommand) runInTerminal({ ...tool, installCommand: tool.installCommand }, root);
    }
  }
  return false;
}

/** Whether the launch goes on, with the configuration's debugger when the listing succeeded. */
export type DebugLaunchGuard = { proceed: false } | { proceed: true; debugger?: DebuggerInfo };

/**
 * What every launch runs before prepare: a configuration whose debugger is
 * not installed offers its plugin; otherwise the debugger's check. A failed
 * debugger listing lets the launch go on (prepare reports it).
 */
export async function guardDebugLaunch(
  root: string,
  cfg: RunConfiguration,
  activeFile?: string,
  /** Called with the configuration's debugger as soon as it is found, before its tools are checked. */
  onDebugger?: (debugger_: DebuggerInfo) => void,
): Promise<DebugLaunchGuard> {
  let debuggers: DebuggerInfo[];
  try {
    debuggers = await listDebuggers();
  } catch {
    return { proceed: true };
  }
  const debugger_ = debuggers.find((item) => item.pluginId === cfg.pluginId && item.type === cfg.type);
  if (!debugger_) {
    const { t } = useVueI18n();
    await offerPluginInstall(t("debug.guidance.configPluginMissing", { name: cfg.name, plugin: cfg.pluginId }), cfg.pluginId);
    return { proceed: false };
  }
  onDebugger?.(debugger_);
  return (await checkBeforeLaunch(debugger_, root, cfg, activeFile)) ? { proceed: true, debugger: debugger_ } : { proceed: false };
}

/**
 * The configuration Run / Debug starts in root: the selected (or first) one.
 * A project without configurations gets one from the first launch snippet of
 * languageId's debugger (named "Current File" when it runs `${file}`), saved
 * and selected. Resolves "new" when the caller should ask for a new
 * configuration (no language, a language without any debugger, or no launch
 * snippet), null when the guidance handled it (official plugin not
 * installed, or the debugger listing failed).
 */
export async function launchConfigurationFor(root: string, languageId: string | undefined): Promise<RunConfiguration | "new" | null> {
  const configs = useDebugConfigsStore();
  await configs.load().catch(() => undefined);
  const state = configs.stateFor(root);
  const selected = state.configs.find((item) => item.id === state.selectedConfigId) ?? state.configs[0];
  if (selected) return selected;
  if (!languageId) return "new";
  const debugger_ = await lookupDebugger(languageId);
  if (debugger_ === "unsupported") return "new";
  if (!debugger_) return null;
  const snippet = debugger_.configurationSnippets.find((item) => item.request === "launch");
  if (!snippet) return "new";
  const { t } = useVueI18n();
  const usesFile = JSON.stringify(snippet.body).includes("${file}");
  const cfg = newConfigurationFromSnippet(debugger_, snippet, usesFile ? t("debug.config.currentFile") : undefined);
  configs.upsertConfig(root, cfg);
  configs.selectConfig(root, cfg.id);
  return cfg;
}
