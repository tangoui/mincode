/**
 * Completions (DAP completions) for an expression input built on
 * `el-autocomplete` (its `fetch-suggestions`, with `debounce` 0: the 150 ms
 * debounce is here). Only while the session is paused with a selected frame
 * and its adapter declares supportsCompletionsRequest; otherwise every call
 * answers an empty list and sends nothing. Each call first empties the shown
 * list (the old items belong to the old text), then requests the latest text
 * only; an answer that comes after a newer call, after `cancel`, or after the
 * session ran on or another frame was selected is dropped.
 */
import { onScopeDispose } from "vue";
import { applyDebugCompletion } from "@/domain/debug/completions";
import type { DebugSessionView } from "@/domain/debug/session-model";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

/** Quiet time after the last keystroke before the request is sent. */
export const DEBUG_COMPLETIONS_DEBOUNCE_MS = 150;
/** Items shown at most (java-debug answers some 150 for a two-letter prefix). */
const MAX_SUGGESTIONS = 100;

/** One row of the suggestion list: `value` is the input text once it is picked. */
export interface DebugCompletionSuggestion {
  value: string;
  label: string;
  type?: string;
}

/** True when completions can be requested for the session. */
export function debugCompletionsAvailable(view: DebugSessionView | null | undefined): view is DebugSessionView {
  return Boolean(
    view && view.state === "paused" && !view.noDebug && view.capabilities.supportsCompletionsRequest === true && view.selectedFrameId !== undefined
  );
}

/**
 * The cursor of the focused input when it holds `query` (fetch-suggestions
 * runs on its input event); the end of the text otherwise.
 */
function cursorIn(query: string): number {
  const element = typeof document === "undefined" ? null : document.activeElement;
  if (
    (element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement) &&
    element.value === query &&
    element.selectionStart !== null &&
    element.selectionStart === element.selectionEnd
  ) {
    return element.selectionStart;
  }
  return query.length;
}

export function useDebugCompletions(session: () => DebugSessionView | null) {
  const sessions = useDebugSessionsStore();
  let timer: ReturnType<typeof setTimeout> | undefined;
  let seq = 0;

  /** Drops the pending request and any answer still to come. */
  function cancel() {
    seq += 1;
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  }

  function fetchSuggestions(query: string, cb: (items: DebugCompletionSuggestion[]) => void): void {
    cancel();
    const token = seq;
    cb([]);
    const view = session();
    const cursor = cursorIn(query);
    const before = query.slice(0, cursor);
    if (!before.trim() || !debugCompletionsAvailable(view)) return;
    const id = view.id;
    const frameId = view.selectedFrameId;
    // DAP columns count UTF-16 code units, like JS strings; MinCode sends 1-based columns and lines.
    const lines = before.split("\n");
    const column = lines[lines.length - 1]!.length + 1;
    const line = lines.length > 1 ? lines.length : undefined;
    timer = setTimeout(() => {
      timer = undefined;
      void sessions.completions(id, query, column, line).then((items) => {
        const now = session();
        if (token !== seq || !debugCompletionsAvailable(now) || now.id !== id || now.selectedFrameId !== frameId) return;
        cb(
          items.slice(0, MAX_SUGGESTIONS).map((item) => ({
            value: applyDebugCompletion(query, item, cursor),
            label: item.label,
            ...(item.type ? { type: item.type } : {}),
          }))
        );
      });
    }, DEBUG_COMPLETIONS_DEBOUNCE_MS);
  }

  onScopeDispose(cancel);

  return { fetchSuggestions, apply: applyDebugCompletion, cancel };
}
