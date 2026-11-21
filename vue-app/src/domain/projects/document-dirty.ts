import type { ProjectEditorDocument } from "@/domain/projects/types";

// While a Monaco model owns a document, `content` is synced lazily and the editor
// reports dirtiness from the model's version, so readers must not diff full strings.
type DirtyTrackedDocument = ProjectEditorDocument & { dirty?: boolean };

export function isProjectDocumentDirty(document: ProjectEditorDocument): boolean {
  if (!document.loaded) return false;
  const dirty = (document as DirtyTrackedDocument).dirty;
  return dirty ?? document.content !== document.savedContent;
}

export function setProjectDocumentDirty(document: ProjectEditorDocument, dirty: boolean | undefined) {
  const tracked = document as DirtyTrackedDocument;
  if (tracked.dirty !== dirty) tracked.dirty = dirty;
}
