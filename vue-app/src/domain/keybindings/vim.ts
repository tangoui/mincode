/**
 * Pure parts of the Vim editor mode (the provider lives in
 * services/vim-editor-mode.ts, the lazily loaded monaco-vim adapter in
 * services/vim/monaco-vim-runtime.ts):
 *  - which key sequences Vim owns, so the Monaco keymap layer leaves them out;
 *  - how MinCode ex commands (`:w`, `:q`, `:wq`, `:x`) map to host actions;
 *  - the mode indicator model;
 *  - attach/detach bookkeeping for one editor (read-only editors stay plain
 *    Monaco; stale asynchronous loads are discarded).
 *
 * No Monaco, DOM or Vue imports: everything here is node-testable.
 */
import type { KeyboardPlatform, KeySequence } from "@/domain/keybindings/keys";

/**
 * `event.code` of the Ctrl+key chords monaco-vim's keymap binds (normal,
 * visual or insert mode): <C-a> <C-b> <C-c> <C-d> <C-e> <C-f> <C-i> <C-n>
 * <C-o> <C-p> <C-q> <C-r> <C-t> <C-u> <C-v> <C-w> <C-x> <C-y> <C-[>
 * <C-Space> <C-BS>. monaco-vim sees the key before Monaco's keybinding
 * service, so these always go to Vim while it is attached; the keymap layer
 * therefore does not add rules on them (their labels would lie).
 *
 * Ctrl+K, Ctrl+S, Ctrl+G, Ctrl+H, Ctrl+J, Ctrl+L, Ctrl+M, Ctrl+Z, digits,
 * F-keys and every ⌘ / Alt / Ctrl+Shift chord are not bound by Vim and keep
 * their MinCode / Monaco meaning.
 */
export const VIM_CTRL_KEY_CODES: ReadonlySet<string> = new Set([
  "KeyA",
  "KeyB",
  "KeyC",
  "KeyD",
  "KeyE",
  "KeyF",
  "KeyI",
  "KeyN",
  "KeyO",
  "KeyP",
  "KeyQ",
  "KeyR",
  "KeyT",
  "KeyU",
  "KeyV",
  "KeyW",
  "KeyX",
  "KeyY",
  "BracketLeft",
  "Space",
  "Backspace",
]);

/**
 * True when Vim handles `sequence` itself while attached. Only plain Ctrl
 * chords (no Shift/Alt/⌘) qualify — on every platform, since monaco-vim reads
 * the physical Ctrl key (⌃ on macOS). The first chord decides: a chord
 * sequence starting with a Vim key never reaches its second chord.
 */
export function vimOwnsSequence(sequence: KeySequence, _platform: KeyboardPlatform): boolean {
  if (sequence.kind !== "chords") return false;
  const first = sequence.chords[0];
  if (!first) return false;
  return first.ctrl && !first.shift && !first.alt && !first.meta && VIM_CTRL_KEY_CODES.has(first.code);
}

/** Host actions reachable from ex commands. */
export type VimExAction = "write" | "quit" | "write-quit";

/**
 * Ex commands MinCode defines on top of monaco-vim's built-ins. `:w` /
 * `:write` is monaco-vim's own (it calls `CodeMirror.commands.save`, which
 * the runtime routes to the "write" action); the others are registered with
 * `Vim.defineEx(name, prefix, …)`. `:e <path>` is intentionally absent: there
 * is no validated path-opening boundary to route it to.
 */
export const VIM_EX_COMMANDS: ReadonlyArray<{ name: string; prefix: string; action: VimExAction }> = [
  { name: "quit", prefix: "q", action: "quit" },
  { name: "wq", prefix: "wq", action: "write-quit" },
  { name: "xit", prefix: "x", action: "write-quit" },
];

export interface VimExEnvironment {
  /** The editor has a save handler (Project, SFTP, Git conflict, Settings JSON). */
  canSave: boolean;
  /** `workbench:close-tab` is registered and enabled in the current context. */
  canClose: boolean;
}

export type VimExHint = "no-save" | "no-close";

