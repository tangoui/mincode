import { onBeforeUnmount, watch, type Ref } from "vue";
import type { ProjectEditorDocument, ProjectEditorPreferences } from "@/domain/projects/types";
import { isEditableProjectText } from "@/domain/projects/save-policy";
import { isProjectDocumentDirty } from "@/domain/projects/document-dirty";

/** Only timers are view-owned; in-flight disk writes belong to the runtime coordinator. */
export function useProjectAutoSave(options: {
  active: Ref<boolean>;
  scope: () => string;
  documents: () => ProjectEditorDocument[];
  preferences: () => ProjectEditorPreferences;
  enabled: () => boolean;
  editable?: (doc: ProjectEditorDocument) => boolean;
  save: (path: string, valid: () => boolean) => Promise<unknown>;
}) {
  const timers = new Map<string, ReturnType<typeof setTimeout>>();
  let generation = 0;
  let disposed = false;
  const requested = new Set<string>();
  const failedPaths = new Set<string>();
  const eligible = (doc: ProjectEditorDocument) => options.enabled() && options.active.value && !failedPaths.has(doc.path) &&
    isEditableProjectText(doc) && options.editable?.(doc) !== false;
  function clear() {
    generation++;
    for (const timer of timers.values()) clearTimeout(timer);
    timers.clear();
    failedPaths.clear();
  }
  function failed(path: string) {
    const timer = timers.get(path);
    if (timer) clearTimeout(timer);
    timers.delete(path);
    failedPaths.add(path);
  }
  function request(path: string) {
    const doc = options.documents().find((item) => item.path === path);
    if (!doc || !eligible(doc) || !isProjectDocumentDirty(doc)) return;
    const scope = options.scope(), sessionId = doc.sessionId, captured = generation;
    const key = JSON.stringify([scope, sessionId, path, captured]);
    if (requested.has(key)) return;
    requested.add(key);
    const valid = () => !disposed && captured === generation && options.scope() === scope && options.active.value &&
      options.documents().some((item) => item.path === path && item.sessionId === sessionId && eligible(item));
    // Let tab transitions and deferred model synchronization finish before entering the save queue.
    void Promise.resolve().then(() => {
      requested.delete(key);
      if (valid() && isProjectDocumentDirty(doc)) return options.save(path, valid);
    })
      .catch(() => undefined).finally(() => requested.delete(key));
  }
  function edited(path: string) {
    failedPaths.delete(path);
    const previous = timers.get(path);
    if (previous) clearTimeout(previous);
    timers.delete(path);
    const doc = options.documents().find((item) => item.path === path);
    if (options.preferences().autoSave !== "afterDelay" || !doc || !eligible(doc) || !isProjectDocumentDirty(doc)) return;
    timers.set(path, setTimeout(() => { timers.delete(path); request(path); }, options.preferences().autoSaveDelay));
  }
  function departure(path: string) {
    if (options.preferences().autoSave === "onFocusChange") request(path);
  }
  function windowBlur() {
    if (options.preferences().autoSave !== "onWindowChange") return;
    for (const doc of options.documents()) request(doc.path);
  }
  watch(() => [options.active.value, options.scope(), options.enabled(), options.preferences()], () => {
    clear();
    if (options.active.value && options.preferences().autoSave === "afterDelay") {
      for (const doc of options.documents()) edited(doc.path);
    }
  }, { flush: "sync", immediate: true });
  watch(() => options.documents().map((doc) => `${doc.path}:${doc.sessionId}:${Boolean(doc.editor)}`).join("\n"), () => {
    // A close/rename/replacement invalidates queued work as well as its timer.
    clear();
    if (options.preferences().autoSave === "afterDelay") for (const doc of options.documents()) edited(doc.path);
  }, { flush: "sync" });
  window.addEventListener("blur", windowBlur);
  onBeforeUnmount(() => { disposed = true; clear(); window.removeEventListener("blur", windowBlur); });
  return { edited, departure, clear, failed };
}
