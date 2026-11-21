/**
 * Values for plugin AI prompt template variables ({{selection}}, {{file}},
 * {{language}}), read from the last focused Monaco code editor. This module
 * only imports Monaco types: tracking attaches through the contribution hook
 * once an editor has loaded Monaco, so the AI composer never loads Monaco.
 *
 * `file` is the path relative to a project root; files outside the given
 * roots contribute only their base name, and non-file editors (diffs,
 * generated documents) contribute nothing, so absolute local paths never end
 * up in a prompt through a template.
 */
import type { AiPromptVariableValues } from "@/domain/ai/plugin-prompts";
import { registerMonacoContributionHook, type MonacoApi } from "@/services/monaco-contributions";

type CodeEditor = ReturnType<MonacoApi["editor"]["getEditors"]>[number];

let lastEditor: CodeEditor | undefined;
const tracked = new WeakSet<CodeEditor>();

function track(editor: CodeEditor) {
  if (tracked.has(editor)) return;
  tracked.add(editor);
  editor.onDidFocusEditorText(() => {
    lastEditor = editor;
  });
  editor.onDidDispose(() => {
    if (lastEditor === editor) lastEditor = undefined;
  });
  if (editor.hasTextFocus()) lastEditor = editor;
}

registerMonacoContributionHook((monaco) => {
  for (const editor of monaco.editor.getEditors()) track(editor);
  const subscription = monaco.editor.onDidCreateEditor((editor) => {
    // onDidCreateEditor also reports diff editors; only code editors have text focus events.
    if ("onDidFocusEditorText" in editor) track(editor as CodeEditor);
  });
  return () => {
    subscription.dispose();
    lastEditor = undefined;
  };
});

function normalizePath(value: string) {
  return value.replace(/\\/g, "/").replace(/\/+$/, "");
}

/** Path of `absolutePath` relative to the first containing root; the base name when none contains it. */
export function relativePromptFilePath(absolutePath: string, roots: readonly string[]): string {
  const path = normalizePath(absolutePath);
  const caseInsensitive = /^[A-Za-z]:\//.test(path);
  const comparable = (value: string) => (caseInsensitive ? value.toLowerCase() : value);
  for (const root of roots) {
    const base = normalizePath(root);
    if (!base) continue;
    if (comparable(path).startsWith(`${comparable(base)}/`)) return path.slice(base.length + 1);
  }
  return path.slice(path.lastIndexOf("/") + 1);
}

/**
 * Variable values from the last focused code editor, or an empty object when
 * no editor has been focused. The selection is the primary selection only;
 * the template expansion caps its size.
 */
export function currentAiPromptVariables(projectRoots: readonly string[]): AiPromptVariableValues {
  const editor = lastEditor;
  const model = editor?.getModel();
  if (!editor || !model) return {};
  const values: AiPromptVariableValues = { language: model.getLanguageId() };
  const selection = editor.getSelection();
  if (selection && !selection.isEmpty()) values.selection = model.getValueInRange(selection);
  if (model.uri.scheme === "file") values.file = relativePromptFilePath(model.uri.fsPath || model.uri.path, projectRoots);
  return values;
}