export interface VimExPlan {
  save: boolean;
  close: boolean;
  /** Status-line hints explaining a skipped step. */
  hints: VimExHint[];
}

/**
 * Decides what an ex command does. `:wq` on an editor without a save handler
 * (the SQL query editor, whose primary action is "run") does not close the
 * tab: closing after a failed write would be surprising, exactly like Vim's
 * refusal to quit with an unwritten buffer.
 */
export function planVimExCommand(action: VimExAction, env: VimExEnvironment): VimExPlan {
  const hints: VimExHint[] = [];
  const wantsSave = action === "write" || action === "write-quit";
  const wantsClose = action === "quit" || action === "write-quit";
  const save = wantsSave && env.canSave;
  if (wantsSave && !env.canSave) hints.push("no-save");
  let close = false;
  if (wantsClose && (!wantsSave || save)) {
    if (env.canClose) close = true;
    else hints.push("no-close");
  }
  return { save, close, hints };
}

/** Mode indicator states (the CodeMirror `vim-mode-change` payload, normalised). */
export type VimModeIndicator = "normal" | "insert" | "visual" | "visual-line" | "visual-block" | "replace";

export function vimModeIndicator(event: { mode?: unknown; subMode?: unknown } | null | undefined): VimModeIndicator {
  const mode = typeof event?.mode === "string" ? event.mode : "normal";
  if (mode === "insert") return "insert";
  if (mode === "replace") return "replace";
  if (mode === "visual") {
    if (event?.subMode === "linewise") return "visual-line";
    if (event?.subMode === "blockwise") return "visual-block";
    return "visual";
  }
  return "normal";
}

/** Keydown events that belong to an IME composition; Vim must not interpret them. */
export function isVimCompositionKey(event: { isComposing?: boolean; keyCode?: number; key?: string }): boolean {
  return Boolean(event.isComposing) || event.keyCode === 229 || event.key === "Process";
}

export interface VimDisposable {
  dispose(): void;
}

export interface VimAttachmentHooks {
  /** Current read-only state of the editor. */
  isReadOnly(): boolean;
  /** Subscribes to read-only changes. */
  onReadOnlyChange(listener: () => void): VimDisposable;
  /** Starts Vim on the editor (may load code lazily). */
  start(): Promise<VimDisposable> | VimDisposable;
  /** Called when `start` rejects (e.g. the lazy chunk failed to load). */
  onError?(error: unknown): void;
}

export interface VimAttachment extends VimDisposable {
  /** True while a started (or starting) Vim session is owned by this attachment. */
  readonly active: boolean;
}

/**
 * Attach/detach bookkeeping for one editor: Vim runs only while the editor is
 * writable (read-only viewers keep plain Monaco navigation and selection),
 * follows read-only toggles, and drops sessions whose asynchronous start
 * resolves after they were no longer wanted.
 */
export function createVimAttachment(hooks: VimAttachmentHooks): VimAttachment {
  let session: VimDisposable | null = null;
  let generation = 0;
  let starting = false;
  let disposed = false;

  const stop = () => {
    generation += 1;
    starting = false;
    session?.dispose();
    session = null;
  };

  const startIfWanted = () => {
    if (disposed || session || starting || hooks.isReadOnly()) return;
    const current = ++generation;
    starting = true;
    let result: Promise<VimDisposable> | VimDisposable;
    try {
      result = hooks.start();
    } catch (error) {
      starting = false;
      hooks.onError?.(error);
      return;
    }
    void Promise.resolve(result).then(
      (started) => {
        if (current !== generation || disposed) {
          started.dispose();
          return;
        }
        starting = false;
        session = started;
      },
      (error: unknown) => {
        if (current === generation) starting = false;
        hooks.onError?.(error);
      },
    );
  };

  const subscription = hooks.onReadOnlyChange(() => {
    if (hooks.isReadOnly()) stop();
    else startIfWanted();
  });
  startIfWanted();

  return {
    get active() {
      return session !== null || starting;
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      subscription.dispose();
      stop();
    },
  };
}
