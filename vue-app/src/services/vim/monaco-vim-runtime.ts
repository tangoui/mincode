/**
 * The monaco-vim adapter. This module is the ONLY place that imports
 * `monaco-vim`, and it is itself only reached through a dynamic `import()`
 * from services/vim-editor-mode.ts, so the Vim keymap (~190 KB) is a lazy
 * chunk loaded the first time an editor attaches while the active keymap
 * preset declares `editorMode: "vim"` (scripts/vim-editor-mode.test.mjs
 * enforces this).
 *
 * Adaptations for monaco-editor 0.56 / MinCode:
 *  - IME: monaco-vim receives a proxied editor whose `onKeyDown` drops every
 *    keydown that belongs to an IME composition, so composing Chinese text in
 *    insert mode works and composition keys never run normal-mode commands.
 *  - `>>` / `<<`: monaco-vim builds Monaco's internal `ShiftCommand` without
 *    the language-configuration service that 0.56 added as a constructor
 *    argument; the proxied `executeCommand` supplies the editor's own service
 *    so re-indenting a misaligned line cannot throw.
 *  - The status line is rendered by `MinCodeVimStatusBar` (textContent / DOM
 *    nodes only, `--app-*` tokens, i18n mode labels) into the host's strip.
 *  - Ex commands `:w`, `:q`, `:wq`, `:x` are routed to host actions; the `+`
 *    and `*` registers write yanks to the system clipboard.
 */
import { VimMode } from "monaco-vim";
import type * as monaco from "@/services/monaco-runtime.js";
import {
  isVimCompositionKey,
  VIM_EX_COMMANDS,
  vimModeIndicator,
  type VimDisposable,
  type VimExAction,
  type VimExHint,
  type VimModeIndicator,
} from "@/domain/keybindings/vim";
import type { MessageKey } from "@/i18n/messages";

type Editor = monaco.editor.IStandaloneCodeEditor;

interface StatusInputOptions {
  selectValueOnOpen?: boolean;
  value?: string;
  onKeyUp?: (event: KeyboardEvent, value: string, close: () => void) => void;
  onKeyDown?: (event: KeyboardEvent, value: string, close: () => void) => boolean | void;
  onKeyInput?: (event: InputEvent, value: string, close: () => void) => void;
  onBlur?: (event: FocusEvent, close: () => void) => void;
  closeOnBlur?: boolean;
  closeOnEnter?: boolean;
}

/** The subset of monaco-vim's CodeMirror adapter MinCode uses. */
interface VimAdapter {
  attach(): void;
  dispose(): void;
  on(event: string, handler: (...args: unknown[]) => void): void;
  setStatusBar(statusBar: MinCodeVimStatusBar): void;
}

interface VimRegister {
  /** MinCode extension: refreshes the cached text without writing the clipboard. */
  sync?(text: string): void;
  setText(text: string, linewise?: boolean, blockwise?: boolean): void;
  pushText(text: string, linewise?: boolean): void;
  clear(): void;
  toString(): string;
  linewise?: boolean;
  blockwise?: boolean;
}

interface VimApi {
  defineEx(name: string, prefix: string, handler: (cm: VimAdapter) => void): void;
  defineRegister(name: string, register: VimRegister): void;
}

interface VimAdapterClass {
  new (editor: Editor): VimAdapter;
  commands: Record<string, (cm: VimAdapter) => void>;
  Vim: VimApi;
}

export interface VimRuntimeActions {
  /** Runs `:w` / `:q` / `:wq` for `editor`; returns hints for the status line. */
  runEx(editor: Editor, action: VimExAction): VimExHint[];
  /** Writes yanked text to the system clipboard (`"+` / `"*` registers). */
  copyToClipboard(text: string): void;
  /** Reads the system clipboard for `"+p` / `"*p`; null where reading would prompt (browser preview). */
  readClipboard(): Promise<string> | null;
  translate(key: MessageKey): string;
}

export interface VimRuntimeAttachOptions {
  /** Host-provided status strip; when absent an overlay strip is created in the editor container. */
  statusElement: HTMLElement | null;
  actions: VimRuntimeActions;
}

const MODE_LABEL_KEYS: Record<VimModeIndicator, MessageKey> = {
  normal: "keybindings.vim.mode.normal",
  insert: "keybindings.vim.mode.insert",
  visual: "keybindings.vim.mode.visual",
  "visual-line": "keybindings.vim.mode.visualLine",
  "visual-block": "keybindings.vim.mode.visualBlock",
  replace: "keybindings.vim.mode.replace",
};

