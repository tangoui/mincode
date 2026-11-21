import type { EditorSaveContext } from "@/services/editor-save-participants";
import type { MonacoApi } from "@/services/monaco-contributions";
import type { editor } from "@/services/monaco-runtime.js";

const SAVE_FORMAT_TIMEOUT_MS = 3000;

/** Formats only the model being saved and cancels formatting if that model changes. */
export async function formatProjectModelOnSave(
  monaco: MonacoApi, model: editor.ITextModel, context: EditorSaveContext,
): Promise<void> {
  if (context.signal?.aborted || context.isCurrent?.() === false || model.isDisposed()) return;
  if (!context.formatOnSave) return;
  const controller = new AbortController();
  const abort = () => controller.abort();
  let timedOut = false;
  context.signal?.addEventListener("abort", abort, { once: true });
  const disposed = model.onWillDispose(abort);
  const changed = model.onDidChangeContent(abort);
  const timer = setTimeout(() => { timedOut = true; abort(); }, SAVE_FORMAT_TIMEOUT_MS);
  try {
    await monaco.mincodeSaveFormatting.run(model, (providers) => providers[0], controller.signal);
    if (timedOut) throw new Error("Save formatting timed out");
  } finally {
    clearTimeout(timer);
    controller.abort();
    disposed.dispose();
    changed.dispose();
    context.signal?.removeEventListener("abort", abort);
  }
}
