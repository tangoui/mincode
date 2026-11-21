/**
 * Save participants run before the project editor writes a text document (format on save).
 * ProjectWorkspaceView.saveDocument awaits runEditorSaveParticipants and then flushes the editor
 * content, so edits a participant applies to the Monaco model are part of the saved text.
 *
 * Participants run sequentially in registration order. A failing or slow participant never
 * blocks the save: its error is caught and returned, and each participant is abandoned after
 * EDITOR_SAVE_PARTICIPANT_MAX_MS even if it enforces no timeout of its own. Participants show
 * their own user-facing messages; the runner never does.
 */

export interface EditorSaveContext {
  /** Absolute document path. */
  path: string;
  signal?: AbortSignal;
  isCurrent?: () => boolean;
  formatOnSave?: boolean;
  /** Supplied by the owning editor so Save All targets the right model. */
  format?: (signal: AbortSignal) => Promise<void>;
  formatterHandled?: boolean;
  /** Project root for project documents; undefined for external files. */
  projectRoot?: string;
  /** True for files opened outside every project root. */
  external: boolean;
}

export type EditorSaveParticipant = (context: EditorSaveContext) => Promise<void>;

export interface EditorSaveParticipantFailure {
  error: unknown;
}

/** Hard cap per participant; participants are expected to use a shorter timeout of their own. */
export const EDITOR_SAVE_PARTICIPANT_MAX_MS = 5000;

const participants: EditorSaveParticipant[] = [];

/** Registers a participant; the returned function removes it. */
export function registerEditorSaveParticipant(participant: EditorSaveParticipant): () => void {
  participants.push(participant);
  return () => {
    const index = participants.indexOf(participant);
    if (index >= 0) participants.splice(index, 1);
  };
}

class EditorSaveParticipantTimeout extends Error {
  constructor() {
    super(`save participant exceeded ${EDITOR_SAVE_PARTICIPANT_MAX_MS} ms`);
    this.name = "EditorSaveParticipantTimeout";
  }
}

async function runBounded(participant: EditorSaveParticipant, context: EditorSaveContext): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const controller = new AbortController();
  const abort = () => controller.abort();
  if (context.signal?.aborted) abort();
  context.signal?.addEventListener("abort", abort, { once: true });
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => { controller.abort(); reject(new EditorSaveParticipantTimeout()); }, EDITOR_SAVE_PARTICIPANT_MAX_MS);
  });
  try {
    await Promise.race([participant({ ...context, signal: controller.signal }), timeout]);
  } finally {
    clearTimeout(timer);
    controller.abort();
    context.signal?.removeEventListener("abort", abort);
  }
}

/** Runs every participant in order; resolves with the failures and never rejects. */
export async function runEditorSaveParticipants(context: EditorSaveContext): Promise<EditorSaveParticipantFailure[]> {
  const failures: EditorSaveParticipantFailure[] = [];
  if (context.format) {
    try { await runBounded((bounded) => context.format!(bounded.signal!), context); }
    catch (error) { failures.push({ error }); }
  }
  for (const participant of [...participants]) {
    if (context.signal?.aborted || context.isCurrent?.() === false) break;
    try {
      await runBounded(participant, context);
    } catch (error) {
      failures.push({ error });
    }
  }
  return failures;
}

export type EditorSavedListener = (context: EditorSaveContext) => void;

const savedListeners = new Set<EditorSavedListener>();

/**
 * Subscribes to successful saves (after the file was written; e.g. lint on save).
 * Listeners must not block: errors are swallowed. Returns the unsubscribe function.
 */
export function onEditorSaved(listener: EditorSavedListener): () => void {
  savedListeners.add(listener);
  return () => {
    savedListeners.delete(listener);
  };
}

/** Called once by the editor after a document was written successfully. */
export function notifyEditorSaved(context: EditorSaveContext): void {
  for (const listener of [...savedListeners]) {
    try {
      listener({ ...context });
    } catch {
      // A failing listener never affects the save or other listeners.
    }
  }
}