const HINT_KEYS: Record<VimExHint, MessageKey> = {
  "no-save": "keybindings.vim.hint.noSave",
  "no-close": "keybindings.vim.hint.noClose",
};

const NOTIFICATION_MS = 5000;

/**
 * Status line for one editor (the monaco-vim `StatusBar` contract: mode,
 * pending keys, the `:`/`/` prompt and notifications). Content is only ever
 * inserted as text nodes or as the DOM nodes monaco-vim builds itself.
 *
 * The prompt `<input>` is created by monaco-vim's keymap (not by MinCode) and
 * handed over as a DOM node; Element Plus cannot be injected there, so it is
 * styled through `.app-vim-status__prompt input` with `--app-*` tokens.
 */
class MinCodeVimStatusBar {
  private readonly root: HTMLElement;
  private readonly modeNode: HTMLElement;
  private readonly promptNode: HTMLElement;
  private readonly notificationNode: HTMLElement;
  private readonly keysNode: HTMLElement;
  private input: { node: HTMLInputElement; callback?: (value: string) => void; options?: StatusInputOptions } | null = null;
  private notificationTimer: ReturnType<typeof setTimeout> | undefined;

  constructor(
    root: HTMLElement,
    private readonly editor: Editor,
    private readonly translate: (key: MessageKey) => string,
  ) {
    this.root = root;
    root.classList.add("app-vim-status");
    root.setAttribute("aria-label", translate("keybindings.vim.statusLabel"));
    this.modeNode = this.part("app-vim-status__mode");
    this.modeNode.setAttribute("role", "status");
    this.modeNode.setAttribute("aria-live", "polite");
    this.promptNode = this.part("app-vim-status__prompt");
    this.notificationNode = this.part("app-vim-status__notification");
    this.notificationNode.setAttribute("role", "status");
    this.keysNode = this.part("app-vim-status__keys");
    this.setMode(null);
  }

  private part(className: string): HTMLElement {
    const node = document.createElement("span");
    node.className = className;
    this.root.appendChild(node);
    return node;
  }

  setMode(event: { mode?: unknown; subMode?: unknown } | null) {
    const mode = vimModeIndicator(event);
    this.root.dataset.vimMode = mode;
    this.modeNode.textContent = this.translate(MODE_LABEL_KEYS[mode]);
  }

  setKeyBuffer(keys: string) {
    this.keysNode.textContent = keys;
  }

  setText(text: string) {
    this.modeNode.textContent = text;
  }

  toggleVisibility(visible: boolean) {
    this.root.hidden = !visible;
    if (this.input) this.removeInputListeners();
    if (this.notificationTimer) clearTimeout(this.notificationTimer);
  }

  setSec(content: Node | string | null | undefined, callback?: (value: string) => void, options?: StatusInputOptions) {
    this.notificationNode.textContent = "";
    if (content === undefined) return this.closeInput;
    replaceContent(this.promptNode, content);
    this.root.classList.toggle("is-prompting", Boolean(content));
    const input = this.promptNode.querySelector("input");
    if (input) {
      input.setAttribute("aria-label", this.translate("keybindings.vim.promptLabel"));
      this.input = { node: input, callback, options };
      if (options?.value) input.value = options.value;
      input.focus();
      if (options?.selectValueOnOpen) input.select();
      this.addInputListeners();
    }
    return this.closeInput;
  }

  showNotification(content: Node | string) {
    const holder = document.createElement("span");
    replaceContent(holder, content);
    this.notificationNode.textContent = holder.textContent;
    if (this.notificationTimer) clearTimeout(this.notificationTimer);
    this.notificationTimer = setTimeout(() => {
      this.notificationNode.textContent = "";
    }, NOTIFICATION_MS);
  }

  closeInput = () => {
    const hadInput = Boolean(this.input);
    this.removeInputListeners();
    this.input = null;
    this.setSec("");
    this.root.classList.remove("is-prompting");
    if (hadInput) this.editor.focus();
  };

  clear = () => {
    replaceContent(this.promptNode, "");
    this.notificationNode.textContent = "";
    this.keysNode.textContent = "";
  };

