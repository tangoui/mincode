/**
 * The Vim editor-mode provider (registered from services/monaco-keymap.ts).
 * It is always registered but inert: it only runs when the active keymap
 * preset chain declares `editorMode: "vim"` (e.g. the official `keymaps.vim`
 * preset). Then `registerMonacoEditor` attaches it to every MinCode
 * text editor; the monaco-vim chunk is loaded on the first attach through a
 * dynamic import and never earlier.
 *
 * Behaviour:
 *  - Vim runs while an editor is writable; read-only viewers (DDL viewer,
 *    read-only files) and diff editors keep plain Monaco behaviour.
 *  - `ownsSequence`: plain Ctrl chords monaco-vim binds (Ctrl+V/R/W/D/U/…,
 *    see domain/keybindings/vim.ts). They go to Vim while the editor has
 *    focus, on every platform; the keymap layer adds no Monaco rules for
 *    them. ⌘ chords on macOS and unbound Ctrl chords (Ctrl+K, Ctrl+S, …) keep
 *    their app / Monaco meaning.
 *  - Ex commands: `:w` → the editor's save handler (no-op with a hint where
 *    the editor has none, e.g. the SQL query editor — use its run key);
 *    `:q` → `workbench:close-tab` when registered and enabled, otherwise a
 *    hint; `:wq` / `:x` → save then close.
 */
import { ElMessage } from "element-plus";
import * as monaco from "@/services/monaco-runtime.js";
import { registerEditorModeProvider } from "@/domain/keybindings/editor-mode";
import { createVimAttachment, planVimExCommand, vimOwnsSequence, type VimExHint } from "@/domain/keybindings/vim";
import { useVueI18n } from "@/i18n/messages";
import { executeRegisteredCommand } from "@/services/command-registry";
import { canRunKeybindingCommand, currentKeybindingContext } from "@/services/keybinding-dispatcher";
import { copyText, isDesktopRuntime, readText } from "@/services/desktop-bridge";
import type { VimRuntimeActions } from "@/services/vim/monaco-vim-runtime";

type Editor = monaco.editor.IStandaloneCodeEditor;

export const VIM_CLOSE_TAB_COMMAND = "workbench:close-tab";

export interface VimEditorModeOptions {
  /** The save handler of a registered editor, if it has one. */
  saveHandlerFor(editor: Editor): (() => void) | undefined;
}

type VimRuntimeModule = typeof import("@/services/vim/monaco-vim-runtime");

let runtimePromise: Promise<VimRuntimeModule> | null = null;
let loadErrorShown = false;

function loadVimRuntime(): Promise<VimRuntimeModule> {
  runtimePromise ??= import("@/services/vim/monaco-vim-runtime").catch((error: unknown) => {
    runtimePromise = null; // allow a retry on the next attach
    throw error;
  });
  return runtimePromise;
}

function createActions(options: VimEditorModeOptions): VimRuntimeActions {
  const { t } = useVueI18n();
  return {
    translate: (key) => t(key),
    copyToClipboard(text) {
      void copyText(text).catch(() => ElMessage.error(t("keybindings.vim.clipboardFailed")));
    },
    // Browser preview: reading would trigger a permission prompt on every focus.
    readClipboard: () => (isDesktopRuntime() ? readText() : null),
    runEx(editor, action): VimExHint[] {
      const save = options.saveHandlerFor(editor);
      const context = currentKeybindingContext(editor.getContainerDomNode());
      const plan = planVimExCommand(action, {
        canSave: Boolean(save),
        canClose: canRunKeybindingCommand(VIM_CLOSE_TAB_COMMAND, context),
      });
      if (plan.save) save?.();
      if (plan.close) {
        void executeRegisteredCommand(VIM_CLOSE_TAB_COMMAND).catch(() =>
          ElMessage.error(t("commandPalette.actionFailed", { name: t(`keybindings.command.${VIM_CLOSE_TAB_COMMAND}`) })),
        );
      }
      return plan.hints;
    },
  };
}

let registered: (() => void) | null = null;

/** Registers the Vim provider once; returns its disposer. */
export function registerVimEditorMode(options: VimEditorModeOptions): () => void {
  if (registered) return registered;
  let actions: VimRuntimeActions | null = null;
  const dispose = registerEditorModeProvider({
    id: "vim",
    ownsSequence: vimOwnsSequence,
    attach(rawEditor, { statusElement }) {
      const editor = rawEditor as Editor;
      return createVimAttachment({
        isReadOnly: () => editor.getOption(monaco.editor.EditorOption.readOnly),
        onReadOnlyChange: (listener) =>
          editor.onDidChangeConfiguration((event) => {
            if (event.hasChanged(monaco.editor.EditorOption.readOnly)) listener();
          }),
        async start() {
          const runtime = await loadVimRuntime();
          actions ??= createActions(options);
          return runtime.attachVim(editor, { statusElement: statusElement ?? null, actions });
        },
        onError() {
          if (loadErrorShown) return;
          loadErrorShown = true;
          ElMessage.error(useVueI18n().t("keybindings.vim.loadFailed"));
        },
      });
    },
  });
  registered = () => {
    dispose();
    registered = null;
  };
  return registered;
}
