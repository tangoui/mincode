import { isImageDocument, type ProjectEditorDocument, type ProjectEditorPreferences } from "./types";

export type SaveOutcome = "saved" | "skipped" | "cancelled" | "failed";

export function isEditableProjectText(document: ProjectEditorDocument): boolean {
  // `debug-source` documents are the adapter's text, never written anywhere.
  return Boolean(document.path && document.loaded && !document.loading && !document.errorKey && !document.editor &&
    !isImageDocument(document.path) && !/\.pdf$/i.test(document.path) &&
    (!document.externalKind || document.externalKind === "text"));
}

export function cleanSavedText(text: string, policy: Pick<ProjectEditorPreferences, "trimTrailingWhitespace" | "insertFinalNewline">): string {
  const eol = text.includes("\r\n") ? "\r\n" : "\n";
  let result = policy.trimTrailingWhitespace ? text.replace(/[\t ]+(?=\r?$)/gm, "") : text;
  if (policy.insertFinalNewline && result && !/[\r\n]$/.test(result)) result += eol;
  return result;
}
