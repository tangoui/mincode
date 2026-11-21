/**
 * The debugger's keybinding / palette commands (`project:debug.*`; keymap
 * command ids need a namespace, see domain/keybindings/catalog.ts) and the
 * `debug.active` / `debug.paused` / `debug.stepInTargets` context keys,
 * registered by the project workspace while it is the active view.
 *
 *  - `debug.active`: a debug session (not a Run without debugging) of one of
 *    this workspace's projects has not ended. The stepping keys of the
 *    keymaps require it, so without a session F8 & co. keep their editor
 *    meaning.
 *  - `debug.paused`: that session is paused.
 *  - `debug.stepInTargets`: that session's adapter supports stepInTargets
 *    (DAP supportsStepInTargetsRequest); Smart Step Into needs it.
 *
 * Stepping, resume, run to cursor and evaluate act on that session (the
 * selected one when it qualifies, otherwise the newest; it becomes selected,
 * so the debug panel and Evaluate dialog follow). Stop ends the selected (or
 * newest) live session, Run / Debug start the run toolbar's selected
 * configuration (services/debug-guidance.ts `launchConfigurationFor`, as the
 * toolbar does). Every handler's `when` excludes a focused terminal: the shell
 * keeps F-keys and ⌘ chords.
 */
import { useVueI18n } from "@/i18n/messages";
import { registerKeybindingContext } from "@/domain/keybindings/context";
import type { DebugSessionView } from "@/domain/debug/session-model";
import { useActiveViewCommands } from "@/composables/use-active-view-commands";
import { registerCommand } from "@/services/command-registry";
import { launchConfigurationFor } from "@/services/debug-guidance";
import { canInvoke } from "@/services/desktop-bridge";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

export interface DebugCommandsOptions {
  /** The project whose run configurations Run / Debug start (the run toolbar's). */
  projectRoot: () => string | null | undefined;
  /** The active project file, for configurations that use `${file}`. */
  activeFile: () => string | undefined;
  /** Monaco language id of the active file (a project without configurations gets a "Current File" one for it). */
  activeLanguageId?: () => string | undefined;
  /** This workspace has the project of root open. */
  ownsProject: (root: string) => boolean;
  /** The text editor's cursor in a project file, or null. */
  cursor: () => { path: string; line: number } | null;
  /** Toggles a breakpoint on a line of the active project file. */
  toggleBreakpoint: (line: number) => void;
  /** Opens the Evaluate dialog with the editor's selection or the expression at its cursor. */
  openEvaluate: () => void;
  /** Asks for a new run configuration (Run / Debug without one). */
  newConfiguration: () => void;
}

/** Command id prefix (catalog.ts lists the same ids). */
const ID_PREFIX = "project:debug.";
const NOT_IN_TERMINAL = "!terminal.focus";
const IN_SESSION = `debug.active && ${NOT_IN_TERMINAL}`;
const WITH_STEP_IN_TARGETS = `debug.active && debug.stepInTargets && ${NOT_IN_TERMINAL}`;

/** Registers the commands and context keys; returns their disposers. */
export function registerDebugCommands(options: DebugCommandsOptions): Array<() => void> {
  const { t } = useVueI18n();
  const sessions = useDebugSessionsStore();

  function liveSession(includeRun: boolean): DebugSessionView | null {
    const live = sessions.sessions.filter(
      (view) => view.state !== "ended" && (includeRun || !view.noDebug) && options.ownsProject(view.projectRoot)
    );
    return live.find((view) => view.id === sessions.selectedSessionId) ?? live[live.length - 1] ?? null;
  }

  /** The debug session the stepping commands act on, selected so the panels follow. */
  function target(): DebugSessionView | null {
    const view = liveSession(false);
    if (view && view.id !== sessions.selectedSessionId) sessions.selectSession(view.id);
    return view;
  }

  let starting = false;
  async function start(noDebug: boolean) {
    const root = options.projectRoot();
    if (!root || starting) return;
    starting = true;
    try {
      const cfg = await launchConfigurationFor(root, options.activeLanguageId?.());
      if (cfg === "new") options.newConfiguration();
      if (!cfg || cfg === "new") return;
      // Failures are reported by the store.
      await sessions.start(root, cfg, { noDebug, activeFile: options.activeFile() });
    } finally {
      starting = false;
    }
  }

  /** `project:debug.<name>`, titled by `debug.command.<name>`. */
  const command = (id: `${typeof ID_PREFIX}${string}`, when: string, execute: () => void | Promise<void>) =>
    registerCommand({
      id,
      source: "builtin",
      title: () => t(`debug.command.${id.slice(ID_PREFIX.length)}` as Parameters<typeof t>[0]),
      subtitle: () => t("commandPalette.source.builtin"),
      keywords: ["debug", "调试"],
      when,
      execute,
    });

  const disposers = [
    registerKeybindingContext("debug.active", () => liveSession(false) !== null),
    registerKeybindingContext("debug.paused", () => liveSession(false)?.state === "paused"),
    registerKeybindingContext("debug.stepInTargets", () => liveSession(false)?.capabilities.supportsStepInTargetsRequest === true),
    command("project:debug.stepOver", IN_SESSION, () => {
      const view = target();
      if (view) return sessions.stepOver(view.id);
    }),
    command("project:debug.stepInto", IN_SESSION, () => {
      const view = target();
      if (view) return sessions.stepInto(view.id);
    }),
    // Several targets open the choice in the session's debug tool window.
    command("project:debug.smartStepInto", WITH_STEP_IN_TARGETS, () => {
      const view = target();
      if (view) return sessions.smartStepInto(view.id);
    }),
    command("project:debug.stepOut", IN_SESSION, () => {
      const view = target();
      if (view) return sessions.stepOut(view.id);
    }),
    command("project:debug.resume", IN_SESSION, () => {
      const view = target();
      if (view) return sessions.resume(view.id);
    }),
    command("project:debug.runToCursor", IN_SESSION, () => {
      const view = target();
      const cursor = options.cursor();
      if (view && cursor) return sessions.runToCursor(view.id, cursor.path, cursor.line);
    }),
    command("project:debug.evaluate", IN_SESSION, () => {
      if (target()) options.openEvaluate();
    }),
    command("project:debug.toggleBreakpoint", NOT_IN_TERMINAL, () => {
      const cursor = options.cursor();
      if (cursor) options.toggleBreakpoint(cursor.line);
    }),
  ];
  // Launching needs the desktop host (the run toolbar shows "desktop required" in a browser).
  if (canInvoke("plugin_host_debugger_start")) {
    disposers.push(
      command("project:debug.run", NOT_IN_TERMINAL, () => start(true)),
      command("project:debug.debug", NOT_IN_TERMINAL, () => start(false)),
      command("project:debug.stop", NOT_IN_TERMINAL, () => {
        const view = liveSession(true);
        if (view) return sessions.stop(view.id);
      })
    );
  }
  return disposers;
}

/** Registers the debugger commands while the calling view is active (mounted and not deactivated). */
export function useDebugCommands(options: DebugCommandsOptions): void {
  useActiveViewCommands(() => registerDebugCommands(options));
}
