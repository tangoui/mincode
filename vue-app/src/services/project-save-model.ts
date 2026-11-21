import * as monaco from "@/services/monaco-runtime.js";
import { acquireMonacoModel, releaseMonacoModel } from "@/services/monaco-model-pool";
import { projectEditorModelUri } from "@/composables/project-file-uri";
import { detectMonacoEditorLanguage } from "@/services/monaco-languages";
import { formatProjectModelOnSave } from "@/services/monaco-formatters";
import { cleanSavedText } from "@/domain/projects/save-policy";
import type { ProjectEditorPreferences } from "@/domain/projects/types";
import type { EditorSaveContext } from "@/services/editor-save-participants";

/** Lazy fallback for a loaded text tab currently shown as preview, without activating the tab. */
export async function prepareProjectSaveModel(content: string, preferences: ProjectEditorPreferences, context: EditorSaveContext): Promise<string | undefined> {
  if (context.signal?.aborted || context.isCurrent?.() === false) return;
  const uri = projectEditorModelUri(context.path, context.projectRoot);
  const model = acquireMonacoModel(uri, () => monaco.editor.createModel(content,
    detectMonacoEditorLanguage(monaco, context.path, content), uri));
  try {
    // A different mounted editor may already lease this URI. Never format or write its newer text.
    if (model.getValue() !== content) return;
    model.updateOptions({ tabSize: preferences.tabSize, insertSpaces: true });
    await formatProjectModelOnSave(monaco, model, context);
    if (context.signal?.aborted || context.isCurrent?.() === false || model.isDisposed()) return;
    const text = cleanSavedText(model.getValue(), preferences);
    if (text !== model.getValue()) {
      model.pushStackElement();
      model.pushEditOperations([], [{ range: model.getFullModelRange(), text }], () => null);
      model.pushStackElement();
    }
    return text;
  } finally { releaseMonacoModel(model); }
}
