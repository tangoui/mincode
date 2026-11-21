/**
 * Terminal key arbitration for xterm's `attachCustomKeyEventHandler`.
 *
 * Every keydown in a terminal gets one of three verdicts:
 *  - `run`: a `terminal:*` command of the effective keymap matched (copy,
 *    paste, find, clear, select all). The caller runs it, calls
 *    preventDefault (so neither xterm nor the native Edit menu act on it)
 *    and returns false.
 *  - `bubble`: an application binding that may leave the terminal (the
 *    skip-shell allowlist below), or any macOS ⌘ chord. The handler returns
 *    false WITHOUT preventDefault so the window keybinding dispatcher sees it.
 *  - `shell`: everything else. xterm encodes the key for the shell (Ctrl+C,
 *    Ctrl+R, Ctrl+W, Ctrl+K ...).
 *
 * With the `sendKeybindingsToShell` option only ⌘ chords and Ctrl+Shift
 * chords are intercepted at all. Pure: no DOM, store or xterm access beyond
 * the injected `hasSelection`.
 */
import { evaluateWhenClauses, type PluginContextValues } from "@/domain/commands/when";
import { getKeybindingCommand } from "@/domain/keybindings/catalog";
import { keyChordFromEvent, type KeyboardEventLike, type KeyboardPlatform, type KeyChord } from "@/domain/keybindings/keys";
import { lookupKeybinding, type KeybindingResolution, type ResolvedKeybinding } from "@/domain/keybindings/resolver";

export type TerminalKeybindingCommand =
  | "terminal:copy"
  | "terminal:paste"
  | "terminal:find"
  | "terminal:clear"
  | "terminal:select-all";

/**
 * Application commands allowed to take a key away from the shell (VS Code's
 * "commands to skip shell"). Anything else only leaves the terminal as a
 * macOS ⌘ chord, which xterm never encodes for the shell anyway.
 */
export const TERMINAL_SKIP_SHELL_COMMANDS: ReadonlySet<string> = new Set([
  "builtin:command-palette",
  "builtin:quick-open",
  "builtin:search-everywhere",
  "workbench:show-view.terminal",
  "workbench:next-tab",
  "workbench:prev-tab",
  "workbench:close-tab",
  "terminal:new-tab",
  "terminal:next-tab",
  "terminal:prev-tab",
  "builtin:settings",
  "builtin:lock-app",
]);

export interface TerminalKeyEventLike extends KeyboardEventLike {
  type: string;
  isComposing?: boolean;
  keyCode?: number;
}

export interface TerminalKeyEnvironment {
  platform: KeyboardPlatform;
  resolution: KeybindingResolution;
  /** Terminal commands this terminal implements (e.g. a log viewer has no paste). */
  handles: (command: TerminalKeybindingCommand) => boolean;
  hasSelection: boolean;
  sendKeybindingsToShell: boolean;
  /** Whether an application command can run now (registered, catalog-available, `when` holds). */
  canExecuteApp: (command: string, context: PluginContextValues) => boolean;
  /** Extra when-context (focus class, providers); terminal keys are added on top. */
  context?: PluginContextValues;
}

export type TerminalKeyDecision =
  | { kind: "run"; command: TerminalKeybindingCommand; binding: ResolvedKeybinding; chord: KeyChord }
  | { kind: "bubble"; binding?: ResolvedKeybinding }
  | { kind: "shell" };

const SHELL: TerminalKeyDecision = { kind: "shell" };

export function isImeKeyEvent(event: TerminalKeyEventLike): boolean {
  return Boolean(event.isComposing) || event.keyCode === 229 || event.key === "Process";
}

function isTerminalCommand(command: string): command is TerminalKeybindingCommand {
  return getKeybindingCommand(command)?.handler === "terminal";
}

/** Chords that stay interceptable when keybindings are sent to the shell. */
function isReservedFromShell(chord: KeyChord): boolean {
  return chord.meta || (chord.ctrl && chord.shift);
}

