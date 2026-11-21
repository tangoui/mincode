/**
 * Editor-mode providers (modal editing such as Vim). A preset may request
 * `editorMode: "vim"`; the provider — lazily loaded, e.g. monaco-vim in a
 * later phase — attaches to each Monaco editor and owns part of the keyboard
 * while active. The Monaco keymap layer asks the active provider which key
 * sequences it owns and leaves those to it instead of adding rules for them.
 *
 * No provider is registered in phases 0–2: a preset requesting a mode that
 * has no provider still works, it just behaves like a normal keymap.
 */
import type { KeyboardPlatform, KeySequence } from "@/domain/keybindings/keys";
import type { KeymapEditorMode } from "@/domain/keybindings/preset";

export interface EditorModeDisposable {
  dispose(): void;
}

export interface EditorModeProvider {
  id: KeymapEditorMode;
  /**
   * Attaches the mode to one Monaco editor instance (typed loosely so the
   * domain layer stays free of Monaco imports). May load code lazily.
   */
  attach(editor: unknown, options: { statusElement?: HTMLElement | null }): Promise<EditorModeDisposable> | EditorModeDisposable;
  /** Key sequences the mode handles itself; the Monaco keymap layer skips rules on them. */
  ownsSequence?(sequence: KeySequence, platform: KeyboardPlatform): boolean;
}

const providers = new Map<KeymapEditorMode, EditorModeProvider>();
const listeners = new Set<() => void>();

/** Registers a provider; returns a disposer. A later registration replaces an earlier one. */
export function registerEditorModeProvider(provider: EditorModeProvider): () => void {
  providers.set(provider.id, provider);
  for (const listener of listeners) listener();
  return () => {
    if (providers.get(provider.id) !== provider) return;
    providers.delete(provider.id);
    for (const listener of listeners) listener();
  };
}

export function getEditorModeProvider(mode: KeymapEditorMode | undefined | null): EditorModeProvider | undefined {
  return mode ? providers.get(mode) : undefined;
}

export function subscribeEditorModeProviders(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
