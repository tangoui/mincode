export interface AiMessageExpansionTarget {
  id?: string;
  createdAt?: number;
}

const DEFAULT_REMEMBERED_LIMIT = 2_000;

/**
 * Decides whether a long message starts expanded. Messages that arrived during this app run stay
 * readable; long messages restored from history start folded. Nothing folds while it is being
 * read, because shrinking content above the reader moves the text they are looking at. A choice
 * the user made is kept until the app restarts, so switching conversations does not undo it.
 */
export function createAiMessageExpansion(sessionStartedAt = Date.now(), limit = DEFAULT_REMEMBERED_LIMIT) {
  const remembered = new Map<string, boolean>();
  return {
    initiallyExpanded(message: AiMessageExpansionTarget, streaming: boolean) {
      if (!message.id) return true;
      const choice = remembered.get(message.id);
      if (choice !== undefined) return choice;
      return streaming || (message.createdAt ?? 0) >= sessionStartedAt;
    },
    remember(id: string, expanded: boolean) {
      remembered.delete(id);
      remembered.set(id, expanded);
      while (remembered.size > limit) {
        const oldest = remembered.keys().next().value;
        if (oldest === undefined) break;
        remembered.delete(oldest);
      }
    },
  };
}

/** Shared by every message bubble for the lifetime of the app window. */
export const aiMessageExpansion = createAiMessageExpansion();
