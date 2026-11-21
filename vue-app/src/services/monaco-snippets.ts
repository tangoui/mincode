/**
 * Monaco stage of the `snippets` contribution point. Holds the accepted
 * snippets per language (services/contributions/snippets.ts feeds it) and
 * keeps exactly one completion provider per language that has snippets;
 * a language whose last snippet goes away has its provider disposed.
 *
 * Providers read the registry at request time, so replacing the snippets of
 * a language never re-registers its provider. Suggestions are plain snippet
 * items (kind Snippet, InsertAsSnippet, sortText "~" so language-server
 * items rank first), at most SNIPPET_MAX_SUGGESTIONS per request, and never
 * in a read-only editor. Monaco itself is reached only through
 * `registerMonacoContributionHook`, so importing this module never loads it.
 */
import {
  SNIPPET_MAX_SUGGESTIONS,
  selectSnippetSuggestions,
  type SnippetItem,
  type SnippetPluginIssue,
} from "@/domain/editor/snippets";
import { registerMonacoContributionHook, type MonacoApi } from "@/services/monaco-contributions";
import { PLUGIN_LANGUAGES_CHANGED_EVENT } from "@/services/monaco-languages";
import { frameworkPluginAllowed } from "@/services/editor-framework-registry";

import type { editor, languages, Position } from "@/services/monaco-runtime.js";

type TextModel = editor.ITextModel;
type CompletionList = languages.CompletionList;
type Disposable = { dispose(): void };

const registry = new Map<string, readonly SnippetItem[]>();
const providers = new Map<string, Disposable>();
let languageIssues: SnippetPluginIssue[] = [];
let active: MonacoApi | undefined;
let unregisterHook: (() => void) | undefined;

/** True when the editor that shows `model` (the focused one, else every one) is read-only. */
function readOnlyModel(monaco: MonacoApi, model: TextModel): boolean {
  const editors = monaco.editor.getEditors().filter((editor) => editor.getModel() === model);
  if (editors.length === 0) return false;
  const readOnly = (editor: (typeof editors)[number]) => editor.getOption(monaco.editor.EditorOption.readOnly) === true;
  const focused = editors.find((editor) => editor.hasTextFocus());
  return focused ? readOnly(focused) : editors.every(readOnly);
}

function provide(monaco: MonacoApi, language: string, model: TextModel, position: Position): CompletionList {
  const items = registry.get(language);
  if (!items?.length || readOnlyModel(monaco, model)) return { suggestions: [] };
  const before = model.getLineContent(position.lineNumber).slice(0, position.column - 1);
  const typed = /\S*$/.exec(before)?.[0] ?? "";
  const path = model.uri?.scheme === "file" ? model.uri.path.replace(/^\/([A-Za-z]:\/)/, "$1") : "";
  const eligible = items.filter((item) => frameworkPluginAllowed(item.pluginId, path));
  const suggestions = selectSnippetSuggestions(eligible, typed, SNIPPET_MAX_SUGGESTIONS).map(({ item, replace }) => ({
    label: item.prefix,
    kind: monaco.languages.CompletionItemKind.Snippet,
    detail: `${item.name} · ${item.pluginName}`,
    // A plain string is rendered as text by Monaco, never as Markdown/HTML.
    documentation: item.description ? `${item.description}\n\n${item.preview}` : item.preview,
    insertText: item.body,
    insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet,
    sortText: "~",
    range: {
      startLineNumber: position.lineNumber,
      startColumn: position.column - replace,
      endLineNumber: position.lineNumber,
      endColumn: position.column,
    },
  }));
  return { suggestions, incomplete: suggestions.length >= SNIPPET_MAX_SUGGESTIONS };
}

/** Registers/disposes providers so exactly the known languages with snippets have one. */
function reconcile() {
  const monaco = active;
  if (!monaco) return;
  const known = new Set(monaco.languages.getLanguages().map((language) => language.id));
  const issues: SnippetPluginIssue[] = [];
  for (const [language, provider] of providers) {
    if (registry.has(language) && known.has(language)) continue;
    provider.dispose();
    providers.delete(language);
  }
  for (const [language, items] of registry) {
    if (!known.has(language)) {
      const reported = new Set<string>();
      for (const item of items) {
        const key = `${item.pluginId}\u0000${item.entry}`;
        if (reported.has(key)) continue;
        reported.add(key);
        issues.push({
          pluginId: item.pluginId,
          entry: item.entry,
          message: `unknown Monaco language "${language}"`,
          stage: "frontend",
        });
      }
      continue;
    }
    if (providers.has(language)) continue;
    providers.set(language, monaco.languages.registerCompletionItemProvider(language, {
      provideCompletionItems: (model, position) => provide(monaco, language, model, position),
    }));
  }
  languageIssues = issues;
}

/** Plugin languages register after this hook may have run; re-check unknown languages then. */
function handleLanguagesChanged() {
  reconcile();
}

function detach() {
  if (active && typeof window !== "undefined") window.removeEventListener(PLUGIN_LANGUAGES_CHANGED_EVENT, handleLanguagesChanged);
  for (const provider of providers.values()) provider.dispose();
  providers.clear();
  languageIssues = [];
  active = undefined;
}

/**
 * Attaches the registry to a loaded Monaco (the contribution hook calls
 * this; tests pass a stub). Returns a disposer that removes every provider.
 */
export function attachMonacoSnippets(monaco: MonacoApi): () => void {
  detach();
  active = monaco;
  if (typeof window !== "undefined") window.addEventListener(PLUGIN_LANGUAGES_CHANGED_EVENT, handleLanguagesChanged);
  reconcile();
  return detach;
}

/** Replaces the snippets for every language (an empty map removes them all). */
export function setMonacoSnippets(byLanguage: ReadonlyMap<string, readonly SnippetItem[]>): void {
  registry.clear();
  for (const [language, items] of byLanguage) {
    if (items.length) registry.set(language, items);
  }
  if (registry.size > 0 && !unregisterHook) {
    unregisterHook = registerMonacoContributionHook((monaco) => attachMonacoSnippets(monaco));
  }
  reconcile();
}

/** Monaco-stage issues (unknown languages) of the current registry. */
export function monacoSnippetIssues(): readonly SnippetPluginIssue[] {
  return languageIssues;
}

/** Languages that currently have a registered provider (diagnostics and tests). */
export function monacoSnippetLanguages(): string[] {
  return [...providers.keys()].sort();
}
