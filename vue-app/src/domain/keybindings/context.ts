/**
 * when-context for keybindings: focus classification of the event target and
 * a registry of context-key providers (stores / components register getters,
 * e.g. `ai.running`). DOM access is limited to the small `ElementLike`
 * surface so the logic stays testable with fakes.
 *
 * Context keys: view, platform, editor.focus, editor.kind, terminal.focus,
 * terminal.selection, input.focus, dialog.focus, ai.panel.focus, ai.running,
 * ai.conversations.global, ai.multiline.field, ai.message.selection,
 * tree.selection, text.selection, palette.open, project.open, debug.active,
 * debug.paused, debug.stepInTargets.
 */
import type { PluginContextValue, PluginContextValues } from "@/domain/commands/when";
import type { KeyboardPlatform } from "@/domain/keybindings/keys";

export type KeybindingFocusClass = "monaco" | "terminal" | "input" | "dialog" | "other";

export interface ElementLike {
  tagName: string;
  isContentEditable?: boolean;
  closest(selector: string): ElementLike | null;
  getAttribute?(name: string): string | null;
}

export const MONACO_SELECTOR = ".monaco-editor";
/**
 * Code editors that are not Monaco (the database SQL editor is CodeMirror, see
 * components/database/query/SqlCodeEditor.vue). They count as editor focus like Monaco:
 * `editor.focus` holds and text-editing chords stay with the editor.
 */
export const CODE_EDITOR_SELECTOR = ".app-code-editor";
export const TERMINAL_SELECTOR = ".xterm";
export const DIALOG_SELECTOR = '.el-dialog, .el-drawer, .el-message-box, [aria-modal="true"]';
export const AI_PANEL_SELECTOR = ".ai-assistant-drawer__panel";

const TEXT_INPUT_TYPES = new Set(["", "text", "search", "url", "tel", "email", "password", "number", "date", "datetime-local", "month", "time", "week"]);

export function isTextInputElement(element: ElementLike | null): boolean {
  if (!element) return false;
  const tag = element.tagName.toUpperCase();
  if (tag === "TEXTAREA" || element.isContentEditable) return true;
  if (tag === "SELECT") return true;
  if (tag !== "INPUT") return false;
  const type = (element.getAttribute?.("type") ?? "").toLowerCase();
  return TEXT_INPUT_TYPES.has(type);
}

/**
 * Focus class of a keyboard event target. Order matters: Monaco and xterm
 * both use hidden textareas, so they are checked before plain inputs; a text
 * field inside a dialog is still "input" (dialogs only restrict non-input
 * targets further).
 */
export function keybindingFocusClass(target: ElementLike | null): KeybindingFocusClass {
  if (!target) return "other";
  if (target.closest(MONACO_SELECTOR) || target.closest(CODE_EDITOR_SELECTOR)) return "monaco";
  if (target.closest(TERMINAL_SELECTOR)) return "terminal";
  if (isTextInputElement(target)) return "input";
  if (target.closest(DIALOG_SELECTOR)) return "dialog";
  return "other";
}

export type KeybindingContextGetter = (target: ElementLike | null) => PluginContextValue | undefined;

const providers = new Map<string, Set<KeybindingContextGetter>>();

/**
 * Registers a context-key getter; returns a disposer. Several getters may
 * provide the same key (e.g. one per mounted editor); the first truthy value
 * wins, so providers only need to answer for their own subtree.
 */
export function registerKeybindingContext(key: string, getter: KeybindingContextGetter): () => void {
  let set = providers.get(key);
  if (!set) {
    set = new Set();
    providers.set(key, set);
  }
  set.add(getter);
  return () => {
    const current = providers.get(key);
    current?.delete(getter);
    if (current && !current.size) providers.delete(key);
  };
}

function providedValue(key: string, target: ElementLike | null): PluginContextValue | undefined {
  const set = providers.get(key);
  if (!set) return undefined;
  let fallback: PluginContextValue | undefined;
  for (const getter of set) {
    let value: PluginContextValue | undefined;
    try {
      value = getter(target);
    } catch {
      value = undefined;
    }
    if (value) return value;
    if (fallback === undefined) fallback = value;
  }
  return fallback;
}

export interface KeybindingContextInput {
  target: ElementLike | null;
  platform: KeyboardPlatform;
  focus?: KeybindingFocusClass;
  /** Whether a non-collapsed text selection exists (window.getSelection()). */
  textSelection?: boolean;
  extra?: PluginContextValues;
}

/** Builds the when-context for one event. Built-in focus keys win over providers. */
export function buildKeybindingContext(input: KeybindingContextInput): PluginContextValues {
  const focus = input.focus ?? keybindingFocusClass(input.target);
  const context: PluginContextValues = {};
  for (const key of providers.keys()) {
    const value = providedValue(key, input.target);
    if (value !== undefined) context[key] = value;
  }
  Object.assign(context, input.extra ?? {});
  context.platform = input.platform;
  context["editor.focus"] = focus === "monaco";
  context["terminal.focus"] = focus === "terminal";
  context["input.focus"] = focus === "input";
  context["dialog.focus"] = Boolean(input.target?.closest(DIALOG_SELECTOR));
  context["ai.panel.focus"] = Boolean(input.target?.closest(AI_PANEL_SELECTOR));
  context["text.selection"] = Boolean(input.textSelection) || Boolean(context["text.selection"]);
  return context;
}