export function decideTerminalKey(event: TerminalKeyEventLike, env: TerminalKeyEnvironment): TerminalKeyDecision {
  if (event.type !== "keydown" || isImeKeyEvent(event)) return SHELL;
  const chord = keyChordFromEvent(event);
  if (!chord) return SHELL;
  if (env.sendKeybindingsToShell && !isReservedFromShell(chord)) return SHELL;
  const macCommandChord = env.platform === "mac" && chord.meta;
  const context: PluginContextValues = {
    ...env.context,
    platform: env.platform,
    "terminal.focus": true,
    "terminal.selection": env.hasSelection,
    "editor.focus": false,
    "input.focus": false,
  };
  const result = lookupKeybinding(env.resolution, [chord], (binding) => {
    if (!evaluateWhenClauses(binding.clauses, context)) return false;
    if (isTerminalCommand(binding.command)) return env.handles(binding.command);
    if (!TERMINAL_SKIP_SHELL_COMMANDS.has(binding.command) && !macCommandChord) return false;
    return env.canExecuteApp(binding.command, context);
  });
  if (result.kind === "execute" && isTerminalCommand(result.binding.command)) {
    return { kind: "run", command: result.binding.command, binding: result.binding, chord };
  }
  if (result.kind !== "none") return { kind: "bubble", binding: result.binding };
  // xterm never sends ⌘ chords to the shell; leave them to the dispatcher and
  // the native menu (⌘Q, ⌘H, ⌘`...).
  return macCommandChord ? { kind: "bubble" } : SHELL;
}

// ---------------------------------------------------------------------------
// xterm custom key handler
// ---------------------------------------------------------------------------

/** The xterm surface the handler needs (a fake in tests). */
export interface TerminalKeybindingTarget {
  hasSelection(): boolean;
  clearSelection(): void;
}

type Action = () => void | Promise<void>;

export interface TerminalKeybindingActions {
  xterm: () => TerminalKeybindingTarget | null | undefined;
  copy?: Action;
  /** Must be the guarded paste path; omit for read-only terminals. */
  paste?: Action;
  find?: Action;
  clear?: Action;
  selectAll?: Action;
}

export interface TerminalKeyEvent extends TerminalKeyEventLike {
  preventDefault(): void;
}

export interface TerminalKeyHandlerEnvironment<E extends TerminalKeyEvent = TerminalKeyEvent> {
  platform: KeyboardPlatform;
  resolution: () => KeybindingResolution;
  sendKeybindingsToShell: () => boolean;
  canExecuteApp: (command: string, context: PluginContextValues) => boolean;
  context?: (event: E) => PluginContextValues;
  onError?: (command: TerminalKeybindingCommand, error: unknown) => void;
}

const ACTION_OF: Record<TerminalKeybindingCommand, keyof Omit<TerminalKeybindingActions, "xterm">> = {
  "terminal:copy": "copy",
  "terminal:paste": "paste",
  "terminal:find": "find",
  "terminal:clear": "clear",
  "terminal:select-all": "selectAll",
};

/**
 * Store-free handler factory (used by the composable and the tests). Returns
 * the value for attachCustomKeyEventHandler: false when the terminal must not
 * process the key.
 */
export function createTerminalKeyHandler<E extends TerminalKeyEvent>(
  actions: TerminalKeybindingActions,
  env: TerminalKeyHandlerEnvironment<E>,
): (event: E) => boolean {
  return (event) => {
    const xterm = actions.xterm();
    if (!xterm || event.type !== "keydown") return true;
    const decision = decideTerminalKey(event, {
      platform: env.platform,
      resolution: env.resolution(),
      handles: (command) => typeof actions[ACTION_OF[command]] === "function",
      hasSelection: xterm.hasSelection(),
      sendKeybindingsToShell: env.sendKeybindingsToShell(),
      canExecuteApp: env.canExecuteApp,
      context: env.context?.(event),
    });
    if (decision.kind === "shell") return true;
    if (decision.kind === "bubble") return false;
    event.preventDefault();
    const action = actions[ACTION_OF[decision.command]];
    const { chord } = decision;
    // Plain Ctrl+C copies once, then interrupts again (Windows Terminal / VS Code behaviour).
    const clearAfterCopy =
      decision.command === "terminal:copy" && env.platform !== "mac" && chord.ctrl && !chord.shift && chord.code === "KeyC";
    const fail = (error: unknown) => env.onError?.(decision.command, error);
    try {
      // Run synchronously: clipboard access keeps the key press as its user gesture.
      void Promise.resolve(action?.())
        .then(() => {
          if (clearAfterCopy) actions.xterm()?.clearSelection();
        })
        .catch(fail);
    } catch (error) {
      fail(error);
    }
    return false;
  };
}