  /** Removes everything this bar added (host strips are reused by later sessions). */
  destroy() {
    this.toggleVisibility(false);
    this.removeInputListeners();
    this.input = null;
    for (const node of [this.modeNode, this.promptNode, this.notificationNode, this.keysNode]) node.remove();
    this.root.classList.remove("app-vim-status", "is-prompting");
    delete this.root.dataset.vimMode;
    this.root.removeAttribute("aria-label");
  }

  private readonly onInputKeyUp = (event: KeyboardEvent) => {
    if (!this.input) return;
    this.input.options?.onKeyUp?.(event, (event.target as HTMLInputElement).value, this.closeInput);
  };

  private readonly onInputInput = (event: Event) => {
    if (!this.input) return;
    this.input.options?.onKeyInput?.(event as InputEvent, (event.target as HTMLInputElement).value, this.closeInput);
  };

  private readonly onInputBlur = (event: FocusEvent) => {
    if (!this.input) return;
    const { options } = this.input;
    options?.onBlur?.(event, this.closeInput);
    if (options?.closeOnBlur) this.closeInput();
  };

  private readonly onInputKeyDown = (event: KeyboardEvent) => {
    if (!this.input) return;
    // Enter/Escape that confirm or cancel an IME composition belong to the IME.
    if (isVimCompositionKey(event)) return;
    const { options, callback, node } = this.input;
    if (options?.onKeyDown?.(event, node.value, this.closeInput)) return;
    const value = node.value;
    if (event.key === "Escape" || (options?.closeOnEnter !== false && event.key === "Enter")) {
      node.blur();
      event.stopPropagation();
      this.closeInput();
    }
    if (event.key === "Enter" && callback) {
      event.stopPropagation();
      event.preventDefault();
      callback(value);
    }
  };

  private addInputListeners() {
    const node = this.input?.node;
    if (!node) return;
    node.addEventListener("keyup", this.onInputKeyUp);
    node.addEventListener("keydown", this.onInputKeyDown);
    node.addEventListener("input", this.onInputInput);
    node.addEventListener("blur", this.onInputBlur);
  }

  private removeInputListeners() {
    const node = this.input?.node;
    if (!node) return;
    node.removeEventListener("keyup", this.onInputKeyUp);
    node.removeEventListener("keydown", this.onInputKeyDown);
    node.removeEventListener("input", this.onInputInput);
    node.removeEventListener("blur", this.onInputBlur);
  }
}

function replaceContent(element: HTMLElement, content: Node | string | null | undefined) {
  element.replaceChildren();
  if (!content) return;
  element.appendChild(typeof content === "string" ? document.createTextNode(content) : content);
}

/** Monaco's internal ShiftCommand as monaco-vim constructs it (see module comment). */
interface ShiftCommandLike {
  _opts: unknown;
  _languageConfigurationService?: unknown;
}

function isShiftCommandLike(value: unknown): value is ShiftCommandLike {
  return Boolean(value) && typeof value === "object" && "_opts" in (value as object) && "_languageConfigurationService" in (value as object);
}

const NO_LANGUAGE_CONFIGURATION = { getLanguageConfiguration: () => undefined };

function languageConfigurationServiceOf(editor: Editor): unknown {
  const internal = editor as unknown as { _getViewModel?: () => { languageConfigurationService?: unknown } | null };
  return internal._getViewModel?.()?.languageConfigurationService ?? NO_LANGUAGE_CONFIGURATION;
}

/** The editor monaco-vim sees: IME-guarded key events and a 0.56-compatible ShiftCommand. */
function vimFacingEditor(editor: Editor, isComposing: () => boolean): Editor {
  const onKeyDown: Editor["onKeyDown"] = (listener) =>
    editor.onKeyDown((event) => {
      if (isComposing() || isVimCompositionKey(event.browserEvent)) return;
      listener(event);
    });
  const executeCommand: Editor["executeCommand"] = (source, command) => {
    if (isShiftCommandLike(command) && !command._languageConfigurationService) {
      command._languageConfigurationService = languageConfigurationServiceOf(editor);
    }
    editor.executeCommand(source, command);
  };
  return new Proxy(editor, {
    get(target, property) {
      if (property === "onKeyDown") return onKeyDown;
      if (property === "executeCommand") return executeCommand;
      const value: unknown = Reflect.get(target, property, target);
      return typeof value === "function" ? (value as (...args: unknown[]) => unknown).bind(target) : value;
    },
  });
}

interface AdapterBinding {
  editor: Editor;
  statusBar: MinCodeVimStatusBar;
  actions: VimRuntimeActions;
}

const bindings = new WeakMap<VimAdapter, AdapterBinding>();
let globalsInstalled = false;
const clipboardRegisters: VimRegister[] = [];

function runEx(cm: VimAdapter, action: VimExAction) {
  const binding = bindings.get(cm);
  if (!binding) return;
  const hints = binding.actions.runEx(binding.editor, action);
  if (hints.length > 0) {
    binding.statusBar.showNotification(hints.map((hint) => binding.actions.translate(HINT_KEYS[hint])).join(" "));
  }
}

/** A register backed by the system clipboard for writes (`"+y`, `"*y`); reads return the last yank. */
function clipboardRegister(copy: (text: string) => void): VimRegister {
  let text = "";
  const register: VimRegister = {
    linewise: false,
    blockwise: false,
    setText(value, linewise, blockwise) {
      text = value || "";
      register.linewise = Boolean(linewise);
      register.blockwise = Boolean(blockwise);
      copy(text);
    },
    pushText(value, linewise) {
      if (linewise && !register.linewise) text += "\n";
      if (linewise) register.linewise = true;
      text += value;
      copy(text);
    },
    clear() {
      text = "";
      register.linewise = false;
      register.blockwise = false;
    },
    sync(value) {
      if (value === text) return;
      text = value;
      register.linewise = false;
      register.blockwise = false;
    },
    toString() {
      return text;
    },
  };
  return register;
}

function installGlobals(adapterClass: VimAdapterClass, actions: VimRuntimeActions) {
  if (globalsInstalled) return;
  globalsInstalled = true;
  // `:w` / `:write` is monaco-vim's built-in; it calls CodeMirror.commands.save.
  adapterClass.commands.save = (cm) => runEx(cm, "write");
  for (const command of VIM_EX_COMMANDS) {
    adapterClass.Vim.defineEx(command.name, command.prefix, (cm) => runEx(cm, command.action));
  }
  // The first attaching editor's clipboard writer is process-wide by design (one clipboard).
  const copy = (text: string) => actions.copyToClipboard(text);
  for (const name of ["+", "*"]) {
    const register = clipboardRegister(copy);
    try {
      adapterClass.Vim.defineRegister(name, register);
      clipboardRegisters.push(register);
    } catch {
      // Already defined (hot reload): keep the existing register.
    }
  }
}

/** Starts Vim on `editor`; the returned disposable restores plain Monaco editing. */
export function attachVim(editor: Editor, options: VimRuntimeAttachOptions): VimDisposable {
  const adapterClass = VimMode as unknown as VimAdapterClass;
  installGlobals(adapterClass, options.actions);

  let composing = false;
  const compositionListeners = [
    editor.onDidCompositionStart(() => {
      composing = true;
    }),
    editor.onDidCompositionEnd(() => {
      composing = false;
    }),
    // `"+p` pastes what the system clipboard held when the editor gained focus.
    editor.onDidFocusEditorText(() => {
      void options.actions.readClipboard()?.then(
        (text) => {
          for (const register of clipboardRegisters) register.sync?.(text);
        },
        () => undefined,
      );
    }),
  ];

  let overlay: HTMLElement | null = null;
  let statusRoot = options.statusElement;
  if (!statusRoot) {
    overlay = document.createElement("div");
    overlay.classList.add("is-overlay");
    editor.getContainerDomNode().appendChild(overlay);
    statusRoot = overlay;
  }

  const statusBar = new MinCodeVimStatusBar(statusRoot, editor, options.actions.translate);
  const adapter = new adapterClass(vimFacingEditor(editor, () => composing));
  bindings.set(adapter, { editor, statusBar, actions: options.actions });

  let keys = "";
  adapter.on("vim-mode-change", (event) => statusBar.setMode(event as { mode?: unknown; subMode?: unknown }));
  adapter.on("vim-keypress", (key) => {
    keys = key === ":" ? "" : keys + String(key);
    statusBar.setKeyBuffer(keys);
  });
  adapter.on("vim-command-done", () => {
    keys = "";
    statusBar.setKeyBuffer("");
  });
  statusBar.toggleVisibility(true);
  adapter.setStatusBar(statusBar);
  adapter.attach();

  let disposed = false;
  return {
    dispose() {
      if (disposed) return;
      disposed = true;
      adapter.dispose();
      bindings.delete(adapter);
      for (const listener of compositionListeners) listener.dispose();
      statusBar.destroy();
      overlay?.remove();
    },
  };
}
